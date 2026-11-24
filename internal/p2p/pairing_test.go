package p2p

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// pairingNodes is an inviter and a redeemer that can reach each other on
// loopback. The fs host keeps these tests off the DHT and mDNS, which have
// nothing to do with pairing; a shared rendezvous directory stands in for the
// DHT lookup an invitation now relies on.
func pairingNodes(t *testing.T) (*Node, *Node) {
	t.Helper()
	inviter, redeemer := fsHost(t), fsHost(t)
	dir := t.TempDir()
	inviter.localDir = dir
	redeemer.localDir = dir
	return inviter, redeemer
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
	if !strings.Contains(text, ".") || strings.Count(strings.TrimPrefix(text, invitationPrefix), ".") != 1 {
		t.Errorf("invitation %q is not <secret>.<expiry>", text)
	}
	if len(text) > 100 {
		t.Errorf("invitation is %d characters; the address-free form should be about 70", len(text))
	}
	for _, leak := range []string{"ip4", "ip6", "tcp", "udp", inviter.Host.ID().String()} {
		if strings.Contains(text, leak) {
			t.Errorf("invitation %q leaks %q", text, leak)
		}
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
	for k, inv := range inviter.pair.invites {
		inv.expires = time.Now().Add(-time.Second)
		inviter.pair.invites[k] = inv
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
		invitationPrefix + "eyJ2ZXJzaW9uIjoxfQ", // the first, address-carrying format
		invitationPrefix + "AAAA.AAAA",
		invitationPrefix + "AAAA.1.2",
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

// The invitation is a secret and an expiry, so the only thing a chat window or
// screenshot can retain is a capability that dies after one use. The inviter's
// addresses live in rendezvous, keyed by a hash the holder of the secret can
// derive and nobody else can.
func TestPairingRendezvousNameNeedsTheSecretAndIsNotTheSecret(t *testing.T) {
	inviter, _ := pairingNodes(t)
	text, _, err := inviter.MintInvitation(0)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := parseInvitation(text)
	if err != nil {
		t.Fatal(err)
	}
	key := pairLookupKey(inv.Secret)
	if strings.Contains(key, inv.Secret) {
		t.Errorf("rendezvous name %q contains the secret", key)
	}
	if key == pairLookupKey(inv.Secret+"x") {
		t.Error("a different secret derived the same rendezvous name")
	}
}

// An invitation has to become findable for the holder of the secret, or the
// paste has nothing to connect to. The path is the hash-derived name, not the
// secret or an address.
func TestPairingInvitationIsAdvertisedUnderItsRendezvousName(t *testing.T) {
	inviter, _ := pairingNodes(t)
	text, _, err := inviter.MintInvitation(0)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := parseInvitation(text)
	if err != nil {
		t.Fatal(err)
	}
	path, err := inviter.peerFilePath(pairLookupKey(inv.Secret))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the invitation was not advertised: %v", err)
	}
	if strings.Contains(path, inv.Secret) {
		t.Errorf("advertisement path %q contains the secret", path)
	}
}
