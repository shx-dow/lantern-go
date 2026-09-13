package discover

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestRegisterRoundTrip(t *testing.T) {
	self := Device{Alias: "srv", Fingerprint: "srv-fp", Port: DefaultPort}
	var peer Device
	var called bool
	srv := httptest.NewServer(NewRegisterHandler(self, func(d Device) {
		called = true
		peer = d
	}))
	defer srv.Close()

	raw, _ := json.Marshal(message{App: "lantern", Alias: "cli", Fingerprint: "cli-fp", Port: 1})
	resp, err := http.Post(srv.URL+registerPath, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var m message
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.Fingerprint != "srv-fp" || m.App != "lantern" {
		t.Fatalf("bad reply: %+v", m)
	}
	if !called || peer.Fingerprint != "cli-fp" {
		t.Fatalf("prober not recorded: %+v called=%v", peer, called)
	}
}

func TestRegisterIgnoresForeignApp(t *testing.T) {
	self := Device{Alias: "srv", Fingerprint: "srv-fp"}
	called := false
	srv := httptest.NewServer(NewRegisterHandler(self, func(Device) { called = true }))
	defer srv.Close()

	raw, _ := json.Marshal(message{App: "something-else", Alias: "x", Fingerprint: "x-fp"})
	resp, err := http.Post(srv.URL+registerPath, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if called {
		t.Fatal("foreign prober must not be recorded")
	}
}

func TestScanLoopback(t *testing.T) {
	self := Device{Alias: "me", Fingerprint: "me-fp"}
	srv := httptest.NewServer(NewRegisterHandler(
		Device{Alias: "found", Fingerprint: "found-fp"}, func(Device) {}))
	defer srv.Close()

	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := Scan(ctx, self, port, []net.IP{net.ParseIP("127.0.0.1")}, 2*time.Second, 4)
	if len(got) != 1 || got[0].Fingerprint != "found-fp" {
		t.Fatalf("want the loopback server, got %+v", got)
	}

	// Closed port: skipped fast, no error, no result.
	got = Scan(ctx, self, port+1, []net.IP{net.ParseIP("127.0.0.1")}, 500*time.Millisecond, 4)
	if len(got) != 0 {
		t.Fatalf("want nothing on closed port, got %+v", got)
	}
}

func TestLocalSubnetIPsSane(t *testing.T) {
	for _, ip := range LocalSubnetIPs() {
		if ip.IsLoopback() || !ip.IsGlobalUnicast() || ip.To4() == nil {
			t.Fatalf("bad candidate %v", ip)
		}
	}
}
