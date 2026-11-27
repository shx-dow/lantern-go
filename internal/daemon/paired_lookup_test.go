package daemon

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"

	libpcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func generatedPeer(t *testing.T) peer.ID {
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

// The DHT lookup is for paired devices only. An unpaired reference has to be
// refused before any network work, or any caller could make this daemon query
// on behalf of a stranger.
func TestFindPairedPeerOnlyLooksUpPairedDevices(t *testing.T) {
	paired := generatedPeer(t)
	store, _ := newPairedStore(t, paired.String())
	d := New(nil)
	d.Trust = store

	unpaired := generatedPeer(t)
	if _, err := d.FindPairedPeer(context.Background(), unpaired); err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("an unpaired peer was not refused by the trust gate: %v", err)
	}

	// The paired peer passes the trust gate and fails at the next stage,
	// because this daemon was built without a node. Reaching that stage is the
	// property: the lookup is allowed only because the pairing exists.
	if _, err := d.FindPairedPeer(context.Background(), paired); err == nil || !strings.Contains(err.Error(), "node not ready") {
		t.Fatalf("a paired peer did not reach the lookup stage: %v", err)
	}
}

func TestFindPairedPeerRefusesWithoutATrustStore(t *testing.T) {
	d := New(nil)
	if _, err := d.FindPairedPeer(context.Background(), generatedPeer(t)); err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("a daemon with no trust store should not look up anyone: %v", err)
	}
}
