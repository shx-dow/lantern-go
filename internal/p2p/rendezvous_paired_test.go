package p2p

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	libpcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func testPeerID(t *testing.T) peer.ID {
	t.Helper()
	key, _, err := libpcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The two devices in a pairing have to compute the same name, and a third peer
// must not. Deriving it from the sorted pair is what makes both true.
func TestPairedLookupKeyIsSymmetricAndJoint(t *testing.T) {
	alice, bob, carol := testPeerID(t), testPeerID(t), testPeerID(t)

	ab := PairedLookupKey(alice, bob)
	if got := PairedLookupKey(bob, alice); got != ab {
		t.Fatalf("the two sides derive different names:\n  alice+bob: %s\n  bob+alice: %s", ab, got)
	}
	if ab == PairedLookupKey(alice, carol) || ab == PairedLookupKey(bob, carol) {
		t.Fatal("a different pairing derived the same name")
	}
	if ab == PairedLookupKey(alice, alice) {
		t.Fatal("a pairing with oneself derived the same name")
	}
	if !strings.HasPrefix(ab, "paired-") {
		t.Fatalf("paired name %q is not distinguishable from a share code or invitation", ab)
	}
}

func newTestNode(t *testing.T) *Node {
	t.Helper()
	n, err := NewNode(0, []string{"none"}, nil, t.TempDir(), WithoutMDNS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func connectTestNodes(t *testing.T, ctx context.Context, from, to *Node) {
	t.Helper()
	info := peer.AddrInfo{ID: to.Host.ID(), Addrs: to.Host.Addrs()}
	if err := from.Host.Connect(ctx, info); err != nil {
		t.Fatalf("connect %s to %s: %v", from.Host.ID(), to.Host.ID(), err)
	}
}

// A paired device that is not on this LAN and whose cached address is stale is
// found through the name only the two of them share. A router in between keeps
// them off each other's direct connection list, so the answer has to come from
// the DHT.
func TestPairedAdvertisementFindsThePeerOverTheDHT(t *testing.T) {
	if testing.Short() {
		t.Skip("DHT propagation is a network operation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	router := newTestNode(t)
	alice := newTestNode(t)
	bob := newTestNode(t)

	connectTestNodes(t, ctx, alice, router)
	connectTestNodes(t, ctx, bob, router)
	if err := alice.DHT.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap alice: %v", err)
	}
	if err := bob.DHT.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap bob: %v", err)
	}

	advCtx, stopAdvertise := context.WithCancel(ctx)
	defer stopAdvertise()
	go func() {
		// Provide can fail while the routing table settles; AdvertisePaired
		// retries, so an error here only ends a goroutine the test has already
		// stopped.
		_ = alice.AdvertisePaired(advCtx, bob.Host.ID())
	}()

	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		info, err := bob.FindPaired(ctx, alice.Host.ID())
		if err == nil {
			if info.ID != alice.Host.ID() {
				t.Fatalf("found %s, want %s", info.ID, alice.Host.ID())
			}
			if len(info.Addrs) == 0 {
				t.Fatal("found the peer with no address to dial")
			}
			return
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("bob never found alice through the paired rendezvous: %v", lastErr)
}
