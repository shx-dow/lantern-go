package daemon

import (
	"fmt"
	"strings"

	"github.com/shx-dow/lantern-go/internal/p2p"
)

// Per-peer access policy.
//
// The daemon owns this decision because it owns both halves of it: the trust
// store says what a paired device is allowed, and the daemon says what this
// device offers. The transport asks, per write.

// WriteScopeFor reports the effective writable roots for one paired device: its
// own narrowed set if it has one, otherwise this device's writable roots.
func (d *Daemon) WriteScopeFor(peerID string) []string {
	if d.Trust == nil {
		return nil
	}
	e, ok := d.Trust.Get(peerID)
	if !ok {
		return nil
	}
	return e.WriteScope(d.WritableRoots)
}

// WritePolicyFor resolves what one peer may write on this device. It is the
// per-peer decision the fs handler consults before it reads a single byte of
// content, and it is deliberately conservative at every step:
//
//   - an unpaired device is refused, because pairing is what confers access
//   - a device started without WritesEnabled is refused, and says so in the
//     words agents already match on
//   - a paired device below TierReadWrite is refused, and the message names the
//     tier and the command that would change it
//   - the returned roots are the peer's own, already confined to this device's
//     writable dirs when they were stored
//
// Returning an error refuses. Returning nil refuses too, so a caller cannot
// accidentally treat "no opinion" as permission.
func (d *Daemon) WritePolicyFor(peerID string) (*p2p.WritePolicy, error) {
	if d.Trust == nil {
		return nil, fmt.Errorf("pairing is not configured on this device")
	}
	entry, ok := d.Trust.Get(peerID)
	if !ok {
		return nil, fmt.Errorf("not paired with %s", shortPeer(peerID))
	}
	if !d.WritesEnabled {
		// This wording is what agents and the audit tests match on.
		return nil, p2p.ErrWritesNotPermitted
	}
	tier := entry.Tier
	if tier == "" {
		tier = DefaultTier
	}
	if !tier.CanWrite() {
		return nil, fmt.Errorf("this device is paired at tier %q, which does not allow writes (it must be %q)", tier, TierReadWrite)
	}
	roots := entry.WriteScope(d.WritableRoots)
	if len(roots) == 0 {
		return nil, fmt.Errorf("no writable roots are configured on this device")
	}
	max := d.MaxWriteBytes
	if max <= 0 {
		max = p2p.DefaultMaxWriteBytes
	}
	return &p2p.WritePolicy{Mode: p2p.WriteSharedRoots, Roots: roots, MaxBytes: max}, nil
}

// ReadableRootsFor reports the shared roots one peer may read, or nil if it may
// not read at all. The fs handler treats an empty result as a refusal, so this
// is the single place a read tier is enforced.
func (d *Daemon) ReadableRootsFor(peerID string) []string {
	if d.Trust == nil || !d.Trust.CanRead(peerID) {
		return nil
	}
	return d.SharedDirs
}

// shortPeer trims a peer ID for a refusal message. A full ID is 52 characters,
// which makes the message unreadable in a log.
func shortPeer(peerID string) string {
	id := strings.TrimSpace(peerID)
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "..."
}
