//go:build fyne

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shx-dow/lantern-go/internal/daemon"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

// Fyne desktop shell for lantern-gui.
//
// This file is excluded from default builds. The window is run via:
//
//	go run -tags fyne ./cmd/lantern-gui
//
// The window attaches to a running lanternd when one answers, and boots an
// embedded in-process daemon otherwise, so it works with zero setup. Unlike
// the old webview shell there is nothing to inject and no bindings to
// generate: the window calls GuiService (a thin proxy over lanternd's v1
// API) directly from button handlers.
//
// NOTE: the -tags fyne build needs a desktop host with OS graphics support
// and cannot be verified in headless CI; default `go build ./...` and
// `go vet ./...` intentionally skip this file.
func main() {
	daemonURL := flag.String("daemon-url", envOr("LANTERND_URL", defaultDaemonURL), "lanternd base URL to attach to")
	daemonToken := flag.String("daemon-token", os.Getenv("LANTERN_DAEMON_TOKEN"), "lanternd bearer token")
	dataDir := flag.String("data-dir", "", "embedded daemon advertisement directory (default OS temp)")
	flag.Parse()

	svc, shutdown := resolveService(*daemonURL, *daemonToken, *dataDir)
	defer shutdown()
	runFyne(svc)
}

// resolveService attaches to a running lanternd when one answers at
// daemonURL, and boots an embedded in-process daemon on an ephemeral
// loopback port otherwise. Native dialogs return real paths, so the
// embedded daemon only serves /v1/ — no browser page, no CORS shim.
func resolveService(daemonURL, token, dataDir string) (*GuiService, func()) {
	svc := NewGuiService(daemonURL, token)
	if _, err := svc.GetStatus(); err == nil {
		fmt.Printf("lantern-gui attached to %s\n", svc.client.base)
		return svc, func() {}
	}
	fmt.Printf("no daemon at %s; starting embedded daemon\n", svc.client.base)

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
	srv := &http.Server{Handler: top}
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
	return NewGuiService(baseURL, realToken), shutdown
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
