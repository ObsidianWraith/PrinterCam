# PrinterCam

Revision 001 · Windows 10/11 · 64-bit

A small local web app that displays up to four printer cameras in one fullscreen dashboard. Supports Bambu P1P/P1S, Bambu X1 Carbon, Klipper MJPEG webcams, JPEG snapshots, and other RTSP cameras. It runs independently of your slicer.

## Download and start

1. Open this repository's **Releases** page and download **PrinterCam_rev_001.zip** from the release assets.
2. Right-click the ZIP and choose **Extract All**. Open the extracted `PrinterCam_rev_001` folder.
3. Double-click **Start PrinterCam.cmd**. Your browser should open `http://127.0.0.1:8640`.
4. Click **Add camera** in a tile, choose the camera type, enter its details, and click **Save**.
5. Repeat for your other cameras, then click **Full screen**.

Keep `PrinterCam.exe` and the `bin` folder together. The release includes the go2rtc camera helper. Docker, Python, Go, and Node are unnecessary for running the download. Chrome or Edge is recommended.

The executable is unsigned. Only run downloads from a source you trust. The GitHub **Source code** archives contain code to build, while the named PrinterCam ZIP contains the ready-to-run app.

For a desktop shortcut, put the extracted folder somewhere permanent and run **Create Desktop Shortcut.cmd** once.

## Connect a Bambu P1P or P1S

1. Turn on the printer and connect the PC to the same local network.
2. On the printer, find its **IP address** and **LAN access code** in its network settings. P1-series screens generally show these under **Settings → WLAN**.
3. In PrinterCam, click **Add camera** and select **Bambu P1P / P1S**.
4. Enter a display name, the printer IP address, and the LAN access code. Click **Save**.

The camera must be installed and enabled. P1P video has a naturally low frame rate. PrinterCam reads its native TLS/JPEG camera feed on port 6000.

## Connect a Bambu X1 Carbon

1. On the printer touchscreen, find its IP address and LAN access code.
2. Enable **LAN Mode Liveview** or **LAN Only Liveview**. The exact wording and location vary with firmware. Look under **Settings → LAN Only**, or the **General / Network** settings pages for the LAN/live-view option.
3. In PrinterCam, select **Bambu X1 Carbon**, enter a name, IP address, and LAN access code, then click **Save**.
4. If you enabled live view after saving the camera, click **Reconnect** on its tile.

The live-view toggle permits direct camera streaming while the printer can remain connected to Bambu Cloud. Full LAN-only mode is not required for this app's camera connection. A working camera in Bambu Studio alone does not prove direct local streaming is enabled. In testing, enabling the live-view setting made the local feed available immediately.

PrinterCam uses the encrypted RTSP camera service on port 322 at `/streaming/live/1`, with username `bblp` and the LAN access code as its password. The built-in X1C option fills in these details for you. For a custom address, use **Other RTSP camera**.

## Connect a Klipper / Mainsail / Fluidd webcam

1. Open your printer's Mainsail or Fluidd page and go to its webcam settings.
2. Copy the webcam's **MJPEG stream URL** or **JPEG snapshot URL**.
3. In PrinterCam, select **Klipper / MJPEG camera**, give it a name, and paste the full camera URL.
4. Click **Save**.

For example, replace `printer.local` with your printer's address:

```text
http://printer.local/webcam/?action=stream
```

For a snapshot camera:

```text
http://printer.local/webcam/?action=snapshot
```

A Mainsail dashboard address or `/webcam/webrtc` address will not work in this field. Camera-streamer installations may offer the MJPEG address above even when Mainsail uses WebRTC. Try the URL in your browser first to confirm it returns your camera picture.

### Optional Moonraker discovery

Expand **Find a camera through Moonraker**, enter its HTTP base address, such as `http://printer.local:7125`, and select a discovered camera. Review the resulting URL before saving. A relative webcam path may need the Mainsail/Fluidd web port instead of Moonraker's port. If discovery fails or requires authentication, enter the camera URL manually.

## Fourth camera and other RTSP sources

Any supported camera type can occupy any tile. Choose **Other RTSP camera** to enter a complete RTSP URL. H.264 is recommended for browser compatibility. H.265 support depends on the browser and hardware. The app repackages the stream without transcoding it.

## Viewing controls

