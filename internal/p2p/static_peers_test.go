package p2p

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// A statically configured peer must be recorded permanently and stay
// dialable on demand, without opening a connection up front. This is the
// --peer path for networks where discovery cannot introduce devices.
func TestAddStaticPeersRecordsPermanently(t *testing.T) {
	n := fsHost(t)
	other := fsHost(t)

	full, err := peer.AddrInfoToP2pAddrs(&peer.AddrInfo{ID: other.Host.ID(), Addrs: other.Host.Addrs()})
	if err != nil || len(full) == 0 {
		t.Fatalf("test peer has no dialable address: %v", err)
	}
	added := n.AddStaticPeers([]string{full[0].String()})
	if len(added) != 1 || added[0].ID != other.Host.ID() {
		t.Fatalf("static peer not returned: %+v", added)
	}
	if addrs := n.Host.Peerstore().Addrs(other.Host.ID()); len(addrs) == 0 {
		t.Fatal("static peer address was not recorded")
	}
	if err := n.Host.Connect(t.Context(), added[0]); err != nil {
		t.Fatalf("a recorded static peer should be dialable on demand: %v", err)
	}
}

func TestAddStaticPeersSkipsInvalid(t *testing.T) {
	n := fsHost(t)
	other := fsHost(t)
	// No /p2p/ suffix: no peer ID to attach the address to.
	bare := other.Host.Addrs()[0].String()
	added := n.AddStaticPeers([]string{"not-an-addr", bare, "  "})
	if len(added) != 0 {
		t.Fatalf("invalid static peers should be skipped, got %+v", added)
	}
}
