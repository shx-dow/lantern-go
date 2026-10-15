// Package lanterntest brings up real Lantern devices that can reach one
// another, for the tests that need a connection rather than a fake.
//
// Most of this repository is tested against doubles: an httptest server, a
// hand-written request, a stub session. That is the right default — it is fast,
// and it says precisely which behaviour is under test. But some of what Lantern
// does only exists between two devices. A share code is minted by one and
// redeemed by the other; pairing means a trust store; a file arrives because a
// stream was negotiated. None of that is reachable with a double, and none of
// it was covered until this package existed.
//
// Two real nodes on the loopback interface, wired so each can dial the other
// before discovery runs. That last part matters: without it a test depends on
// mDNS or the DHT finding a peer, which is slow, flaky, and tests a different
// thing.
package lanterntest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shx-dow/lantern-go/internal/p2p"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

// DialTimeout bounds a test's whole life. It is generous enough that a loaded
// runner does not fail a healthy dial, and short enough that a test which does
// hang is reported rather than waited out.
const DialTimeout = 30 * time.Second

// Device is one Lantern node, brought up for a test.
type Device struct {
	// Lantern is the node itself. Send and receive take this directly, which is
	// what makes the CLI's own commands testable in process.
	Lantern *lantern.Lantern
	// Node is the same node's p2p layer, for a test that needs to change what
	// this device serves.
	Node *p2p.Node
	// ID is this device's peer ID: what the other device addresses.
	ID string
	// Addrs are the addresses this device listens on.
	Addrs []string
	// DataDir holds the identity key and whatever else this device persists.
	DataDir string
}

// Pair is two devices that already know how to reach each other.
type Pair struct {
	// Sender serves content; Receiver asks for it.
	Sender, Receiver *Device
}

// Close tears both devices down. Registered with the test, so a test that fails
// early does not leave two hosts running for the rest of the package.
func (p Pair) Close() {
	if p.Receiver != nil && p.Receiver.Lantern != nil {
		p.Receiver.Lantern.Close()
	}
	if p.Sender != nil && p.Sender.Lantern != nil {
		p.Sender.Lantern.Close()
	}
}

// New brings up one device serving roots.
//
// It registers the list and filesystem handlers and grants read access to those
// roots for any peer, which is the shape a trusted device presents. Pairing
// tiers are a daemon concern and are deliberately not modelled here: a test
// that needs them should build on Pair and layer its own trust store on top,
// the way internal/daemon does.
func New(t *testing.T, roots ...string) *Device {
	t.Helper()

	dataDir := t.TempDir()
	ln, err := lantern.New(lantern.Config{Port: 0, DataDir: dataDir})
	if err != nil {
		t.Fatalf("bringing up a lantern device: %v", err)
	}
	node := ln.Node()
	if node == nil || node.Host == nil {
		ln.Close()
		t.Fatal("the device did not expose a host")
	}

	node.SetReadAccess(func(string) []string { return roots })
	node.RegisterListHandler()
	node.RegisterFSHandler()

	d := &Device{
		Lantern: ln,
		Node:    node,
		ID:      node.Host.ID().String(),
		DataDir: dataDir,
	}
	for _, a := range node.Host.Addrs() {
		d.Addrs = append(d.Addrs, a.String())
	}
	return d
}

// NewPair brings up two devices and teaches each the other's addresses, so
// either can dial the other with no discovery running.
func NewPair(t *testing.T, senderRoots ...string) Pair {
	t.Helper()

	sender := New(t, senderRoots...)
	receiver := New(t)

	// Known addresses are what resolvePeer consults, so a dial works on the
	// first attempt rather than after a discovery round.
	sender.Lantern.WithKnownAddrs(func(peerID string) []string {
		if peerID == receiver.ID {
			return receiver.Addrs
		}
		return nil
	})
	receiver.Lantern.WithKnownAddrs(func(peerID string) []string {
		if peerID == sender.ID {
			return sender.Addrs
		}
		return nil
	})

	p := Pair{Sender: sender, Receiver: receiver}
	t.Cleanup(p.Close)
	return p
}

// Ctx is a context bounded by DialTimeout, for calls that should finish on
// their own.
//
// A test that hangs instead of failing is the worst outcome available: it says
// nothing, and it holds the package's timeout before anyone learns what broke.
func Ctx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), DialTimeout)
	t.Cleanup(cancel)
	return ctx
}

// BridgeDiscovery copies the sender's local advertisement for code into the
// receiver's directory, so the receiver finds it without a DHT round trip.
//
// Share codes are resolved by rendezvous, not by address: the sender publishes
// under the code and the receiver looks it up. This module advertises locally,
// into the node's own data directory, and looks there too — so a test with two
// separate data directories cannot resolve a code at all, and Discover falls
// through to the DHT, which in a unit test means a timeout rather than a peer.
//
// Note what this implies beyond the test. The local advertisement only helps
// when two devices share a rendezvous directory, and each device uses its own
// data directory, so in ordinary use it never fires: the comment on
// AdvertiseLocal calls it same-machine discovery, and two Lanterns on one
// machine do not look there. Bridging it in a test makes the share-code route
// reachable, but the route it reaches is not the one two real same-machine
// devices would take. Whether that is a real gap in the product or only in the
// comment is a question for the maintainer, not for this helper.
func (p Pair) BridgeDiscovery(t *testing.T, code string) {
	t.Helper()

	name := "lantern-" + code + ".peer"
	data, err := os.ReadFile(filepath.Join(p.Sender.DataDir, name))
	if err != nil {
		t.Fatalf("the sender advertised no local file for code %s: %v", code, err)
	}
	if err := os.WriteFile(filepath.Join(p.Receiver.DataDir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(p.Receiver.DataDir, name)) })
}
