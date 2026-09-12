//go:build !wails

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shx-dow/lantern-go/internal/daemon"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

// Console fallback for lantern-gui: without -tags wails there is no desktop
// window, so this attaches to a running lanternd or starts an embedded one
// and waits like lanternd console mode.
func main() {
	daemonURL := flag.String("daemon-url", envOr("LANTERND_URL", defaultDaemonURL), "lanternd base URL to attach to")
	daemonToken := flag.String("daemon-token", os.Getenv("LANTERN_DAEMON_TOKEN"), "lanternd bearer token")
	embeddedAddr := flag.String("embedded-addr", "127.0.0.1:43782", "listen address when starting an embedded daemon")
	dataDir := flag.String("data-dir", "", "embedded daemon advertisement directory (default OS temp)")
	flag.Parse()

	svc := NewGuiService(*daemonURL, *daemonToken)
	if st, err := svc.GetStatus(); err == nil {
		fmt.Printf("lantern-gui attached to %s (peer %.12s)\n", *daemonURL, st.PeerID)
		waitForSignal()
		fmt.Println("shutting down")
		return
	} else {
		fmt.Printf("no daemon at %s (%v); starting embedded daemon\n", *daemonURL, err)
	}

	token, err := daemon.LoadOrCreateToken(*dataDir, *daemonToken)
	if err != nil {
		log.Fatalf("token: %v", err)
	}
	ln, err := lantern.New(lantern.Config{DataDir: *dataDir})
	if err != nil {
		log.Fatalf("init p2p: %v", err)
	}
	defer ln.Close()

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
	daemon.NewHandler(daemon.New(ln), peerID, addrs, true, 0, *dataDir).Routes(mux)

	// Same split as lanternd: the UI page is public, /v1/ stays authed.
	top := http.NewServeMux()
	page, contentType := daemon.UI()
	top.HandleFunc("GET /ui", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(page)
	})
	top.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/ui", http.StatusFound)
	})
	top.Handle("/v1/", daemon.RequireAuth(mux, token))

	srv := &http.Server{Addr: *embeddedAddr, Handler: top}
	go func() {
		fmt.Printf("lantern-gui embedded daemon on http://%s (open /ui in a browser)\n", *embeddedAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	waitForSignal()
	fmt.Println("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func waitForSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	signal.Stop(sig)
}
