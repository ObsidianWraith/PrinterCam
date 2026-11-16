package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var jpeg = []byte{255, 216, 1, 2, 3, 255, 217}

func testApp(t *testing.T) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := &App{ctx: ctx, shutdown: cancel, configPath: filepath.Join(t.TempDir(), "cameras.dat"), transport: &http.Transport{ResponseHeaderTimeout: time.Second}}
	for i := range a.feeds {
		a.feeds[i] = &Feed{state: "idle"}
	}
	return a
}
func request(a *App, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8640"+path, strings.NewReader(body))
	r.Header.Set("X-PrinterCam", "1")
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	return w
}
func TestValidation(t *testing.T) {
	for _, c := range []Camera{{Name: "test", Kind: "rtsp", Source: "exec:cmd"}, {Name: "test", Kind: "mjpeg", Source: "file:///etc/passwd"}, {Name: "test", Kind: "p1p", Host: "printer/extra", Code: "x"}, {Name: "test", Kind: "x1c", Host: "printer", Code: strings.Repeat("x", 33)}} {
		if validCamera(c) == nil {
			t.Fatalf("accepted unsafe or invalid camera: %s", c.Kind)
		}
	}
	if validCamera(Camera{Name: "Printer", Kind: "p1p", Host: "192.168.1.2", Code: "12345678"}) != nil {
		t.Fatal("valid P1P rejected")
	}
}
func TestSettingsAndSecretRedaction(t *testing.T) {
	a := testApp(t)
	w := request(a, "PUT", "/api/cameras/0", `{"name":"P1P","kind":"p1p","host":"printer.local","code":"private-code"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(a, "GET", "/api/cameras", "")
	if strings.Contains(w.Body.String(), "private-code") {
		t.Fatal("secret exposed")
	}
	w = request(a, "PUT", "/api/cameras/0", `{"name":"Renamed","kind":"p1p","host":"printer.local"}`)
	if w.Code != 200 || a.cameras[0].Code != "private-code" {
		t.Fatal("blank edit cleared secret")
	}
	b := testApp(t)
	b.configPath = a.configPath
	if b.load() != nil || b.cameras[0].Name != "Renamed" {
		t.Fatal("settings did not persist")
	}
	w = request(a, "PUT", "/api/cameras/0", `{"kind":""}`)
	if w.Code != 200 || a.cameras[0].Code != "" {
		t.Fatal("remove failed")
	}
}
func TestRequestsRejectOtherWebsites(t *testing.T) {
	a := testApp(t)
	for _, scenario := range []string{"origin", "host", "header"} {
		r := httptest.NewRequest("PUT", "http://127.0.0.1:8640/api/cameras/0", strings.NewReader(`{"kind":""}`))
		r.Header.Set("X-PrinterCam", "1")
		if scenario == "origin" {
			r.Header.Set("Origin", "https://other.example")
		}
		if scenario == "host" {
			r.Host = "other.example:8640"
		}
		if scenario == "header" {
			r.Header.Del("X-PrinterCam")
		}
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("accepted", scenario)
		}
	}
}
func TestP1PFragmentedFrameAndAuth(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &Feed{}
	done := make(chan error, 1)
	go func() { done <- consumeP1P(ctx, left, Camera{Code: "12345678"}, f) }()
	auth := make([]byte, 80)
	right.SetDeadline(time.Now().Add(2 * time.Second))
	if _, e := io.ReadFull(right, auth); e != nil {
		t.Fatal(e)
	}
	if binary.LittleEndian.Uint32(auth) != 0x40 || binary.LittleEndian.Uint32(auth[4:]) != 0x3000 || string(auth[16:20]) != "bblp" || string(auth[48:56]) != "12345678" {
		t.Fatal("wrong authentication packet")
	}
	head := make([]byte, 16)
	binary.LittleEndian.PutUint32(head, uint32(len(jpeg)))
	for _, b := range append(head, jpeg...) {
		if _, e := right.Write([]byte{b}); e != nil {
			t.Fatal(e)
		}
	}
	for n := 0; n < 100; n++ {
		state, _ := f.status()
		if state == "live" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	f.mu.Lock()
	match := bytes.Equal(f.frame.JPEG, jpeg)
	f.mu.Unlock()
	if !match {
		t.Fatal("frame corruption")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not close reader")
	}
}
func TestRejectOversizedP1PFrame(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- consumeP1P(ctx, left, Camera{Code: "test"}, &Feed{}) }()
	io.ReadFull(right, make([]byte, 80))
	head := make([]byte, 16)
	binary.LittleEndian.PutUint32(head, maxFrame+1)
	right.Write(head)
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("oversize accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("reader hung")
	}
}
func TestMJPEGAndSnapshot(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(map[bool]string{false: "snapshot", true: "multipart"}[multi], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !multi {
					w.Header().Set("Content-Type", "image/jpeg")
					w.Write(jpeg)
					return
				}
				w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
				io.WriteString(w, "--frame\r\nContent-Type: image/jpeg\r\n\r\n")
				w.Write(jpeg)
				io.WriteString(w, "\r\n--frame--\r\n")
			}))
			defer server.Close()
			a := testApp(t)
			f := &Feed{}
			a.readMJPEG(a.ctx, Camera{Source: server.URL}, f)
			if state, _ := f.status(); state != "live" {
				t.Fatal("no frame")
			}
		})
	}
}
func TestFeedSharesOneCameraConnection(t *testing.T) {
	a := testApp(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpeg)
	}))
	defer upstream.Close()
	a.cameras[0] = Camera{Name: "test", Kind: "mjpeg", Source: upstream.URL}
	server := httptest.NewServer(a.handler())
	defer server.Close()
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	clients := []*http.Response{}
	for n := 0; n < 2; n++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/feed/0", nil)
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		clients = append(clients, res)
	}
	a.feeds[0].mu.Lock()
	viewers := a.feeds[0].viewers
	a.feeds[0].mu.Unlock()
	if viewers != 2 {
		t.Fatal("missing viewers")
	}
	reader := multipart.NewReader(clients[0].Body, "frame")
	part, e := reader.NextPart()
	if e != nil {
		t.Fatal(e)
	}
	data, _ := io.ReadAll(part)
	if !bytes.Equal(data, jpeg) {
		t.Fatal("bad output frame")
	}
	cancel()
	for _, c := range clients {
		c.Body.Close()
	}
}
func TestRTSPBridgeRegistration(t *testing.T) {
	var received string
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" {
			t.Error("registration must be memory-only PATCH")
		}
		received = r.URL.Query().Get("src")
		w.WriteHeader(200)
	}))
	defer b.Close()
	a := testApp(t)
	a.bridge = b.URL
	c := Camera{Name: "X1C", Kind: "x1c", Host: "printer.local", Code: "a:@b"}
	if a.configureBridge(1, c) != nil {
		t.Fatal("registration failed")
	}
	u, e := url.Parse(strings.Split(received, "#")[0])
	if e != nil || u.Scheme != "rtspx" || u.Host != "printer.local:322" || u.Path != "/streaming/live/1" {
		t.Fatal("wrong X1C source")
	}
	p, _ := u.User.Password()
	if p != c.Code {
		t.Fatal("credential escaping failed")
	}
}
func TestFailedSavePreservesConfig(t *testing.T) {
	a := testApp(t)
	a.cameras[0] = Camera{Name: "Old", Kind: "p1p", Host: "printer", Code: "test"}
	os.WriteFile(a.configPath, []byte("old"), 0600)
	a.configPath = filepath.Join(a.configPath, "invalid")
	w := request(a, "PUT", "/api/cameras/0", `{"name":"New","kind":"p1p","host":"printer"}`)
	if w.Code != 500 || a.cameras[0].Name != "Old" {
		t.Fatal("failed save changed live config")
	}
}
func TestConfigProtectionRoundtrip(t *testing.T) {
	data, _ := json.Marshal(Camera{Name: "test", Code: "secret"})
	sealed, e := protect(data)
	if e != nil {
		t.Fatal(e)
	}
	plain, e := unprotect(sealed)
	if e != nil || !bytes.Equal(data, plain) {
		t.Fatal("protection roundtrip failed")
	}
}
