package daemon

import (
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func pairingDaemon(t *testing.T) *Daemon {
	t.Helper()
	store, err := NewTrustStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Daemon{Trust: store, PairingName: "laptop"}
}

// Redeeming an invitation is a pairing, and a pairing confers reading. It must
// never confer writing on its own, exactly like `trust add`.
func TestAcceptPairingGrantsReadAndRecordsTheName(t *testing.T) {
	d := pairingDaemon(t)
	remote := peer.ID("12D3KooWTheDesktop")

	name, tier, err := d.AcceptPairing(remote, "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if name != "laptop" {
		t.Errorf("this device answered as %q, want laptop", name)
	}
	if tier != string(TierRead) {
		t.Errorf("tier = %q, want %q", tier, TierRead)
	}
	entry, ok := d.Trust.Get(remote.String())
	if !ok {
		t.Fatal("the redeemer was not recorded")
	}
	if entry.Alias != "desktop" {
		t.Errorf("alias = %q, want the name the redeemer sent", entry.Alias)
	}
	if entry.Tier != TierRead {
		t.Errorf("stored tier = %q, want %q", entry.Tier, TierRead)
	}
}

// A second invitation redeemed by a device that already has a grant must not
// narrow it. Someone who raised a laptop to read-write keeps it.
func TestAcceptPairingDoesNotDemoteAnExistingPairing(t *testing.T) {
	d := pairingDaemon(t)
	remote := peer.ID("12D3KooWTheDesktop")
	if _, err := d.Trust.Add(TrustSpec{
		PeerID:        remote.String(),
		Alias:         "work-laptop",
		Tier:          string(TierReadWrite),
		WritableRoots: []string{"/srv/inbox"},
		DeviceRoots:   []string{"/srv/inbox"},
	}); err != nil {
		t.Fatal(err)
	}

	_, tier, err := d.AcceptPairing(remote, "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if tier != string(TierReadWrite) {
		t.Errorf("tier = %q, want the existing read-write", tier)
	}
	entry, _ := d.Trust.Get(remote.String())
	if entry.Alias != "work-laptop" {
		t.Errorf("alias = %q, want the operator's name kept", entry.Alias)
	}
	if entry.Tier != TierReadWrite {
		t.Errorf("stored tier = %q, want read-write kept", entry.Tier)
	}
	if len(entry.WritableRoots) != 1 || entry.WritableRoots[0] != "/srv/inbox" {
		t.Errorf("writable roots = %v, want the existing subset kept", entry.WritableRoots)
	}
}

func TestAcceptPairingNeedsAPairingStore(t *testing.T) {
	d := &Daemon{PairingName: "laptop"}
	if _, _, err := d.AcceptPairing(peer.ID("12D3KooWTheDesktop"), "desktop"); err == nil {
		t.Fatal("pairing without a trust store was accepted")
	}
}

// The alias comes from another device, so it is display data, not a command.
func TestPairingAliasIsOnePrintableLine(t *testing.T) {
	pid := "12D3KooWTheDesktop"
	for _, tc := range []struct {
		name, deviceName, want string
	}{
		{"plain", "desktop", "desktop"},
		{"trimmed", "  desktop  ", "desktop"},
		{"control characters removed", "desk\ntop\x00", "desktop"},
		{"empty falls back to the peer ID", "  ", shortPeer(pid)},
		{"too long is cut", strings.Repeat("x", 90), strings.Repeat("x", 64)},
	} {
		if got := pairingAlias(tc.deviceName, pid); got != tc.want {
			t.Errorf("%s: pairingAlias(%q) = %q, want %q", tc.name, tc.deviceName, got, tc.want)
		}
	}
}
