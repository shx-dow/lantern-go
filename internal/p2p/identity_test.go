package p2p

import (
	"testing"
)

func TestIdentityPersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreatePrivKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreatePrivKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Equals(b) {
		t.Fatal("same dir returned different keys")
	}
	other, err := LoadOrCreatePrivKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a.Equals(other) {
		t.Fatal("different dirs returned identical keys")
	}
}

func TestNodesSharePeerIDWithSameKey(t *testing.T) {
	key, err := LoadOrCreatePrivKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewNode(0, []string{"none"}, key, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewNode(0, []string{"none"}, key, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.Host.ID() != b.Host.ID() {
		t.Fatalf("peer IDs differ: %s vs %s", a.Host.ID(), b.Host.ID())
	}
}

func TestNilKeyGivesEphemeralIdentity(t *testing.T) {
	a, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.Host.ID() == b.Host.ID() {
		t.Fatal("ephemeral nodes share a peer ID")
	}
}
