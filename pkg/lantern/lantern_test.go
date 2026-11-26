package lantern

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	libpcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

var errTest = errors.New("test error")

func TestSubscribeReceivesEventsAndStopsOnCancel(t *testing.T) {
	l := &Lantern{
		events:      make(chan Event, 1),
		subscribers: make(map[uint64]subscription),
	}
	events, unsubscribe := l.Subscribe(1)
	l.emit(Event{TransferID: "transfer-1", Type: EventTransferDone})

	got := <-events
	if got.TransferID != "transfer-1" || got.Type != EventTransferDone {
		t.Fatalf("got %+v", got)
	}
	unsubscribe()
	if _, ok := <-events; ok {
		t.Fatal("subscription remained open after cancellation")
	}
}

func TestSubscribeAfterCloseIsClosed(t *testing.T) {
	l := &Lantern{
		events:      make(chan Event, 1),
		subscribers: make(map[uint64]subscription),
		closed:      true,
	}
	events, unsubscribe := l.Subscribe(1)
	defer unsubscribe()
	if _, ok := <-events; ok {
		t.Fatal("subscription was open after close")
	}
}

func TestNewSessionHasIDImmediately(t *testing.T) {
	l := &Lantern{
		events:      make(chan Event, 1),
		subscribers: make(map[uint64]subscription),
	}
	s := l.newSession(context.Background(), "code-1")
	defer s.Close()
	if s.ID() != "code-1" {
		t.Fatalf("session ID is %q, want code-1", s.ID())
	}
	if s.State() != TransferRunning {
		t.Fatalf("session state is %v, want running", s.State())
	}
	select {
	case <-s.Done():
		t.Fatal("fresh session reports done")
	default:
	}
}

func TestSessionTracksOnlyItsTransfer(t *testing.T) {
	l := &Lantern{
		events:      make(chan Event, 1),
		subscribers: make(map[uint64]subscription),
	}
	s := l.newSession(context.Background(), "transfer-1")
	defer s.Close()

	l.emit(Event{TransferID: "transfer-2", Type: EventTransferDone})
	if s.State() != TransferRunning {
		t.Fatalf("unrelated event changed state to %v", s.State())
	}
	l.emit(Event{TransferID: "transfer-1", Type: EventTransferProgress, Bytes: 1, Total: 2})
	if s.State() != TransferRunning {
		t.Fatalf("progress event changed state to %v", s.State())
	}
	l.emit(Event{TransferID: "transfer-1", Type: EventTransferDone})
	if s.State() != TransferDone {
		t.Fatalf("session state is %v, want done", s.State())
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("session did not close Done")
	}
}

func TestSessionFailsOnTransferError(t *testing.T) {
	l := &Lantern{
		events:      make(chan Event, 1),
		subscribers: make(map[uint64]subscription),
	}
	s := l.newSession(context.Background(), "transfer-9")
	defer s.Close()

	l.emit(Event{TransferID: "transfer-9", Type: EventError, Err: errTest})
	if s.State() != TransferFailed {
		t.Fatalf("session state is %v, want failed", s.State())
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("failed session did not close Done")
	}
}

func TestEmitDoesNotBlockOnSlowSubscriber(t *testing.T) {
	l := &Lantern{
		events:      make(chan Event, 64),
		subscribers: make(map[uint64]subscription),
	}
	slow, unsubSlow := l.Subscribe(1)
	defer unsubSlow()
	fast, unsubFast := l.Subscribe(64)
	defer unsubFast()

	// Fill the slow subscriber so its buffer is full and unread.
	l.emit(Event{TransferID: "x", Type: EventTransferProgress, Bytes: 1, Total: 10})
	<-slow // consume one to leave buffer state deterministic

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			l.emit(Event{TransferID: "x", Type: EventTransferProgress, Bytes: int64(i), Total: 20})
		}
		l.emit(Event{TransferID: "x", Type: EventTransferDone})
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("emit blocked on slow subscriber")
	}

	select {
	case e := <-fast:
		_ = e
	case <-time.After(5 * time.Second):
		t.Fatal("fast subscriber received nothing")
	}
}

