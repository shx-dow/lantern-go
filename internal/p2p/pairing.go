package p2p

// Pairing by invitation: one string minted on one device, redeemed on another,
// leaving both devices paired at read.
//
// The invitation is a bearer capability, so it is deliberately narrow: one
// pairing, one use, short-lived, and held only in memory. See ADR 0006.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/shx-dow/lantern-go/internal/protocol"
)

// PairProtocolID is the stream protocol for redeeming a pairing invitation.
// Bump it for any wire-incompatible change; an older build refuses a version
// it does not know rather than guessing at the fields.
const PairProtocolID = "/lantern/pair/1.0.0"

// PairVersion is the invitation and stream protocol revision this build
// speaks.
const PairVersion = 1

// DefaultPairTTL is how long a freshly minted invitation stays valid.
const DefaultPairTTL = 10 * time.Minute

// MaxPairTTL bounds an invitation's life. "It expires" is part of what makes
// a leaked invitation survivable, so --ttl cannot make one permanent.
const MaxPairTTL = 24 * time.Hour

// pairSecretBytes is the entropy behind one invitation. 256 bits is far beyond
// guessing range, which is what lets the secret be the only thing a redeemer
// has to present.
const pairSecretBytes = 32

// invitationPrefix makes a pasted invitation recognisable, so a share code or a
// peer ID in the wrong place is refused with a sentence instead of a parse
// error.
const invitationPrefix = "lantern-pair1."

// PairError is a refusal a redeeming device can act on.
type PairError string

func (e PairError) Error() string { return string(e) }

const (
	// ErrPairExpired means the invitation was minted too long ago.
	ErrPairExpired PairError = "invitation expired; run \"lantern pair\" again for a new one"
	// ErrPairUsed means someone already redeemed it, or tried to.
	ErrPairUsed PairError = "invitation already used; run \"lantern pair\" again for a new one"
	// ErrPairUnknown means the inviter has no such invitation, usually because
	// its daemon restarted.
	ErrPairUnknown PairError = "invitation is not known to that device; run \"lantern pair\" there for a new one"
)

// PairRequest is sent by the redeeming device. Its peer ID is not a field: the
// inviter reads it from the connection, so a device cannot claim to be another.
type PairRequest struct {
	Version    int    `json:"version"`
	Secret     string `json:"secret"`
	DeviceName string `json:"device_name,omitempty"`
}

