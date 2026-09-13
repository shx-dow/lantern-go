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
// server), --port (default 43781), --interval (re-announce period),
// --unicast ip:port (also announce straight at one peer, bypassing
// multicast — the diagnostic that separates "multicast blocked" from
// "UDP blocked"), --scan (probe the local subnets once at startup via
// the HTTP fallback), --scan-every (repeat the scan periodically).
//
// The instance always serves the HTTP presence endpoint on --port (TCP,
// same port as UDP discovery — different protocol, like LocalSend), so
// scanned peers can find it back. A second copy on the same machine
// can't rebind that TCP port; it warns and carries on with multicast.
//
// Windows note: if two instances on the same port never see each other,
// allow inbound UDP *and* TCP on the discovery port (or the test binary)
// in Windows Defender Firewall; both are dropped silently otherwise.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
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
	unicast := flag.String("unicast", "", "also announce directly to ip:port (bypasses multicast)")
	scan := flag.Bool("scan", false, "probe local subnets once at startup via the HTTP fallback")
	scanEvery := flag.Duration("scan-every", 0, "repeat the subnet scan periodically (implies scan)")
	scanPort := flag.Int("scan-port", 0, "subnet-scan target TCP port (default: --port)")
	flag.Parse()
	if *scanPort == 0 {
		*scanPort = *port
	}

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
	note := func(source string, d discover.Device) {
		if reg.Seen(d) {
			fmt.Printf("+ nearby: %-20q type=%-8s fp=%.8s from %s (%s)\n",
				d.Alias, d.Type, d.Fingerprint, d.Addr, source)
		}
	}

	// Always serve presence so scanners can find us back. A second copy
	// on the same machine loses the TCP bind; multicast still works.
	go func() {
		if err := discover.ServeRegister(ctx,
			fmt.Sprintf(":%d", *port), me,
			func(d discover.Device) { note("scan", d) }); err != nil {
			fmt.Printf("presence HTTP unavailable (another copy running?): %v\n", err)
		}
	}()

	dst, err := discover.GroupAddr(*port)
	if err != nil {
		log.Fatalf("group: %v", err)
	}
	go discover.AnnounceLoop(ctx, dst, me, *interval)
	if u := strings.TrimSpace(*unicast); u != "" {
		udst, err := net.ResolveUDPAddr("udp", u)
		if err != nil {
			log.Fatalf("unicast: %v", err)
		}
		fmt.Printf("also announcing directly to %s\n", udst)
		go discover.AnnounceLoop(ctx, udst, me, *interval)
	}

	conn, err := discover.ListenPacketConn(*port, nil)
	if err != nil {
		// Never fatal: the HTTP scan path still works without multicast,
		// which is exactly the Windows story. Multicast just stays deaf.
		fmt.Printf("multicast unavailable, continuing deaf (scan/unicast only): %v\n", err)
	} else {
		defer conn.Close()
		go discover.ListenLoop(ctx, conn, me.Fingerprint, func(d discover.Device) {
			note("multicast", d)
		})
	}

	runScan := func() {
		ips := discover.LocalSubnetIPs()
		fmt.Printf("scanning %d subnet addresses on port %d…\n", len(ips), *scanPort)
		for _, d := range discover.Scan(ctx, me, *scanPort, ips, 400*time.Millisecond, 64) {
			note("scan", d)
		}
	}
	if *scan || *scanEvery > 0 {
		go runScan()
	}
	var scanTicker *time.Ticker
	if *scanEvery > 0 {
		scanTicker = time.NewTicker(*scanEvery)
		defer scanTicker.Stop()
	}

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
		case <-scanTick(scanTicker):
			go runScan()
		}
	}
}

// scanTick returns a nil channel when disabled, which never fires.
func scanTick(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

func defaultAlias() string {
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return "lantern"
}
