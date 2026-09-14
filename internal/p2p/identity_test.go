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

func TestNodesSharePeerIDWithSameDir(t *testing.T) {
	dir := t.TempDir()
	a, err := NewNode(0, []string{"none"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	idA := a.Host.ID()
	a.Close()
	b, err := NewNode(0, []string{"none"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if idA != b.Host.ID() {
		t.Fatalf("peer IDs differ: %s vs %s", idA, b.Host.ID())
	}
}