// PairResponse is the inviter's answer. On refusal Error is set and the other
// fields are empty.
type PairResponse struct {
	Version    int    `json:"version"`
	PeerID     string `json:"peer_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	Tier       string `json:"tier,omitempty"`
	Error      string `json:"error,omitempty"`
}

// PairResult is what a redeeming device learned: who it paired with, at what
// tier, and where that device was reached.
type PairResult struct {
	PeerID     string
	DeviceName string
	Tier       string
	Addrs      []string
}

// invitation is the decoded form of the string minted by MintInvitation.
type invitation struct {
	Version   int      `json:"version"`
	PeerID    string   `json:"peer_id"`
	Addrs     []string `json:"addrs,omitempty"`
	Secret    string   `json:"secret"`
	ExpiresAt int64    `json:"expires_at"`
}

// pairState is a Node's outstanding invitations and its pairing decision. It
// is a field on Node rather than package state so two nodes in one process
// (which every test has) never see each other's invitations.
type pairState struct {
	mu      sync.Mutex
	invites map[[32]byte]time.Time
	accept  func(remote peer.ID, peerName string) (deviceName, tier string, err error)
	once    sync.Once
}

// SetPairAcceptor installs the device's answer to a valid redemption. The
// callback records the pairing and returns this device's name and the tier the
// peer actually has. It is called only after the invitation's secret and
// expiry have been checked.
func (n *Node) SetPairAcceptor(fn func(remote peer.ID, peerName string) (deviceName, tier string, err error)) {
	n.pair.mu.Lock()
	n.pair.accept = fn
	n.pair.mu.Unlock()
}

// RegisterPairHandler installs the pairing stream handler (idempotent).
func (n *Node) RegisterPairHandler() {
	n.pair.once.Do(func() {
		n.Host.SetStreamHandler(PairProtocolID, n.servePair)
	})
}

// MintInvitation returns an invitation string and its expiry. ttl of zero
// means DefaultPairTTL; anything above MaxPairTTL is refused rather than
// clamped, so the caller never believes an invitation is shorter-lived than it
// is.
func (n *Node) MintInvitation(ttl time.Duration) (string, time.Time, error) {
	if n == nil || n.Host == nil {
		return "", time.Time{}, errors.New("pairing is not available on this node")
	}
	if ttl == 0 {
		ttl = DefaultPairTTL
	}
	if ttl < 0 {
		return "", time.Time{}, fmt.Errorf("invitation lifetime must be positive")
	}
	if ttl > MaxPairTTL {
		return "", time.Time{}, fmt.Errorf("invitation lifetime is at most %s", MaxPairTTL)
	}

	raw := make([]byte, pairSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate invitation secret: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(ttl).UTC()

	inv := invitation{
		Version:   PairVersion,
		PeerID:    n.Host.ID().String(),
		Addrs:     dialableAddrs(n.Host.Addrs()),
		Secret:    secret,
		ExpiresAt: expires.Unix(),
	}
	payload, err := json.Marshal(inv)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encode invitation: %w", err)
	}

	sum := sha256.Sum256([]byte(secret))
	n.pair.mu.Lock()
	if n.pair.invites == nil {
		n.pair.invites = make(map[[32]byte]time.Time)
	}
	n.pair.invites[sum] = expires
	// Expired invitations are unusable anyway; dropping them keeps a device
	// that mints many from holding them forever.
	for k, exp := range n.pair.invites {
		if time.Now().After(exp) {
			delete(n.pair.invites, k)
		}
	}
	n.pair.mu.Unlock()

	return invitationPrefix + base64.RawURLEncoding.EncodeToString(payload), expires, nil
}

// RedeemInvitation dials the inviter named by invitation and presents its
// secret. On success the inviter has recorded this device, and the result says
// who this device just paired with.
func (n *Node) RedeemInvitation(ctx context.Context, invitationText, deviceName string) (PairResult, error) {
	inv, err := parseInvitation(invitationText)
	if err != nil {
		return PairResult{}, err
	}
	if time.Now().After(time.Unix(inv.ExpiresAt, 0)) {
		return PairResult{}, ErrPairExpired
	}

	id, err := peer.Decode(inv.PeerID)
	if err != nil {
		return PairResult{}, fmt.Errorf("invitation names an invalid peer ID: %w", err)
	}
	pi := peer.AddrInfo{ID: id}
	for _, a := range inv.Addrs {
		if ma, err := multiaddr.NewMultiaddr(a); err == nil {
			pi.Addrs = append(pi.Addrs, ma)
		}
	}
	if len(pi.Addrs) == 0 {
		return PairResult{}, errors.New("invitation carries no address to dial; ask for a new one")
	}

	ctx, cancel := context.WithTimeout(ctx, fsiOTimeout)
	defer cancel()
	if err := n.Host.Connect(ctx, pi); err != nil {
		return PairResult{}, fmt.Errorf("reach the other device: %w", err)
	}
	raw, err := n.Host.NewStream(ctx, id, PairProtocolID)
	if err != nil {
		return PairResult{}, fmt.Errorf("open pairing stream: %w", err)
	}
	defer raw.Close()
	s := idleStream{Stream: raw, timeout: fsiOTimeout}

	req := PairRequest{Version: PairVersion, Secret: inv.Secret, DeviceName: strings.TrimSpace(deviceName)}
	if err := protocol.WriteMetadata(s, req); err != nil {
		return PairResult{}, fmt.Errorf("send invitation: %w", err)
	}
	var resp PairResponse
	if err := protocol.ReadMetadata(s, &resp); err != nil {
		return PairResult{}, fmt.Errorf("read pairing answer: %w", err)
	}
	if resp.Error != "" {
		return PairResult{}, errors.New(resp.Error)
	}
	if resp.PeerID != inv.PeerID {
		// The connection already authenticates the peer, so this can only mean
		// the invitation was rewritten. Refusing keeps a doctored invitation
		// from producing a pairing with a device the operator did not name.
		return PairResult{}, fmt.Errorf("pairing answer came from %s, not the device the invitation named", shortPairPeer(resp.PeerID))
	}
	return PairResult{PeerID: resp.PeerID, DeviceName: resp.DeviceName, Tier: resp.Tier, Addrs: inv.Addrs}, nil
}

func (n *Node) servePair(raw network.Stream) {
	defer raw.Close()
	s := idleStream{Stream: raw, timeout: fsiOTimeout}

	var req PairRequest
	if err := protocol.ReadMetadata(s, &req); err != nil {
		_ = protocol.WriteMetadata(s, PairResponse{Error: "bad pairing request"})
		return
	}
	if req.Version != PairVersion {
		_ = protocol.WriteMetadata(s, PairResponse{Error: fmt.Sprintf("pairing protocol version %d, want %d", req.Version, PairVersion)})
		return
	}

	// Consume first, then decide. An attempt that reaches this device spends
	// the invitation whether or not the pairing is made, so a leaked string
	// cannot be replayed after the intended device has used it.
	if err := n.consumeInvitation(req.Secret); err != nil {
		_ = protocol.WriteMetadata(s, PairResponse{Error: err.Error()})
		return
	}

	n.pair.mu.Lock()
	accept := n.pair.accept
	n.pair.mu.Unlock()
	if accept == nil {
		_ = protocol.WriteMetadata(s, PairResponse{Error: "pairing is not configured on this device"})
		return
	}

	remote := raw.Conn().RemotePeer()
	name, tier, err := accept(remote, req.DeviceName)
	if err != nil {
		_ = protocol.WriteMetadata(s, PairResponse{Error: err.Error()})
		return
	}
	_ = protocol.WriteMetadata(s, PairResponse{
		Version:    PairVersion,
		PeerID:     n.Host.ID().String(),
		DeviceName: name,
		Tier:       tier,
	})
}

// consumeInvitation burns secret and reports why it could not be used. The
// lookup is by hash so the stored map never holds a working secret.
func (n *Node) consumeInvitation(secret string) error {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ErrPairUnknown
	}
	sum := sha256.Sum256([]byte(secret))

	n.pair.mu.Lock()
	defer n.pair.mu.Unlock()
	exp, ok := n.pair.invites[sum]
	if ok {
		delete(n.pair.invites, sum)
	}
	if !ok {
		return ErrPairUnknown
	}
	if time.Now().After(exp) {
		return ErrPairExpired
	}
	return nil
}

// parseInvitation accepts only the exact shape this build minted. A share
// code, a peer ID, or a truncated paste is named as such rather than failing
// somewhere inside the dial.
func parseInvitation(text string) (invitation, error) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, invitationPrefix) {
		return invitation{}, fmt.Errorf("that is not a pairing invitation (it starts with %q)", invitationPrefix)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(trimmed, invitationPrefix))
	if err != nil {
		return invitation{}, fmt.Errorf("malformed invitation: %w", err)
	}
	var inv invitation
	if err := json.Unmarshal(payload, &inv); err != nil {
		return invitation{}, fmt.Errorf("malformed invitation: %w", err)
	}
	if inv.Version != PairVersion {
		return invitation{}, fmt.Errorf("invitation version %d, want %d; upgrade both devices", inv.Version, PairVersion)
	}
	if strings.TrimSpace(inv.Secret) == "" || strings.TrimSpace(inv.PeerID) == "" {
		return invitation{}, errors.New("malformed invitation: missing peer ID or secret")
	}
	return inv, nil
}

// dialableAddrs drops loopback addresses. They are meaningful on this machine
// only, and an invitation is read on another one. A node that has nothing but
// loopback addresses -- a container, a test -- keeps them, because an
// invitation with no address cannot be redeemed at all.
func dialableAddrs(addrs []multiaddr.Multiaddr) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if isLoopbackMultiaddr(a) {
			continue
		}
		out = append(out, a.String())
	}
	if len(out) == 0 {
		for _, a := range addrs {
			out = append(out, a.String())
		}
	}
	return out
}

func isLoopbackMultiaddr(a multiaddr.Multiaddr) bool {
	for _, proto := range []int{multiaddr.P_IP4, multiaddr.P_IP6} {
		v, err := a.ValueForProtocol(proto)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(v); ip != nil && ip.IsLoopback() {
			return true
		}
	}
	return false
}

// shortPairPeer trims a peer ID for an error message. A full ID is 52
// characters and hides the sentence around it.
func shortPairPeer(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "..."
}