// A peer the network has not announced must still be dialable from the
// cached addresses, so a restart can reach a paired device immediately.
func TestResolvePeerUsesCachedAddrs(t *testing.T) {
	ln, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	other, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })

	// Learn the peer's addresses, then forget them from the peerstore so the
	// only remaining source is the cache.
	target := other.Node().Host.ID().String()
	addrs := make([]string, 0)
	for _, a := range other.Node().Host.Addrs() {
		addrs = append(addrs, a.String())
	}
	ln.WithKnownAddrs(func(string) []string { return addrs })
	ln.Node().Host.Peerstore().ClearAddrs(other.Node().Host.ID())

	pi, err := ln.resolvePeer(context.Background(), target)
	if err != nil {
		t.Fatalf("resolvePeer should fall back to cached addresses: %v", err)
	}
	if len(pi.Addrs) == 0 {
		t.Fatal("resolved peer has no addresses to dial")
	}
	if pi.ID != other.Node().Host.ID() {
		t.Fatal("resolved the wrong peer")
	}
}

func TestResolvePeerFailsCleanlyWhenUnknown(t *testing.T) {
	ln, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	// A well-formed peer ID with no host behind it. Starting a real node and
	// calling it "unknown" is wrong: it is on the network, and discovery can
	// find it before the resolve gives up.
	unknownKey, _, err := libpcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := peer.IDFromPrivateKey(unknownKey)
	if err != nil {
		t.Fatal(err)
	}
	ln.WithKnownAddrs(func(string) []string { return nil })

	_, err = ln.resolvePeer(context.Background(), unknown.String())
	if err == nil {
		t.Fatal("expected an error for a peer that is not on the network")
	}
	// The message has to tell an agent what to do about it.
	if !strings.Contains(err.Error(), "network") {
		t.Fatalf("error should be actionable, got: %v", err)
	}
}

func TestResolvePeerRejectsGarbage(t *testing.T) {
	ln, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	if _, err := ln.resolvePeer(context.Background(), "not-a-peer-id"); err == nil {
		t.Fatal("expected a decode error")
	}
}

// A paired device whose cached address has gone stale is found through the
// pairing's rendezvous. Before this fallback existed, resolvePeer stopped at
// the cache and reported a device that was online as "not on this network".
func TestResolvePeerUsesThePairedLookupWhenNoAddressIsKnown(t *testing.T) {
	ln, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	// A well-formed peer ID with no host behind it: the only way to name it is
	// the paired lookup.
	key, _, err := libpcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	target, err := peer.IDFromPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := multiaddr.NewMultiaddr("/ip4/192.0.2.10/tcp/41000")
	if err != nil {
		t.Fatal(err)
	}

	called := false
	ln.WithPairedLookup(func(_ context.Context, id peer.ID) (peer.AddrInfo, error) {
		called = true
		if id != target {
			t.Errorf("paired lookup asked for %s, want %s", id, target)
		}
		return peer.AddrInfo{ID: id, Addrs: []multiaddr.Multiaddr{addr}}, nil
	})

	info, err := ln.resolvePeer(context.Background(), target.String())
	if err != nil {
		t.Fatalf("resolvePeer ignored the paired lookup: %v", err)
	}
	if !called {
		t.Fatal("the paired lookup was not consulted")
	}
	if info.ID != target || len(info.Addrs) != 1 || info.Addrs[0].String() != addr.String() {
		t.Fatalf("resolvePeer returned %+v, want %s at %s", info, target, addr)
	}
}

// The fallback is last: a fresher address from the peerstore must win, and the
// lookup must not run at all.
func TestResolvePeerPrefersAKnownAddressOverThePairedLookup(t *testing.T) {
	ln, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	other, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })

	ln.Node().Host.Peerstore().AddAddrs(other.Node().Host.ID(), other.Node().Host.Addrs(), time.Minute)
	ln.WithPairedLookup(func(context.Context, peer.ID) (peer.AddrInfo, error) {
		t.Error("the paired lookup ran even though a peerstore address was known")
		return peer.AddrInfo{}, nil
	})

	info, err := ln.resolvePeer(context.Background(), other.Node().Host.ID().String())
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Addrs) == 0 {
		t.Fatal("resolved the peer with no address")
	}
}
