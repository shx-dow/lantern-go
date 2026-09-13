package discover

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestMessageRoundTrip(t *testing.T) {
	raw, err := json.Marshal(message{
		App: "lantern", Alias: "lab-pc", Version: ProtoVersion,
		Model: "Windows", Type: TypeDesktop, Fingerprint: "ab12",
		Port: DefaultPort, Announce: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var m message
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.App != "lantern" || m.Alias != "lab-pc" || m.Fingerprint != "ab12" {
		t.Fatalf("bad round trip: %+v", m)
	}
}

func TestRegistrySeenPrune(t *testing.T) {
	r := NewRegistry()
	a := Device{Alias: "a", Fingerprint: "fp-a"}
	b := Device{Alias: "b", Fingerprint: "fp-b"}
	if !r.Seen(a) {
		t.Fatal("first sighting should be new")
	}
	if r.Seen(a) {
		t.Fatal("second sighting should not be new")
	}
	r.Seen(b)
	if got := len(r.Active(time.Minute)); got != 2 {
		t.Fatalf("want 2 active, got %d", got)
	}
	time.Sleep(60 * time.Millisecond)
	if got := len(r.Prune(30 * time.Millisecond)); got != 2 {
		t.Fatalf("want 2 pruned, got %d", got)
	}
	if got := len(r.Active(time.Minute)); got != 0 {
		t.Fatalf("want 0 active after prune, got %d", got)
	}
}

// TestAnnounceListenLoopback is deterministic (no multicast): announce to a
// plain loopback socket and expect ListenLoop to deliver it.
func TestAnnounceListenLoopback(t *testing.T) {
	lc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	defer lc.Close()
	conn, ok := lc.(*net.UDPConn)
	if !ok {
		t.Skip("loopback socket is not UDP")
	}
	dst := conn.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make(chan Device, 4)
	go ListenLoop(ctx, conn, "self-fp", func(d Device) { got <- d })

	dev := Device{Alias: "loop", Version: ProtoVersion, Fingerprint: "peer-fp", Port: 1}
	if err := AnnounceOnce(dst, dev); err != nil {
		t.Fatalf("announce: %v", err)
	}
	select {
	case d := <-got:
		if d.Alias != "loop" || d.Fingerprint != "peer-fp" {
			t.Fatalf("bad device: %+v", d)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for loopback announce")
	}
}

// TestSelfAndForeignTrafficIgnored covers self-fingerprint drops and the
// shared-group case where real LocalSend packets arrive.
func TestSelfAndForeignTrafficIgnored(t *testing.T) {
	lc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	defer lc.Close()
	conn := lc.(*net.UDPConn)
	dst := conn.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got := make(chan Device, 4)
	go ListenLoop(ctx, conn, "self-fp", func(d Device) { got <- d })

	write := func(s string) {
		c, err := net.DialUDP("udp", nil, dst)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := c.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	// Self announce must be dropped.
	self, _ := json.Marshal(message{App: "lantern", Alias: "me", Fingerprint: "self-fp"})
	write(string(self))
	// A real LocalSend announce sharing the group must be ignored.
	write(`{"alias":"Nice Orange","version":"2.0","fingerprint":"ls-fp","announce":true}`)
	// Garbage must be ignored.
	write("not json at all")

	select {
	case d := <-got:
		t.Fatalf("expected silence, got %+v", d)
	case <-ctx.Done():
		// Correct: nothing deliverable arrived.
	}
}

// TestMulticastLoopback exercises the real group path. It skips (never
// fails) when the environment cannot do multicast, so headless CI stays
// green while desktop hosts prove the path for real.
func TestMulticastLoopback(t *testing.T) {
	conn, err := ListenPacketConn(DefaultPort, nil)
	if err != nil {
		t.Skipf("no multicast socket: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	got := make(chan Device, 4)
	go ListenLoop(ctx, conn, "self-fp", func(d Device) { got <- d })

	dst, err := GroupAddr(DefaultPort)
	if err != nil {
		t.Skipf("no group addr: %v", err)
	}
	dev := Device{Alias: "mc", Version: ProtoVersion, Fingerprint: "mc-fp", Port: DefaultPort}
	for i := 0; i < 3; i++ {
		if err := AnnounceOnce(dst, dev); err != nil {
			t.Skipf("multicast send failed: %v", err)
		}
		select {
		case d := <-got:
			if d.Fingerprint != "mc-fp" {
				t.Fatalf("bad device: %+v", d)
			}
			return
		case <-time.After(2 * time.Second):
		}
	}
	t.Skip("multicast announced but nothing received; environment blocks it")
}
