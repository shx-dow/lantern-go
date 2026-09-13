// Command lantern-discover is the LocalSend-route spike: it announces this
// machine on the LAN and lists nearby Lantern peers, no share codes.
//
// Run it in two terminals (or on two machines on the same network) and
// watch each side appear on the other:
//
//	go run ./cmd/lantern-discover
//	go run ./cmd/lantern-discover --alias "second box"
//
// Flags: --alias (default hostname), --type (desktop|mobile|headless|
// server), --port (default 43781), --interval (re-announce period).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/shx-dow/lantern-go/internal/discover"
)

func main() {
	alias := flag.String("alias", defaultAlias(), "device name shown to nearby peers")
	devType := flag.String("type", "headless", "device type: desktop, mobile, headless, server")
	port := flag.Int("port", discover.DefaultPort, "discovery UDP port")
	interval := flag.Duration("interval", discover.DefaultInterval, "re-announce period")
	flag.Parse()

	fp, err := discover.NewFingerprint()
	if err != nil {
		log.Fatalf("fingerprint: %v", err)
	}
	me := discover.Device{
		Alias:       strings.TrimSpace(*alias),
		Version:     discover.ProtoVersion,
		Type:        discover.DeviceType(strings.ToLower(strings.TrimSpace(*devType))),
		Fingerprint: fp,
		Port:        *port,
	}
	fmt.Printf("lantern-discover as %q (fp %.8s) on port %d\n", me.Alias, me.Fingerprint, me.Port)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := discover.NewRegistry()
	dst, err := discover.GroupAddr(*port)
	if err != nil {
		log.Fatalf("group: %v", err)
	}
	go discover.AnnounceLoop(ctx, dst, me, *interval)

	conn, err := discover.ListenPacketConn(*port, nil)
	if err != nil {
		log.Fatalf("listen (multicast blocked here?): %v", err)
	}
	defer conn.Close()
	go discover.ListenLoop(ctx, conn, me.Fingerprint, func(d discover.Device) {
		if reg.Seen(d) {
			fmt.Printf("+ nearby: %-20q type=%-8s fp=%.8s from %s\n", d.Alias, d.Type, d.Fingerprint, d.Addr)
		}
	})

	t := time.NewTicker(discover.DefaultTTL / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("bye")
			return
		case <-t.C:
			for _, d := range reg.Prune(discover.DefaultTTL) {
				fmt.Printf("- gone:   %-20q fp=%.8s\n", d.Alias, d.Fingerprint)
			}
		}
	}
}

func defaultAlias() string {
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return "lantern"
}
