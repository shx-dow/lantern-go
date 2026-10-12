package p2p

import (
	"context"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// WithDialTimeout has to reach go-libp2p's swarm, because nothing else can
// move the dial deadline: every FS operation caps its own context, and
// context.WithTimeout with a later deadline than its parent is discarded.
//
// The digest tests that motivated the option cannot check this. They pass on an
// idle runner whether or not the override does anything, so a change that
// dropped the wiring — or kept DialTimeout but forgot DialTimeoutLocal, which
// is the one that applies to a LAN — would leave them green.
//
// Two overrides, one shorter than go-libp2p's defaults and one longer, because
// only the long one can tell the two halves of the option apart. Neither
// encodes what those defaults are.
func TestWithDialTimeoutMovesTheDialDeadline(t *testing.T) {
	// Accepts the connection and then says nothing, so the dial dies in the
	// security handshake — the stage the Windows failure actually reached. An
	// unroutable address would never get past TCP and would only measure the
	// transport's own connect timeout.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, c) }()
		}
	}()

	id := strangerID(t)

	// Short override: shorter than both of go-libp2p's defaults, so it is what
	// wins. This is what catches the override not reaching the swarm at all.
	short := dialElapsed(t, ln, id, 200*time.Millisecond)
	if short > 1500*time.Millisecond {
		t.Errorf("a 200ms dial limit took %s; the override is not reaching the swarm", short.Round(time.Millisecond))
	}

	// Long override: longer than both defaults, so each half is load-bearing.
	// The swarm takes dialTimeout, but falls back to dialTimeoutLocal when that
	// is shorter, and every address on the same LAN is private — so an override
	// that set only one of them would be cut back to the 5s private default
	// that a short override never runs into.
	long := dialElapsed(t, ln, id, 7*time.Second)
	if long < 6*time.Second {
		t.Errorf("a 7s dial limit took %s; something shorter is still capping it — is DialTimeoutLocal applied?", long.Round(time.Millisecond))
	}

	// Not covered: dropping WithDialTimeout while keeping WithDialTimeoutLocal.
	// The swarm takes dialTimeoutLocal whenever it is the shorter of the two,
	// so a 7s override lands in the same place either way, and separating them
	// needs a non-private address — which cannot be reached at the handshake
	// stage from a test without a second host on another network. The two calls
	// are adjacent lines of one option, so the reachable mistake is covered and
	// this one is not.
}

// dialElapsed brings up a node with the given dial timeout and reports how
// long a dial to a silent listener on the same machine took.
func dialElapsed(t *testing.T, target net.Listener, target2 peer.ID, d time.Duration) time.Duration {
	t.Helper()

	n, err := NewNode(0, []string{"none"}, nil, t.TempDir(), WithDialTimeout(d))
	if err != nil {
		t.Fatalf("build node with a %s dial timeout: %v", d, err)
	}
	defer n.Close()

	addr, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/" + strconv.Itoa(target.Addr().(*net.TCPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	// The dial is meant to fail: the listener never speaks the security
	// protocol, so what matters is when the node gives up.
	_ = n.Host.Connect(ctx, peer.AddrInfo{ID: target2, Addrs: []multiaddr.Multiaddr{addr}})
	return time.Since(start)
}

// strangerID is a peer ID nobody is listening on.
func strangerID(t *testing.T) peer.ID {
	t.Helper()
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
