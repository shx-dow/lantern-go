//go:build wails

package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shx-dow/lantern-go/internal/daemon"
	"github.com/shx-dow/lantern-go/pkg/lantern"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Wails v3 desktop shell (beta API per https://v3.wails.io/migration/v2-to-v3/).
//
// This file is excluded from default builds. The window is run via:
//
//	wails3 dev          # copies frontend/dist in and hot-reloads Go changes
//
// The window attaches to a running lanternd when one answers, and boots an
// embedded in-process daemon otherwise, so it works with zero setup: the
// page gets {baseURL, token} injected into window.__LANTERN__ and never asks
// the user for a token. Direct Go bindings for GuiService are generated via:
//
//	wails3 generate bindings -d ./frontend/dist/bindings   # from cmd/lantern-gui
//
// and picked up by the page automatically (it falls back to HTTP otherwise).
//
// A manual window build needs the page copied in first (see
// frontend/dist/SYNC.txt) plus the v3 module pinned in go.mod:
//
//	go get github.com/wailsapp/wails/v3@latest
//	go run -tags wails .
//
// Full desktop flow (packaging) needs the v3 CLI:
//
//	go install github.com/wailsapp/wails/v3/cmd/wails3@latest
//	wails3 setup        # checks OS webview deps, see README.md
//
// NOTE: the -tags wails build needs a desktop host with OS webview support
// and cannot be verified in headless CI; default `go build ./...` and
// `go vet ./...` intentionally skip this file.

//go:embed frontend/dist
var assets embed.FS

// injectPlaceholder marks where the daemon connection is injected into the
// served page. It must appear in frontend/dist/index.html (kept verbatim in
// the browser copy, where it is a harmless comment).
const injectPlaceholder = "<!--LANTERN_INJECT-->"

func main() {
	svc, baseURL, token, shutdown := resolveService()
	defer shutdown()
	runWails(svc, baseURL, token)
}

// resolveService attaches to a running lanternd when one answers at
// LANTERND_URL (or the default), and boots an embedded in-process daemon on
// an ephemeral loopback port otherwise. It returns the bound service, the
// base URL and token the page must use, and a shutdown func for the embedded
// case (no-op when attached).
func resolveService() (*GuiService, string, string, func()) {
	url := envOr("LANTERND_URL", defaultDaemonURL)
	token := strings.TrimSpace(os.Getenv("LANTERN_DAEMON_TOKEN"))

	svc := NewGuiService(url, token)
	if _, err := svc.GetStatus(); err == nil {
		fmt.Printf("lantern-gui attached to %s\n", svc.client.base)
		return svc, svc.client.base, token, func() {}
	}
	fmt.Printf("no daemon at %s; starting embedded daemon\n", svc.client.base)

	dataDir := strings.TrimSpace(os.Getenv("LANTERN_DATA_DIR"))
	realToken, err := daemon.LoadOrCreateToken(dataDir, token)
	if err != nil {
		log.Fatalf("token: %v", err)
	}
	ln, err := lantern.New(lantern.Config{DataDir: dataDir})
	if err != nil {
		log.Fatalf("init p2p: %v", err)
	}

	peerID := ""
	var addrs []string
	// Host stays exported for dialing until the transport seam is extracted.
	if node := ln.Node(); node != nil && node.Host != nil {
		peerID = node.Host.ID().String()
		for _, a := range node.Host.Addrs() {
			addrs = append(addrs, a.String())
		}
	}

	mux := http.NewServeMux()
	daemon.NewHandler(daemon.New(ln), peerID, addrs, true, 0, dataDir).Routes(mux)
	top := http.NewServeMux()
	top.Handle("/v1/", daemon.RequireAuth(mux, realToken))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		ln.Close()
		log.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: daemon.CORS(top)}
	go func() {
		if err := srv.Serve(lis); err != nil && err != http.ErrServerClosed {
			log.Printf("embedded daemon: %v", err)
		}
	}()
	baseURL := "http://" + lis.Addr().String()
	fmt.Printf("lantern-gui embedded daemon on %s\n", baseURL)

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
		ln.Close()
	}
	return NewGuiService(baseURL, realToken), baseURL, realToken, shutdown
}

// injectScript renders the page bootstrap: the daemon connection the Go side
// already knows. encoding/json HTML-escapes <, >, and &, so the token cannot
// break out of the script element.
func injectScript(baseURL, token string) string {
	raw, _ := json.Marshal(map[string]string{"baseURL": baseURL, "token": token})
	return "<script>window.__LANTERN__=" + string(raw) + ";</script>"
}

// assetHandler serves the embedded page with the daemon connection injected,
// and delegates everything else (including generated bindings under
// frontend/dist/bindings) to the Wails asset server.
func assetHandler(baseURL, token string) http.Handler {
	next := application.AssetFileServerFS(assets)
	snippet := injectScript(baseURL, token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			if page, err := assets.ReadFile("frontend/dist/index.html"); err == nil {
				out := strings.Replace(string(page), injectPlaceholder, snippet, 1)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.WriteString(w, out)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func runWails(svc *GuiService, baseURL, token string) {
	app := application.New(application.Options{
		Name:        "Lantern",
		Description: "Peer-to-peer file transfer",
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: assetHandler(baseURL, token),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Lantern",
		Width:  1024,
		Height: 768,
	})
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
