package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFiles embed.FS

const address = "127.0.0.1:8640"
const maxFrame = 16 * 1024 * 1024

type Camera struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Host   string `json:"host"`
	Code   string `json:"code,omitempty"`
	Source string `json:"source,omitempty"`
}
type Frame struct {
	JPEG []byte
	At   time.Time
	Seq  uint64
}
type Feed struct {
	mu      sync.Mutex
	frame   Frame
	state   string
	cancel  context.CancelFunc
	viewers int
}

func (f *Feed) status() (string, float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	age := -1.0
	if !f.frame.At.IsZero() {
		age = time.Since(f.frame.At).Seconds()
	}
	return f.state, age
}
func (f *Feed) publish(data []byte) error {
	if len(data) < 4 || len(data) > maxFrame || data[0] != 255 || data[1] != 216 || data[len(data)-2] != 255 || data[len(data)-1] != 217 {
		return errors.New("invalid JPEG")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frame = Frame{data, time.Now(), f.frame.Seq + 1}
	f.state = "live"
	return nil
}
func (f *Feed) setState(state string) { f.mu.Lock(); f.state = state; f.mu.Unlock() }

type App struct {
	mu         sync.Mutex
	cameras    [4]Camera
	feeds      [4]*Feed
	configPath string
	bridge     string
	ctx        context.Context
	shutdown   context.CancelFunc
	transport  *http.Transport
}

func validCamera(c Camera) error {
	if c.Kind == "" {
		return nil
	}
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 60 {
		return errors.New("Enter a name (up to 60 characters).")
	}
	switch c.Kind {
	case "p1p", "x1c":
		if c.Host == "" || strings.ContainsAny(c.Host, "/\\@?# \t\r\n") {
			return errors.New("Enter only the printer IP address or hostname.")
		}
		if strings.Contains(c.Host, ":") && net.ParseIP(c.Host) == nil {
			return errors.New("Do not include a port in the printer address.")
		}
		if len(c.Code) < 1 || len(c.Code) > 32 {
			return errors.New("Enter the printer LAN access code.")
		}
		for _, r := range c.Code {
			if r < 32 || r > 126 {
				return errors.New("Access code must contain ASCII characters.")
			}
		}
	case "mjpeg", "rtsp":
		u, e := url.Parse(c.Source)
		if e != nil || u.Hostname() == "" || u.Fragment != "" {
			return errors.New("Enter a complete camera stream URL without a fragment.")
		}
		if c.Kind == "mjpeg" && u.Scheme != "http" && u.Scheme != "https" {
			return errors.New("Use an HTTP or HTTPS MJPEG/snapshot URL.")
		}
		if c.Kind == "rtsp" && u.Scheme != "rtsp" && u.Scheme != "rtsps" && u.Scheme != "rtspx" {
			return errors.New("Use an RTSP camera URL.")
		}
	default:
		return errors.New("Unsupported camera type.")
	}
	return nil
}
func source(c Camera) string {
	if c.Kind == "x1c" {
		return (&url.URL{Scheme: "rtspx", User: url.UserPassword("bblp", c.Code), Host: net.JoinHostPort(c.Host, "322"), Path: "/streaming/live/1"}).String() + "#media=video#backchannel=0"
	}
	return c.Source
}
func (a *App) load() error {
	b, e := os.ReadFile(a.configPath)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	b, e = unprotect(b)
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, &a.cameras); e != nil {
		return e
	}
	for _, c := range a.cameras {
		if e = validCamera(c); e != nil {
			return e
		}
	}
	return nil
}
func (a *App) save(cameras [4]Camera) error {
	b, e := json.Marshal(cameras)
	if e != nil {
		return e
	}
	b, e = protect(b)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(a.configPath), 0700); e != nil {
		return e
	}
	temp := a.configPath + ".tmp"
	if e = os.WriteFile(temp, b, 0600); e != nil {
		return e
	}
	return replaceFile(temp, a.configPath)
}
func (a *App) configureBridge(i int, c Camera) error {
	if a.bridge == "" {
		return errors.New("Camera bridge is unavailable. Restart PrinterCam.")
	}
	q := url.Values{"name": {fmt.Sprintf("camera%d", i)}, "src": {fmt.Sprintf("camera%d", i)}}
	method := "DELETE"
	if c.Kind == "x1c" || c.Kind == "rtsp" {
		method = "PATCH"
		q.Set("src", source(c))
	}
	req, _ := http.NewRequest(method, a.bridge+"/api/streams?"+q.Encode(), nil)
	client := http.Client{Timeout: 5 * time.Second}
	res, e := client.Do(req)
	if e != nil {
		return errors.New("Camera bridge is unavailable. Restart PrinterCam.")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return errors.New("Camera bridge could not register this stream.")
	}
	return nil
}
func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func apiError(w http.ResponseWriter, s string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": s})
}
func slot(r *http.Request) (int, error) {
	i, e := strconv.Atoi(r.PathValue("id"))
	if e != nil || i < 0 || i > 3 {
		return 0, errors.New("slot")
	}
	return i, nil
}
func (a *App) list(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	list := make([]any, 4)
	for i, c := range a.cameras {
		state, age := a.feeds[i].status()
		list[i] = map[string]any{"id": i, "name": c.Name, "kind": c.Kind, "host": c.Host, "hasCode": c.Code != "", "hasSource": c.Source != "", "state": state, "age": age}
	}
	jsonResponse(w, map[string]any{"cameras": list, "revision": "001"})
}
func (a *App) update(w http.ResponseWriter, r *http.Request) {
	i, e := slot(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	var c Camera
	if e = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&c); e != nil {
		apiError(w, "Invalid camera settings.", 400)
		return
	}
	c.Name = strings.TrimSpace(c.Name)
	c.Host = strings.TrimSpace(c.Host)
	c.Source = strings.TrimSpace(c.Source)
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.cameras[i]
	if c.Kind == old.Kind {
		if c.Code == "" {
			c.Code = old.Code
		}
		if c.Source == "" {
			c.Source = old.Source
		}
	}
	if e = validCamera(c); e != nil {
		apiError(w, e.Error(), 400)
		return
	}
	if c.Kind == "" {
		c = Camera{}
	}
	if c.Kind == "x1c" || c.Kind == "rtsp" {
		if e = a.configureBridge(i, c); e != nil {
			apiError(w, e.Error(), 503)
			return
		}
	}
	next := a.cameras
	next[i] = c
	if e = a.save(next); e != nil {
		a.configureBridge(i, old)
		apiError(w, "Could not save settings on this computer.", 500)
		return
	}
	f := a.feeds[i]
	f.mu.Lock()
	if f.cancel != nil {
		f.cancel()
	}
	f.mu.Unlock()
	a.feeds[i] = &Feed{state: "idle"}
	a.cameras = next
	if c.Kind != "x1c" && c.Kind != "rtsp" {
		a.configureBridge(i, c)
	}
	jsonResponse(w, map[string]bool{"ok": true})
}
func readP1P(ctx context.Context, c Camera, f *Feed) error {
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 8 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true}} // Bambu LAN cameras use self-signed certificates.
	conn, e := dialer.DialContext(ctx, "tcp", net.JoinHostPort(c.Host, "6000"))
	if e != nil {
		return e
	}
	defer conn.Close()
	return consumeP1P(ctx, conn, c, f)
}
func consumeP1P(ctx context.Context, conn net.Conn, c Camera, f *Feed) error {
	var e error
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	auth := make([]byte, 80)
	binary.LittleEndian.PutUint32(auth, 0x40)
	binary.LittleEndian.PutUint32(auth[4:], 0x3000)
	copy(auth[16:], "bblp")
	copy(auth[48:], c.Code)
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, e = io.Copy(conn, bytes.NewReader(auth)); e != nil {
		return e
	}
	clear(auth)
	header := make([]byte, 16)
	for {
		conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		if _, e = io.ReadFull(conn, header); e != nil {
			return e
		}
		size := binary.LittleEndian.Uint32(header)
		if size < 4 || size > maxFrame {
			return errors.New("invalid frame size")
		}
		b := make([]byte, size)
		if _, e = io.ReadFull(conn, b); e != nil {
			return e
		}
		if e = f.publish(b); e != nil {
			return e
		}
	}
}
func (a *App) readMJPEG(ctx context.Context, c Camera, f *Feed) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := time.AfterFunc(20*time.Second, cancel)
	defer idle.Stop()
	req, e := http.NewRequestWithContext(ctx, "GET", c.Source, nil)
	if e != nil {
		return e
	}
	client := http.Client{Transport: a.transport}
	// Cancel only stalled reads; healthy MJPEG streams stay connected.
	res, e := client.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("camera HTTP status")
	}
	media, p, e := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if e != nil {
		return e
	}
	if media == "image/jpeg" {
		b, e := io.ReadAll(io.LimitReader(res.Body, maxFrame+1))
		if e != nil {
			return e
		}
		if e = f.publish(b); e != nil {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(700 * time.Millisecond):
			return nil
		}
	}
	if !strings.HasPrefix(media, "multipart/") || p["boundary"] == "" {
		return errors.New("Use the camera MJPEG URL, not the Mainsail page URL")
	}
	reader := multipart.NewReader(res.Body, p["boundary"])
	for {
		part, e := reader.NextPart()
		if e != nil {
			return e
		}
		b, e := io.ReadAll(io.LimitReader(part, maxFrame+1))
		part.Close()
		if e != nil {
			return e
		}
		if e = f.publish(b); e != nil {
			return e
		}
		idle.Reset(20 * time.Second)
	}
}
func (a *App) runFeed(ctx context.Context, c Camera, f *Feed) {
	for ctx.Err() == nil {
		f.setState("connecting")
		var e error
		if c.Kind == "p1p" {
			e = readP1P(ctx, c, f)
		} else {
			e = a.readMJPEG(ctx, c, f)
		}
		if ctx.Err() != nil {
			return
		}
		if e == nil {
			continue
		}
		f.setState("retrying")
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
func (a *App) feed(w http.ResponseWriter, r *http.Request) {
	i, e := slot(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	a.mu.Lock()
	c := a.cameras[i]
	f := a.feeds[i]
	if c.Kind == "" {
		a.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if c.Kind == "x1c" || c.Kind == "rtsp" {
		a.mu.Unlock()
		target, _ := url.Parse(a.bridge)
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = "/api/stream.mp4"
			req.URL.RawQuery = url.Values{"src": {fmt.Sprintf("camera%d", i)}, "video": {"h264,h265"}}.Encode()
			req.Host = target.Host
			req.Header.Del("Cookie")
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			apiError(w, "Stream unavailable. Check printer address, LAN access and camera settings.", 502)
		}
		proxy.ModifyResponse = func(res *http.Response) error {
			if res.StatusCode >= 400 {
				res.Body.Close()
				res.Body = io.NopCloser(strings.NewReader("Camera stream unavailable"))
				res.ContentLength = -1
				res.Header.Del("Content-Length")
			}
			return nil
		}
		proxy.FlushInterval = -1
		proxy.ServeHTTP(w, r)
		return
	}
	f.mu.Lock()
	f.viewers++
	if f.cancel == nil {
		ctx, cancel := context.WithCancel(a.ctx)
		f.cancel = cancel
		go a.runFeed(ctx, c, f)
	}
	f.mu.Unlock()
	a.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.viewers--
		if f.viewers == 0 {
			f.cancel()
			f.cancel = nil
			f.state = "idle"
			f.frame = Frame{}
		}
		f.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var seq uint64
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			f.mu.Lock()
			frame := f.frame
			f.mu.Unlock()
			if frame.Seq == seq || len(frame.JPEG) == 0 {
				continue
			}
			seq = frame.Seq
			http.NewResponseController(w).SetWriteDeadline(time.Now().Add(8 * time.Second))
			if _, e = fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame.JPEG)); e != nil {
				return
			}
			if _, e = w.Write(frame.JPEG); e != nil {
				return
			}
			if _, e = w.Write([]byte("\r\n")); e != nil {
				return
			}
			flusher.Flush()
		}
	}
}
func (a *App) discover(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL string `json:"url"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&input) != nil {
		apiError(w, "Enter a Moonraker URL.", 400)
		return
	}
	u, e := url.Parse(strings.TrimSpace(input.URL))
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		apiError(w, "Enter an HTTP Moonraker base URL.", 400)
		return
	}
	base := *u
	u.Path = strings.TrimRight(u.Path, "/") + "/server/webcams/list"
	u.RawQuery = ""
	u.Fragment = ""
	client := http.Client{Timeout: 8 * time.Second}
	res, e := client.Get(u.String())
	if e != nil {
		apiError(w, "Cannot reach Moonraker. Enter the camera stream URL directly.", 502)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		apiError(w, "Moonraker denied discovery. Enter the camera stream URL directly.", 502)
		return
	}
	var doc struct {
		Result struct {
			Webcams []struct {
				Name    string `json:"name"`
				Stream  string `json:"stream_url"`
				Service string `json:"service"`
			} `json:"webcams"`
		} `json:"result"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1024*1024)).Decode(&doc) != nil {
		apiError(w, "Invalid Moonraker response.", 502)
		return
	}
	list := []map[string]string{}
	for _, cam := range doc.Result.Webcams {
		ref, e := url.Parse(cam.Stream)
		if e != nil || cam.Stream == "" {
			continue
		}
		resolved := base.ResolveReference(ref)
		list = append(list, map[string]string{"name": cam.Name, "url": resolved.String(), "service": cam.Service})
	}
	jsonResponse(w, list)
}
func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/cameras", a.list)
	mux.HandleFunc("PUT /api/cameras/{id}", a.update)
	mux.HandleFunc("GET /feed/{id}", a.feed)
	mux.HandleFunc("POST /api/discover", a.discover)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]string{"app": "PrinterCam", "revision": "001"})
	})
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]bool{"ok": true})
		go func() { time.Sleep(200 * time.Millisecond); a.shutdown() }()
	})
	assets, _ := fs.Sub(webFiles, "web")
	mux.Handle("/", http.FileServer(http.FS(assets)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, e := net.SplitHostPort(r.Host)
		if e != nil || (host != "127.0.0.1" && host != "localhost") {
			http.Error(w, "Local access only", 403)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			http.Error(w, "Origin denied", 403)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-PrinterCam") != "1" {
			http.Error(w, "Request denied", 403)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob:; media-src 'self' blob:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	})
}
func startBridge(ctx context.Context, exe, dir string) (string, func(), error) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return "", func() {}, e
	}
	addr := l.Addr().String()
	l.Close()
	config := map[string]any{"api": map[string]any{"listen": addr}, "rtsp": map[string]string{"listen": ""}, "webrtc": map[string]string{"listen": ""}, "log": map[string]string{"level": "error"}}
	b, _ := json.Marshal(config)
	path := filepath.Join(dir, "bridge.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		return "", func() {}, e
	}
	cmd := exec.CommandContext(ctx, exe, "-config", path)
	quietProcess(cmd)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if e = cmd.Start(); e != nil {
		return "", func() {}, e
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	cleanup := func() { cmd.Process.Kill(); <-done }
	client := http.Client{Timeout: time.Second}
	for n := 0; n < 40; n++ {
		res, e := client.Get("http://" + addr + "/api")
		if e == nil {
			res.Body.Close()
			return "http://" + addr, cleanup, nil
		}
		select {
		case <-ctx.Done():
			return "", cleanup, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return "", cleanup, errors.New("bridge did not start")
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	l, e := net.Listen("tcp", address)
	if e != nil {
		client := http.Client{Timeout: time.Second}
		res, err := client.Get("http://" + address + "/api/health")
		if err == nil {
			defer res.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
			if bytes.Contains(b, []byte(`"app":"PrinterCam"`)) {
				openBrowser("http://" + address)
				return
			}
		}
		log.Fatal("Port 8640 is busy. Close the program using it and try again.")
	}
	dir, e := os.UserConfigDir()
	if e != nil {
		log.Fatal("Cannot find your settings directory")
	}
	dir = filepath.Join(dir, "PrinterCam")
	if os.MkdirAll(dir, 0700) != nil {
		log.Fatal("Cannot create settings directory")
	}
	exe, _ := os.Executable()
	bridgeExe := filepath.Join(filepath.Dir(exe), "bin", bridgeName())
	if v := os.Getenv("PRINTERCAM_BRIDGE"); v != "" {
		bridgeExe = v
	}
	bridge, cleanupBridge, e := startBridge(ctx, bridgeExe, dir)
	defer cleanupBridge()
	if e != nil {
		log.Print("RTSP bridge unavailable; P1P and MJPEG cameras remain available.")
	}
	a := &App{configPath: filepath.Join(dir, "cameras.dat"), bridge: bridge, ctx: ctx, shutdown: cancel, transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Second}}
	for i := range a.feeds {
		a.feeds[i] = &Feed{state: "idle"}
	}
	if e = a.load(); e != nil {
		log.Print("Cannot read saved camera settings. Your existing settings were preserved.")
		return
	}
	for i, c := range a.cameras {
		if c.Kind == "x1c" || c.Kind == "rtsp" {
			a.configureBridge(i, c)
		}
	}
	server := &http.Server{Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() { <-ctx.Done(); server.Close() }()
	log.Print("PrinterCam revision 001: http://" + address + " — leave this window running; Ctrl+C stops it.")
	if os.Getenv("PRINTERCAM_NO_BROWSER") == "" {
		openBrowser("http://" + address)
	}
	if e = server.Serve(l); e != nil && !errors.Is(e, http.ErrServerClosed) {
		log.Print("Dashboard stopped")
	}
}