| Control | Action |
| --- | --- |
| **Full screen** or **F** | Fill the monitor with the dashboard |
| Double-click a feed, its focus button, or **1–4** | Enlarge one camera |
| **Esc** | Return to the grid / leave browser fullscreen |
| **Fit / Fill** | Show the full image or crop to fill its tile |
| **Reconnect** | Restart that viewing connection |
| **Pause all / Resume all** | Disconnect or reconnect the camera views |
| Camera gear button | Edit or remove that camera |
| **Quit** | Stop PrinterCam and its helper |

Closing the browser leaves the local app running. Launch the app again to reopen it. Camera details survive restarts. Leave a saved code or URL blank when editing to keep its existing value.

## Troubleshooting

| Problem | What to check |
| --- | --- |
| Browser does not open | Open `http://127.0.0.1:8640` manually after starting the app |
| App immediately closes | Run `PrinterCam.exe` from a terminal to see the error. Verify `bin/go2rtc.exe` was extracted |
| Bambu camera keeps reconnecting | Check the current printer IP and access code. Close other camera viewers temporarily to test available connections |
| X1C works in Studio but fails here | Enable the printer's LAN live-view toggle, then reconnect |
| Klipper camera fails | Test the full MJPEG/snapshot URL in a browser. Check its hostname, path, and port |
| Camera IP changed | Edit its saved IP, or configure a DHCP reservation in your router |
| Slow P1P image updates | The built-in camera limits its frame rate |
| Fullscreen button fails | Press **F11** in your desktop browser |

## Local settings and privacy

The app and bridge listen on loopback addresses on the PC. Settings are stored in `%APPDATA%\PrinterCam\cameras.dat`. Windows DPAPI encrypts them for the current Windows user. The dashboard does not return saved access codes or source URLs to the browser. Camera sources are registered with go2rtc in memory.

There are no included camera settings, accounts, telemetry, or cloud uploads. Camera addresses and credentials stay on the computer running PrinterCam. Bambu cameras use self-signed certificates, so their camera connections intentionally skip certificate verification. Other programs running under the same Windows account are outside this protection boundary.

Do not attach your settings files or unredacted camera URLs to public issues. If resetting the app, quit it and delete its settings file, then restart and add your cameras again. An encrypted settings file cannot be moved to another Windows account as a portable backup.

## Verification and limits

- P1P and Klipper MJPEG feeds were verified through the Windows app on real printers.
- An X1C feed was verified through PrinterCam as fragmented MP4 after local live view was enabled.
- Windows configuration saving and desktop-shortcut creation were exercised.
- Automated tests cover configuration, credential redaction, origin checks, P1P framing and cancellation, MJPEG/snapshot handling, shared feeds, and RTSP registration.
- Simulated integration checks exercised four simultaneous feeds and Moonraker discovery.

The app supports four slots. Phone/LAN hosting, authenticated Moonraker discovery, and direct WebRTC/HLS input are not implemented. Browser fullscreen and every codec/firmware combination have not been comprehensively tested. Linux/macOS builds store user-only settings without Windows DPAPI encryption.

## Build from source

Install Go 1.25 or newer, clone this repository, then run:

```sh
go test ./...
go build -trimpath -ldflags="-s -w" -o PrinterCam.exe .
```

On supported systems with a C compiler, run `go test -race ./...` for race detection. To cross-compile for 64-bit Windows from Linux/macOS:

```sh
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o PrinterCam.exe .
```

Download the official **go2rtc v1.9.15 Windows amd64** release, extract it, and put its executable at `bin/go2rtc.exe` beside PrinterCam. The helper executable used in this release has SHA-256:

```text
994aa42768d2d6e1bbcf561c85bf9c46cafa705a90a13537cd38be2faee287de
```

Source is also included in the Windows download under `source/`. Release downloads include `SHA256SUMS.txt` for their files.

## License and references

PrinterCam is MIT licensed. The bundled unmodified go2rtc helper is MIT licensed, and the Go runtime has its BSD license. Notices are in `licenses/`.

- [go2rtc](https://github.com/AlexxIT/go2rtc)
- [Bambu camera protocol documentation](https://github.com/Doridian/OpenBambuAPI/blob/main/video.md)
- [Bambu LAN settings documentation](https://wiki.bambulab.com/en/knowledge-sharing/enable-lan-mode)
