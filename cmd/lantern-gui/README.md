# lantern-gui (Fyne experiment)

Desktop shell for Lantern. Thin client over `lanternd`'s localhost v1 API
(`api/openapi.yaml`) — every method in `service.go` proxies to the
daemon. No transfer logic lives here, so the GUI cannot drift from the
CLI or SDKs, and no `openapi.yaml` change is needed.

## Status

Experiment. What works now:

- `go run ./cmd/lantern-gui` attaches to a running `lanternd`
  (`--daemon-url`, default `$LANTERND_URL` or `http://127.0.0.1:43782`)
  or starts an embedded daemon when none answers, then waits like
  `lanternd` console mode. Pure Go, no desktop deps.
- `Client`/`GuiService` are covered against a fake `/v1` server
  (`client_test.go`, `service_test.go`).
- `go run -tags fyne ./cmd/lantern-gui` opens a native Fyne window
  (Send / Receive / Transfers / Status tabs) on a desktop host.
  The window has real file dialogs and clipboard copy — no webview,
  no bundled browser, no generated bindings.

Not yet wired: tray menu, notifications, packaging. The daemon's
browser UI (`lanternd /ui`, served from `internal/daemon/web/`) is
unchanged and still works in any browser.

## How the window talks to the daemon

`main_fyne.go` attaches to a running `lanternd` (`$LANTERND_URL` or
`http://127.0.0.1:43782`) or boots an embedded in-process daemon on an
ephemeral loopback port, then drives the window from `GuiService`.
Native file dialogs return real paths, so shares go straight to
`POST /v1/shares` — no upload round-trip. Network calls run in
goroutines so the UI never freezes; the Transfers tab refreshes on
demand (Refresh button + after every action).

## Desktop build (Fyne v2)

Requires a desktop host — headless CI cannot build `-tags fyne`:

- Linux: `libgl1-mesa-dev`, `xorg-dev` (+ gcc, pkg-config);
  first build downloads the Fyne module (~100MB with dependencies).
- macOS: Xcode command-line tools.
- Windows: standard Go toolchain; build with
  `-ldflags -H=windowsgui` to hide the console window.

Then, from the repo root:

```sh
go run -tags fyne ./cmd/lantern-gui
```

`fyne package` for distributable bundles is not wired yet.

Default `go build ./...`, `go vet ./...`, and `go test ./...` skip the
`fyne`-tagged files by build tag, so the main module stays free of
graphics deps until you opt in.
