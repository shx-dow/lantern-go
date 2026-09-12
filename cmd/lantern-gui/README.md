# lantern-gui (Wails v3 spike)

Desktop shell for Lantern. Thin client over `lanternd`'s localhost v1 API
(`api/openapi.yaml`) — every bound method in `service.go` proxies to the
daemon. No transfer logic lives here, so the GUI cannot drift from the CLI
or SDKs, and no `openapi.yaml` change is needed.

## Status

Spike. What works now, with plain Go and no desktop deps:

- `go run ./cmd/lantern-gui` attaches to a running `lanternd`
  (`--daemon-url`, default `$LANTERND_URL` or `http://127.0.0.1:43782`)
  or starts an embedded daemon when none answers.
- `Client`/`GuiService` are covered against a fake `/v1` server
  (`client_test.go`, `service_test.go`).
- `frontend/dist/index.html` is the canonical web UI; Wails serves it,
  `lanternd /ui` embeds a byte-identical copy
  (enforced by `internal/daemon/web_sync_test.go`).

Not yet wired: window bindings in the page JS (it still `fetch`es `/v1`
with the token hook `window.__LANTERN__`), tray menu, file dialogs,
packaging. Those land once the shell below runs on a desktop host.

## Desktop build (Wails v3 beta)

Requires a desktop host — headless CI cannot build `-tags wails`:

- Linux: `libgtk-4-dev`, `libwebkitgtk-6.0-dev` (+ gcc, pkg-config);
  `wails3 setup` tells you the exact packages.
- macOS: Xcode command-line tools.
- Windows: WebView2 runtime.

Then:

```sh
go install github.com/wailsapp/wails/v3/cmd/wails3@latest
wails3 setup
cd cmd/lantern-gui
cp ../../frontend/dist/index.html frontend/dist/index.html
go get github.com/wailsapp/wails/v3@latest   # pin the beta
wails3 dev     # hot reload + generated bindings in frontend/bindings/
wails3 build   # packaged app
```

`main_wails.go` follows the beta API
(`application.New` + `Services` + `AssetFileServerFS`, see
<https://v3.wails.io/migration/v2-to-v3/>). Default `go build ./...`,
`go vet ./...`, and `go test ./...` skip that file by build tag, so the
main module stays cgo-free until you opt in.
