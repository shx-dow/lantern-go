package p2p

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// A device that hears another Lantern on the network must record where it is
// without opening a connection. This is what keeps the connection count from
// growing with the square of the number of devices present.
func TestMDNSRecordsAddressWithoutConnecting(t *testing.T) {
	n := fsHost(t)
	other := fsHost(t)

	d := &mdnsDiscovery{node: n}
	d.HandlePeerFound(peer.AddrInfo{ID: other.Host.ID(), Addrs: other.Host.Addrs()})

	if n.Host.Network().Connectedness(other.Host.ID()) == network.Connected {
		t.Fatal("announcing a peer must not open a connection to it")
	}
	addrs := n.Host.Peerstore().Addrs(other.Host.ID())
	if len(addrs) == 0 {
		t.Fatal("the announced address should have been recorded")
	}
	// A recorded address is what lets resolvePeer dial on demand later.
	pi := peer.AddrInfo{ID: other.Host.ID(), Addrs: addrs}
	if err := n.Host.Connect(t.Context(), pi); err != nil {
		t.Fatalf("a recorded address should be dialable on demand: %v", err)
	}
}

func TestMDNSIgnoresSelf(t *testing.T) {
	n := fsHost(t)
	// libp2p seeds a host's own addresses at startup, so clear them first or
	// this would pass or fail for reasons unrelated to the handler.
	n.Host.Peerstore().ClearAddrs(n.Host.ID())
	if got := n.Host.Peerstore().Addrs(n.Host.ID()); len(got) != 0 {
		t.Fatalf("could not isolate the handler: %d self addrs remain", len(got))
	}

	d := &mdnsDiscovery{node: n}
	d.HandlePeerFound(peer.AddrInfo{ID: n.Host.ID(), Addrs: n.Host.Addrs()})

	if got := n.Host.Peerstore().Addrs(n.Host.ID()); len(got) != 0 {
		t.Fatalf("a node must not record its own announcement as a peer, got %d", len(got))
	}
}

func TestMDNSIgnoresEmptyAddresses(t *testing.T) {
	n := fsHost(t)
	other := fsHost(t)
	d := &mdnsDiscovery{node: n}
	d.HandlePeerFound(peer.AddrInfo{ID: other.Host.ID()})
	if len(n.Host.Peerstore().Addrs(other.Host.ID())) != 0 {
		t.Fatal("an announcement with no addresses should be ignored")
	}
}

// Recorded addresses must expire, or a device that moved would be dialed at
// an address that no longer belongs to it for ever.
func TestSeenAddressTTLIsShort(t *testing.T) {
	if SeenAddressTTL > 30*time.Minute {
		t.Fatalf("seen addresses live too long to notice a device moving: %v", SeenAddressTTL)
	}
	n := fsHost(t)
	other := fsHost(t)
	(&mdnsDiscovery{node: n}).HandlePeerFound(peer.AddrInfo{ID: other.Host.ID(), Addrs: other.Host.Addrs()})

	// Shrink the recorded lifetime to confirm the TTL is what governs it.
	otherID := other.Host.ID()
	n.Host.Peerstore().ClearAddrs(otherID)
	n.Host.Peerstore().AddAddrs(otherID, other.Host.Addrs(), time.Second)
	if len(n.Host.Peerstore().Addrs(otherID)) == 0 {
		t.Fatal("address should be present before expiry")
	}
	time.Sleep(1500 * time.Millisecond)
	if got := n.Host.Peerstore().Addrs(otherID); len(got) != 0 {
		t.Fatalf("address should have expired, still have %d", len(got))
	}
}
