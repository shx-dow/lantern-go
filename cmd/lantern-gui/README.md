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

Not yet wired: tray menu, file dialogs, packaging. Those land once the
shell below runs on a desktop host.

## How the window talks to the daemon

No token paste box in the window: `main_wails.go` attaches to a running
`lanternd` (`$LANTERND_URL` or `http://127.0.0.1:43782`) or boots an embedded
in-process daemon on an ephemeral loopback port, then injects
`window.__LANTERN__ = {baseURL, token}` into the served page. The page hides
its token card when injected, so Send (drop zone → `/v1/uploads` →
`/v1/shares`) and Receive (`/v1/fetches` + progress poll) work with zero
setup. `internal/daemon.CORS` allows the webview origin to call the daemon;
the bearer token is still required on every `/v1/` call.

Direct Go bindings are progressive enhancement: every page op tries the
generated module first and falls back to HTTP, so a missing/renamed binding
never breaks the window. To light up the direct path, from this directory:

```sh
wails3 generate bindings -d ./frontend/dist/bindings -f "-tags wails" -b
```

Flags matter: `-f "-tags wails"` makes the scanner see `main_wails.go`
(hidden behind the `wails` build tag, otherwise it reports 0 services),
and `-b` uses the bundled `/wails/runtime.js` instead of the npm package
(our page is a single static file with no `node_modules`).

Re-run after changing `service.go` (the page reads the `-d` copy under the
asset root; `wails3 dev` also regenerates into the default bindings dir on
Go changes, which the page does not read).

The page probes `./bindings/lantern/guiservice.js` then
`./bindings/main/guiservice.js`; if your generator emits a different path,
adjust the candidate list at the top of the page script. Check devtools for
`lantern: using Wails bindings` vs `lantern: using HTTP API`.

## Desktop build (Wails v3 beta)

Requires a desktop host — headless CI cannot build `-tags wails`:

- Linux: `libgtk-4-dev`, `libwebkitgtk-6.0-dev` (+ gcc, pkg-config);
  `wails3 setup` tells you the exact packages.
- macOS: Xcode command-line tools.
- Windows: WebView2 runtime.

Then, from `cmd/lantern-gui`:

```sh
wails3 dev     # copies the page in, opens the window, rebuilds on Go changes
```

No Node/npm needed: the page is a single static file, so there is no
frontend build step. `wails3 build` / `wails3 package` are not wired yet;
that needs the full Taskfile scaffold from `wails3 init` (a later step,
along with generated bindings, tray menu, and file dialogs).

`main_wails.go` follows the beta API
(`application.New` + `Services` + `AssetFileServerFS`, see
<https://v3.wails.io/migration/v2-to-v3/>). Default `go build ./...`,
`go vet ./...`, and `go test ./...` skip that file by build tag, so the
main module stays cgo-free until you opt in.
