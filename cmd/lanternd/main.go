package main

import (
	"bytes"
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
	"github.com/shx-dow/lantern-go/internal/tray"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

type mdnsFilter struct{}

func (mdnsFilter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("[WARN] mdns:")) {
		return len(p), nil
	}
	return os.Stderr.Write(p)
}

func init() {
	log.SetOutput(mdnsFilter{})
}

func main() {
	var (
		addr       = flag.String("addr", "", "localhost HTTP listen address (default from config file or 127.0.0.1:43782)")
		p2pPort    = flag.Int("p2p-port", -1, "libp2p listen port (0 = random, -1 = config default)")
		dataDir    = flag.String("data-dir", "", "local advertisement directory (default OS temp)")
		lan        = flag.Bool("lan", true, "LAN-only mode: no public DHT bootstraps, mDNS plus local adverts")
		lanNeg     = flag.Bool("no-lan", false, "disable LAN-only mode (global DHT)")
		configPath = flag.String("config", "", "config file path (default $XDG_CONFIG_HOME/lantern/lanternd.json)")
		tokenFlag  = flag.String("token", "", "bearer token (default $LANTERND_TOKEN, else persisted in data dir)")
		defaultTTL = flag.Int64("default-ttl", -1, "default share lifetime in seconds (0 = no expiry, -1 = config default)")
		trayFlag   = flag.Bool("tray", true, "show a tray icon that opens the web UI (falls back to console when unsupported)")
		noTrayFlag = flag.Bool("no-tray", false, "disable the tray icon")
	)
	flag.Parse()

	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = daemon.DefaultConfigPath()
	}
	cfg, err := daemon.LoadConfigFile(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	listenAddr := *addr
	if listenAddr == "" {
		listenAddr = cfg.Addr
	}
	if listenAddr == "" {
		listenAddr = "127.0.0.1:43782"
	}
	port := *p2pPort
	if port < 0 {
		port = cfg.P2PPort
	}
	dir := *dataDir
	if dir == "" {
		dir = cfg.DataDir
	}
	lanOnly := *lan && !*lanNeg
	if cfg.LANOnly != nil && !flagNSet("lan") && !*lanNeg {
		lanOnly = *cfg.LANOnly
	}
	ttlSecs := *defaultTTL
	if ttlSecs < 0 {
		ttlSecs = cfg.DefaultTTLSeconds
	}
	var ttl time.Duration
	if ttlSecs > 0 {
		ttl = time.Duration(ttlSecs) * time.Second
	}

	bootstrap := []string{"none"}
	if !lanOnly {
		bootstrap = nil // nil keeps the default public bootstraps
	}

	ln, err := lantern.New(lantern.Config{Port: port, DataDir: dir, Bootstrap: bootstrap})
	if err != nil {
		log.Fatalf("init p2p: %v", err)
	}
	defer ln.Close()

	token, err := daemon.LoadOrCreateToken(dir, firstNonEmpty(*tokenFlag, os.Getenv("LANTERND_TOKEN")))
	if err != nil {
		log.Fatalf("token: %v", err)
	}

	d := daemon.New(ln)

	addrs := make([]string, 0)
	peerID := ""
	// Host stays exported for dialing until the transport seam is extracted.
	if node := ln.Node(); node != nil && node.Host != nil {
		peerID = node.Host.ID().String()
		for _, a := range node.Host.Addrs() {
			addrs = append(addrs, a.String())
		}
	}

	mux := http.NewServeMux()
	daemon.NewHandler(d, peerID, addrs, lanOnly, ttl, dir).Routes(mux)

	// The UI page is public (it holds no secrets; API calls carry the
	// token from browser storage). Everything under /v1/ stays authed.
	top := http.NewServeMux()
	page, contentType := daemon.UI()
	top.HandleFunc("GET /ui", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(page)
	})
	// Unqualified so it never conflicts with the authed /v1/ subtree.
	top.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/ui", http.StatusFound)
	})
	top.Handle("/v1/", daemon.RequireAuth(mux, token))

	srv := &http.Server{Addr: listenAddr, Handler: top}

	go func() {
		fmt.Printf("lanternd listening on http://%s (lan_only=%v)\n", listenAddr, lanOnly)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	uiURL := "http://" + listenAddr + "/ui"
	if *trayFlag && !*noTrayFlag && tray.Available() {
		// Tray owns the main thread (required on macOS/Windows); a
		// signal unblocks it so shutdown below still runs.
		go func() {
			<-sig
			fmt.Println("\nshutting down")
			tray.Quit()
		}()
		fmt.Printf("lantern tray running (%s)\n", uiURL)
		if err := tray.Run(tray.Config{Title: "Lantern", UIURL: uiURL}); err != nil {
			log.Printf("tray: %v (continuing in console mode)", err)
			<-sig
			fmt.Println("\nshutting down")
		}
	} else {
		<-sig
		fmt.Println("\nshutting down")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func flagNSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
