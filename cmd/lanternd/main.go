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
		addr    = flag.String("addr", "127.0.0.1:43782", "localhost HTTP listen address (never expose publicly in v1)")
		p2pPort = flag.Int("p2p-port", 0, "libp2p listen port (0 = random)")
		dataDir = flag.String("data-dir", "", "local advertisement directory (default OS temp)")
		lan     = flag.Bool("lan", true, "LAN-only mode: no public DHT bootstraps, mDNS plus local adverts")
	)
	flag.Parse()

	bootstrap := []string{"none"}
	if !*lan {
		bootstrap = nil // nil keeps the default public bootstraps
	}

	ln, err := lantern.New(lantern.Config{Port: *p2pPort, DataDir: *dataDir, Bootstrap: bootstrap})
	if err != nil {
		log.Fatalf("init p2p: %v", err)
	}
	defer ln.Close()

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
	daemon.NewHandler(d, peerID, addrs, *lan).Routes(mux)

	srv := &http.Server{Addr: *addr, Handler: mux}

	go func() {
		fmt.Printf("lanternd listening on http://%s (lan_only=%v)\n", *addr, *lan)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("\nshutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
