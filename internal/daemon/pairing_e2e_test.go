package daemon

// End-to-end pairing over the real transport and the real local API: two
// lantern nodes, two daemons, one invitation string. The point is that neither
// side has to know a peer ID in advance, and that both sides end up paired --
// the one-directional `trust add` cannot do that in one step.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/pkg/lantern"
	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

type pairingDevice struct {
	daemon *Daemon
	store  *TrustStore
	ln     *lantern.Lantern
	node   string
	api    *httptest.Server
	client *lanternclient.Client
}

func newPairingDevice(t *testing.T, base, dir, name string) *pairingDevice {
	t.Helper()
	ln, err := lantern.New(lantern.Config{
		Port:      0,
		DataDir:   filepath.Join(base, dir),
		Bootstrap: []string{"none"},
		NoMDNS:    true,
		// The devices dial each other over loopback. The swarm's own limit for
		// a private address is 5 seconds, which a security handshake under
		// -race on a loaded runner can outlast; a longer caller context does
		// not raise it.
		DialTimeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	store, err := NewTrustStore(filepath.Join(base, dir+"-state"))
	if err != nil {
		t.Fatal(err)
	}
	d := New(ln)
	d.Trust = store
	d.PairingName = name

	ln.Node().SetPairAcceptor(d.AcceptPairing)
	ln.Node().RegisterPairHandler()

	h := NewHandler(d, ln.Node().Host.ID().String(), nil, true, 0, "").WithDeviceName(name)
	mux := http.NewServeMux()
	h.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &pairingDevice{
		daemon: d,
		store:  store,
		ln:     ln,
		node:   ln.Node().Host.ID().String(),
		api:    srv,
		client: lanternclient.New(srv.URL, "test-token"),
	}
}

// connect stands in for the connection two devices already have when they are
// on one LAN (mDNS) or were started with --peer. Without it these fixtures
// have no DHT peer at all, and an address-free invitation has no route to the
// inviter to test.
func connect(t *testing.T, from, to *pairingDevice) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info := peer.AddrInfo{ID: to.ln.Node().Host.ID(), Addrs: to.ln.Node().Host.Addrs()}
	if err := from.ln.Node().Host.Connect(ctx, info); err != nil {
		t.Fatalf("connect the two devices: %v", err)
	}
}

func TestPairingOnePastePairsBothDevices(t *testing.T) {
	base := t.TempDir()
	laptop := newPairingDevice(t, base, "laptop", "laptop")
	desktop := newPairingDevice(t, base, "desktop", "desktop")
	connect(t, desktop, laptop)

	inv, err := laptop.client.PairInvite(0)
	if err != nil {
		t.Fatalf("mint an invitation: %v", err)
	}
	if !strings.HasPrefix(inv.Invitation, "lantern-pair1.") {
		t.Errorf("invitation = %q, want the pasteable prefix", inv.Invitation)
	}
	if len(inv.Invitation) > 100 {
		t.Errorf("invitation is %d characters, want the address-free shape", len(inv.Invitation))
	}
	if strings.Contains(inv.Invitation, "ip4/") || strings.Contains(inv.Invitation, "ip6/") {
		t.Errorf("invitation leaks a dial address: %q", inv.Invitation)
	}

	res, err := desktop.client.PairRedeem(inv.Invitation)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if res.PeerID != laptop.node {
		t.Errorf("redeemer paired with %q, want %q", res.PeerID, laptop.node)
	}
	if res.DeviceName != "laptop" || res.Tier != string(TierRead) {
		t.Errorf("redeem result = %+v, want laptop at read", res)
	}

	// Both stores now answer, which is what "mutual" means.
	onLaptop, ok := laptop.store.Get(desktop.node)
	if !ok {
		t.Fatal("the laptop did not record the desktop")
	}
	if onLaptop.Alias != "desktop" || onLaptop.Tier != TierRead {
		t.Errorf("laptop recorded %+v, want desktop at read", onLaptop)
	}
	onDesktop, ok := desktop.store.Get(laptop.node)
	if !ok {
		t.Fatal("the desktop did not record the laptop")
	}
	if onDesktop.Alias != "laptop" || onDesktop.Tier != TierRead {
		t.Errorf("desktop recorded %+v, want laptop at read", onDesktop)
	}
	if len(onDesktop.Addrs) == 0 {
		t.Error("the desktop did not keep the laptop's address from the invitation")
	}

	// The same string cannot pair a third device, or re-pair this one.
	if _, err := desktop.client.PairRedeem(inv.Invitation); err == nil {
		t.Fatal("the invitation was redeemable twice")
	}
}

// A redeemer that already has read-write on the inviter keeps it. The
// invitation grants read; it cannot narrow what is already there.
func TestPairingDoesNotDemoteAnExistingGrant(t *testing.T) {
	base := t.TempDir()
	laptop := newPairingDevice(t, base, "laptop", "laptop")
	desktop := newPairingDevice(t, base, "desktop", "desktop")
	connect(t, desktop, laptop)

	if _, err := laptop.store.Add(TrustSpec{
		PeerID: desktop.node,
		Alias:  "workstation",
		Tier:   string(TierReadWrite),
	}); err != nil {
		t.Fatal(err)
	}

	inv, err := laptop.client.PairInvite(0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := desktop.client.PairRedeem(inv.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != string(TierReadWrite) {
		t.Errorf("reported tier = %q, want the existing read-write", res.Tier)
	}
	entry, _ := laptop.store.Get(desktop.node)
	if entry.Tier != TierReadWrite || entry.Alias != "workstation" {
		t.Errorf("existing pairing changed: %+v", entry)
	}
}
