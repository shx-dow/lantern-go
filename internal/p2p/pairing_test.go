package p2p

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// pairingNodes is an inviter and a redeemer that can reach each other on
// loopback. The fs host keeps these tests off the DHT and mDNS, which have
// nothing to do with pairing.
func pairingNodes(t *testing.T) (*Node, *Node) {
	t.Helper()
	return fsHost(t), fsHost(t)
}

// One paste is the whole claim: the inviter records the redeemer, the redeemer
// learns who it paired with, and neither side needs a peer ID typed by hand.
func TestPairingPairsBothSidesInOnePaste(t *testing.T) {
	inviter, redeemer := pairingNodes(t)
	inviter.RegisterPairHandler()

	var seenPeer peer.ID
	var seenName string
	inviter.SetPairAcceptor(func(remote peer.ID, name string) (string, string, error) {
		seenPeer, seenName = remote, name
		return "laptop", "read", nil
	})

	text, expires, err := inviter.MintInvitation(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, invitationPrefix) {
		t.Errorf("invitation %q does not carry the prefix", text)
	}
	if wait := time.Until(expires); wait < DefaultPairTTL-time.Minute || wait > DefaultPairTTL {
		t.Errorf("expiry is %s away, want about %s", wait, DefaultPairTTL)
	}

	res, err := redeemer.RedeemInvitation(context.Background(), text, "desktop")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if res.PeerID != inviter.Host.ID().String() {
		t.Errorf("peer = %q, want the inviter %q", res.PeerID, inviter.Host.ID())
	}
	if res.DeviceName != "laptop" || res.Tier != "read" {
		t.Errorf("result = %+v, want laptop at read", res)
	}
	if seenPeer != redeemer.Host.ID() {
		t.Errorf("inviter saw peer %q, want %q", seenPeer, redeemer.Host.ID())
	}
	if seenName != "desktop" {
		t.Errorf("inviter saw name %q, want desktop", seenName)
	}
	if len(res.Addrs) == 0 {
		t.Error("invitation carried no address, so the redeemer could not dial it later")
	}
}

// The invitation is one pairing, not a standing password. A second redeem must
// not pair a second device.
func TestPairingInvitationIsSingleUse(t *testing.T) {
	inviter, redeemer := pairingNodes(t)
	inviter.RegisterPairHandler()
	inviter.SetPairAcceptor(func(peer.ID, string) (string, string, error) {
		return "laptop", "read", nil
	})

	text, _, err := inviter.MintInvitation(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := redeemer.RedeemInvitation(context.Background(), text, "desktop"); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	_, err = redeemer.RedeemInvitation(context.Background(), text, "desktop")
	if err == nil {
		t.Fatal("the same invitation paired a second time")
	}
	if !strings.Contains(err.Error(), "not known") {
		t.Errorf("second redeem failed with %q, want a spent-invitation message", err)
	}
}

// A redemption that reaches the inviter spends the invitation even when the
// pairing is refused, so retrying a leaked string cannot succeed later.
func TestPairingBurnsTheInvitationOnAFailedRedemption(t *testing.T) {
	inviter, redeemer := pairingNodes(t)
	inviter.RegisterPairHandler()
	inviter.SetPairAcceptor(func(peer.ID, string) (string, string, error) {
		return "", "", errors.New("refused on purpose")
	})

	text, _, err := inviter.MintInvitation(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := redeemer.RedeemInvitation(context.Background(), text, "desktop"); err == nil ||
		!strings.Contains(err.Error(), "refused on purpose") {
		t.Fatalf("first redeem = %v, want the acceptor's refusal", err)
	}
	if _, err := redeemer.RedeemInvitation(context.Background(), text, "desktop"); err == nil ||
		!strings.Contains(err.Error(), "not known") {
		t.Fatalf("second redeem = %v, want a spent-invitation message", err)
	}
}

func TestPairingExpiredInvitationIsRefused(t *testing.T) {
	inviter, redeemer := pairingNodes(t)
	inviter.RegisterPairHandler()
	inviter.SetPairAcceptor(func(peer.ID, string) (string, string, error) {
		return "laptop", "read", nil
	})

	text, _, err := inviter.MintInvitation(0)
	if err != nil {
		t.Fatal(err)
	}
	// Reach into the store rather than sleeping: the expiry clock is what
	// matters, not how long a test is willing to wait.
	inviter.pair.mu.Lock()
	for k := range inviter.pair.invites {
		inviter.pair.invites[k] = time.Now().Add(-time.Second)
	}
	inviter.pair.mu.Unlock()

	if _, err := redeemer.RedeemInvitation(context.Background(), text, "desktop"); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("redeem of an expired invitation = %v, want an expiry message", err)
	}
}

func TestPairingRefusesTextThatIsNotAnInvitation(t *testing.T) {
	_, redeemer := pairingNodes(t)
	for _, text := range []string{
		"",
		"not-an-invitation",
		"0123456789abcdef0123456789abcdef", // a share code
		"12D3KooWpeer",
		invitationPrefix + "!!!",
	} {
		_, err := redeemer.RedeemInvitation(context.Background(), text, "desktop")
		if err == nil {
			t.Errorf("%q was accepted as an invitation", text)
			continue
		}
		if !strings.Contains(err.Error(), "invitation") {
			t.Errorf("%q failed with %q, which does not name the problem", text, err)
		}
	}
}

func TestMintInvitationBoundsItsLifetime(t *testing.T) {
	node, _ := pairingNodes(t)
	for _, ttl := range []time.Duration{-time.Second, MaxPairTTL + time.Second} {
		if _, _, err := node.MintInvitation(ttl); err == nil {
			t.Errorf("ttl %s was accepted", ttl)
		}
	}
	if _, _, err := node.MintInvitation(time.Minute); err != nil {
		t.Errorf("a one-minute invitation was refused: %v", err)
	}
}

// An invitation is read on another machine, so loopback addresses are dropped
// when there is anywhere else to dial. When loopback is all a node has -- a
// container, a test -- keeping it is the difference between an invitation that
// works and one that carries no address at all.
func TestDialableAddrsDropsLoopbackOnlyWhenSomethingElseIsDialable(t *testing.T) {
	lo := multiaddr.StringCast("/ip4/127.0.0.1/tcp/4001")
	lan := multiaddr.StringCast("/ip4/192.168.1.5/tcp/4001")
	v6 := multiaddr.StringCast("/ip6/::1/tcp/4001")

	if got := dialableAddrs([]multiaddr.Multiaddr{lo, lan}); len(got) != 1 || got[0] != lan.String() {
		t.Errorf("with a LAN address, got %v, want only %s", got, lan)
	}
	if got := dialableAddrs([]multiaddr.Multiaddr{lo, v6}); len(got) != 2 {
		t.Errorf("with only loopback, got %v, want both kept", got)
	}
}
