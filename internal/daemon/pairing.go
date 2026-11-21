package daemon

// Pairing by invitation, from the daemon's side: the decision to accept a
// redeemed invitation, and the two local API routes that mint and redeem one.
// The wire protocol lives in internal/p2p. See ADR 0006.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/p2p"
)

// PairingName is set on the Daemon by the app wiring; it is the device name
// this daemon reports to a peer that redeems an invitation. Empty means the
// peer falls back to a shortened peer ID.

// AcceptPairing records a peer that presented a valid invitation and answers
// with this device's name and the tier that peer now has.
//
// A new pairing is `read`, the same default as `trust add`. An existing pairing
// is left exactly as it is, so redeeming an invitation never demotes a device
// that was granted read-write, and never renames one the operator named.
func (d *Daemon) AcceptPairing(remote peer.ID, peerName string) (string, string, error) {
	if d.Trust == nil {
		return "", "", fmt.Errorf("pairing is not configured on this device")
	}
	name := strings.TrimSpace(d.PairingName)

	pid := remote.String()
	if prev, ok := d.Trust.Get(pid); ok {
		tier := prev.Tier
		if tier == "" {
			tier = DefaultTier
		}
		return name, string(tier), nil
	}
	entry, err := d.Trust.Add(TrustSpec{
		PeerID:      pid,
		Alias:       pairingAlias(peerName, pid),
		Tier:        string(DefaultTier),
		DeviceRoots: d.WritableRoots,
	})
	if err != nil {
		return "", "", fmt.Errorf("record the pairing: %w", err)
	}
	return name, string(entry.Tier), nil
}

// pairingAlias names a peer the operator did not name by hand. The device's own
// name is what the other side calls itself, so it is used when it is usable;
// otherwise a short peer ID is still something to type.
func pairingAlias(deviceName, peerID string) string {
	name := strings.TrimSpace(deviceName)
	var b strings.Builder
	for _, r := range name {
		// A name travels from another device: keep it one printable line, so
		// it cannot rewrite a terminal or a listing.
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	name = strings.TrimSpace(b.String())
	if name == "" {
		return shortPeer(peerID)
	}
	if runes := []rune(name); len(runes) > 64 {
		// Aliases are typed by a person; a wall of text is not one. Counted in
		// runes, so a name in any script is cut between characters rather than
		// inside one.
		name = strings.TrimSpace(string(runes[:64]))
	}
	return name
}

// Pairing routes.

type pairInvitationRequest struct {
	TTLSeconds int64 `json:"ttl_seconds,omitempty"`
}

type pairInvitationResponse struct {
	Invitation string `json:"invitation"`
	ExpiresAt  string `json:"expires_at"`
}

type pairRedeemRequest struct {
	Invitation string `json:"invitation"`
}

type pairRedeemResponse struct {
	PeerID     string `json:"peer_id"`
	DeviceName string `json:"device_name"`
	Tier       string `json:"tier"`
}

func (h *Handler) postPairingInvitations(w http.ResponseWriter, r *http.Request) {
	if _, err := h.trustStore(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	node := h.node()
	if node == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("pairing is not available on this daemon"))
		return
	}

	var req pairInvitationRequest
	if err := readJSONBody(w, r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if req.TTLSeconds < 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("ttl_seconds must not be negative"))
		return
	}
	text, expires, err := node.MintInvitation(time.Duration(req.TTLSeconds) * time.Second)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, pairInvitationResponse{
		Invitation: text,
		ExpiresAt:  expires.Format(time.RFC3339),
	})
}

func (h *Handler) postPairingRedeem(w http.ResponseWriter, r *http.Request) {
	// The local store is checked before dialing: a successful redeem records
	// the pairing on the inviter, and discovering afterwards that this device
	// cannot record its half would leave the two sides disagreeing.
	store, err := h.trustStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	node := h.node()
	if node == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("pairing is not available on this daemon"))
		return
	}

	var req pairRedeemRequest
	if err := readJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if strings.TrimSpace(req.Invitation) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invitation must not be empty"))
		return
	}

	res, err := node.RedeemInvitation(r.Context(), req.Invitation, h.deviceName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// The other device already recorded this one; record it back so the
	// pairing is mutual. A device that was already paired keeps its tier and
	// name, for the same reason AcceptPairing does not overwrite them.
	if prev, ok := store.Get(res.PeerID); !ok {
		if _, err := store.Add(TrustSpec{
			PeerID:      res.PeerID,
			Alias:       pairingAlias(res.DeviceName, res.PeerID),
			Tier:        string(DefaultTier),
			DeviceRoots: h.daemon.WritableRoots,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("the other device paired with you, but recording it here failed: %w", err))
			return
		}
	} else if prev.Alias == "" {
		_, _ = store.SetAlias(res.PeerID, pairingAlias(res.DeviceName, res.PeerID))
	}
	if len(res.Addrs) > 0 {
		// The invitation named where the inviter was reachable, which is the
		// freshest address this device has. A failure here only costs a
		// discovery round trip later, so it is not reported as a pairing
		// failure.
		_ = store.UpdateAddrs(res.PeerID, res.Addrs)
	}

	writeJSON(w, http.StatusOK, pairRedeemResponse{
		PeerID:     res.PeerID,
		DeviceName: res.DeviceName,
		Tier:       res.Tier,
	})
}

// node returns the transport this daemon serves, or nil when it has none. The
// pairing routes need it; the rest of the API does not.
func (h *Handler) node() *p2p.Node {
	if h.daemon == nil || h.daemon.ln == nil {
		return nil
	}
	return h.daemon.ln.Node()
}
