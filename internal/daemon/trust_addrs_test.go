package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func newPairedStore(t *testing.T, peerID string) (*TrustStore, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(peerID, "laptop"); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

const testPeer = "12D3KooWTestPeerID00000000000000000000000000000000"

func TestUpdateAddrsStoresAndPersists(t *testing.T) {
	s, dir := newPairedStore(t, testPeer)
	addrs := []string{"/ip4/192.168.1.5/tcp/4001", "/ip4/192.168.1.5/udp/4001/quic-v1"}
	if err := s.UpdateAddrs(testPeer, addrs); err != nil {
		t.Fatal(err)
	}

	// A fresh store must see them, or the cache dies with the process.
	reloaded, err := NewTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := reloaded.byID[testPeer]
	if !ok {
		t.Fatal("peer missing after reload")
	}
	if len(e.Addrs) != 2 {
		t.Fatalf("got %d addrs, want 2: %v", len(e.Addrs), e.Addrs)
	}
	if e.SeenAt == "" {
		t.Fatal("SeenAt should be stamped so staleness is visible")
	}
}

func TestUpdateAddrsIgnoresStrangers(t *testing.T) {
	s, dir := newPairedStore(t, testPeer)
	stranger := "12D3KooWSomeOtherPeer000000000000000000000000000000"
	if err := s.UpdateAddrs(stranger, []string{"/ip4/10.0.0.9/tcp/4001"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range s.List() {
		if e.PeerID == stranger {
			t.Fatal("must not start tracking a device that was never paired")
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "trusted_peers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "10.0.0.9") {
		t.Fatal("a stranger's address must not reach disk")
	}
}

// Loopback and unspecified addresses never help when dialing another
// machine, and storing them wastes the space that real addresses need.
func TestUpdateAddrsDropsLoopback(t *testing.T) {
	s, _ := newPairedStore(t, testPeer)
	addrs := []string{
		"/ip4/127.0.0.1/tcp/4001",
		"/ip4/0.0.0.0/tcp/4001",
		"/ip4/192.168.1.5/tcp/4001",
	}
	if err := s.UpdateAddrs(testPeer, addrs); err != nil {
		t.Fatal(err)
	}
	e := s.byID[testPeer]
	if len(e.Addrs) != 1 || !strings.Contains(e.Addrs[0], "192.168.1.5") {
		t.Fatalf("only the routable address should be kept, got %v", e.Addrs)
	}
}

func TestUpdateAddrsIsBounded(t *testing.T) {
	s, _ := newPairedStore(t, testPeer)
	var many []string
	for i := 0; i < 40; i++ {
		many = append(many, "/ip4/10.0.0."+strconv.Itoa(i%250+1)+"/tcp/"+strconv.Itoa(4000+i))
	}
	if err := s.UpdateAddrs(testPeer, many); err != nil {
		t.Fatal(err)
	}
	if got := len(s.byID[testPeer].Addrs); got > MaxTrackedAddrs {
		t.Fatalf("stored %d addrs, bound is %d", got, MaxTrackedAddrs)
	}
}

func TestUpdateAddrsDeduplicates(t *testing.T) {
	s, _ := newPairedStore(t, testPeer)
	a := "/ip4/192.168.1.5/tcp/4001"
	if err := s.UpdateAddrs(testPeer, []string{a}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAddrs(testPeer, []string{a, a}); err != nil {
		t.Fatal(err)
	}
	if got := len(s.byID[testPeer].Addrs); got != 1 {
		t.Fatalf("got %d addrs, want 1: %v", got, s.byID[testPeer].Addrs)
	}
}

func TestUpdateAddrsWithNothingUsableIsNoop(t *testing.T) {
	s, _ := newPairedStore(t, testPeer)
	if err := s.UpdateAddrs(testPeer, []string{"", "not-a-multiaddr", "/ip4/127.0.0.1/tcp/1"}); err != nil {
		t.Fatal(err)
	}
	if got := s.byID[testPeer].Addrs; len(got) != 0 {
		t.Fatalf("nothing dialable was offered, so nothing should be stored: %v", got)
	}
}

func TestUpdateAddrsGarbageInFileDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "trusted_peers.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTrustStore(dir); err == nil {
		t.Fatal("a corrupt trust file should report an error, not load empty")
	}
}

// An address stored before the loopback rule existed must be cleaned up on
// the next update rather than lingering for ever.
func TestUpdateAddrsPrunesPreviouslyStoredLoopback(t *testing.T) {
	s, _ := newPairedStore(t, testPeer)
	// Reach into the entry the way an older build would have written it.
	s.byID[testPeer] = TrustEntry{PeerID: testPeer, Alias: "laptop", Addrs: []string{"/ip4/127.0.0.1/tcp/4001"}}

	if err := s.UpdateAddrs(testPeer, []string{"/ip4/192.168.1.5/tcp/4001"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range s.byID[testPeer].Addrs {
		if strings.Contains(a, "127.0.0.1") {
			t.Fatalf("a stale loopback address survived: %v", s.byID[testPeer].Addrs)
		}
	}
}
