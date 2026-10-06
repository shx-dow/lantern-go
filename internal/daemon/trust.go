package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multiaddr/net"

	"github.com/shx-dow/lantern-go/internal/paths"
	"github.com/shx-dow/lantern-go/internal/storage"
)

// AccessTier is what one paired device is allowed to do on this device.
//
// Tiers replace the old "pairing implies everything" model. A pairing is a
// long-lived, one-directional grant, so what it actually confers has to be
// chosen deliberately rather than inherited from the fact that pairing exists.
//
// The tiers are ordered, and a tier includes everything below it.
type AccessTier string

const (
	// TierNone grants nothing: the peer stays paired, so it is not an error,
	// but it can neither read nor write. Useful for keeping a pairing without
	// the standing capability.
	TierNone AccessTier = "none"
	// TierRead reads inside this device's shared dirs. This is the default for
	// a newly paired device, so pairing alone never confers a write.
	TierRead AccessTier = "read"
	// TierReadWrite adds writes inside this device's writable roots.
	TierReadWrite AccessTier = "read-write"
)

// DefaultTier is what a newly paired device gets. It is deliberately
// read-only: pairing a device should never hand it the ability to change this
// one without a second, explicit decision.
const DefaultTier = TierRead

// AllTiers lists every tier, for help text and validation errors.
var AllTiers = []AccessTier{TierNone, TierRead, TierReadWrite}

// ParseTier reads a tier name. An empty string means DefaultTier, which is how
// entries written before tiers existed get a defined meaning rather than
// failing to load.
func ParseTier(v string) (AccessTier, error) {
	t := AccessTier(strings.ToLower(strings.TrimSpace(v)))
	if t == "" {
		return DefaultTier, nil
	}
	for _, known := range AllTiers {
		if t == known {
			return t, nil
		}
	}
	names := make([]string, 0, len(AllTiers))
	for _, known := range AllTiers {
		names = append(names, string(known))
	}
	return "", fmt.Errorf("unknown access tier %q (want one of: %s)", v, strings.Join(names, ", "))
}

// CanRead reports whether this tier may read inside the shared dirs.
func (t AccessTier) CanRead() bool { return t == TierRead || t == TierReadWrite }

// CanWrite reports whether this tier may write.
func (t AccessTier) CanWrite() bool { return t == TierReadWrite }

// AtLeast reports whether t is at least as capable as other.
func (t AccessTier) AtLeast(other AccessTier) bool { return t.rank() >= other.rank() }

func (t AccessTier) rank() int {
	switch t {
	case TierReadWrite:
		return 2
	case TierRead:
		return 1
	default:
		return 0
	}
}

// TrustEntry is one paired device: its stable libp2p peer ID, a human alias,
// and what it is allowed to do.
//
// Tier is the standing capability the pairing confers. WritableRoots, when set,
// narrows where that device may write to a subset of this device's writable
// roots; it can never widen them, because --writable-dirs is the operator's
// hard bound and per-peer configuration only restricts further.
//
// Addrs are the last addresses this device was seen at, with SeenAt saying
// when. They are a cache, not a promise: they let a restart reach a paired
// device immediately instead of waiting for the network to announce it
// again. Anything recorded here may be stale, so a dial is still expected
// to fail and be retried by discovery.
type TrustEntry struct {
	PeerID        string     `json:"peer_id"`
	Alias         string     `json:"alias,omitempty"`
	AddedAt       string     `json:"added_at"`
	Tier          AccessTier `json:"tier,omitempty"`
	WritableRoots []string   `json:"writable_roots,omitempty"`
	Addrs         []string   `json:"addrs,omitempty"`
	SeenAt        string     `json:"seen_at,omitempty"`
}

// WriteScope reports where this paired device may write: the effective roots,
// which are its own subset if it has one, otherwise the device's own writable
// roots.
func (e TrustEntry) WriteScope(deviceRoots []string) []string {
	if len(e.WritableRoots) > 0 {
		return e.WritableRoots
	}
	return deviceRoots
}

// MaxTrackedAddrs bounds how many addresses one peer keeps. A device on a
// normal network offers a handful; the bound stops a peer that advertises
// many (or changes them often) from growing the trust file without limit.
const MaxTrackedAddrs = 8

// UpdateAddrs records where a peer was last seen. It never fails a
// transfer: a peerstore problem is not worth interrupting a request for,
// so a save failure is returned but callers may ignore it. Addresses that
// are loopback are dropped, since they are meaningless on another machine.
func (s *TrustStore) UpdateAddrs(peerID string, addrs []string) error {
	clean := usableAddrs(addrs)
	if len(clean) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[peerID]
	if !ok {
		// Not a paired device; do not start tracking strangers.
		return nil
	}
	merged := make([]string, 0, MaxTrackedAddrs)
	// Filter the combined list, not just the new addresses, so an entry
	// stored before this rule existed is cleaned up on the next update.
	for _, a := range usableAddrs(append(append([]string(nil), e.Addrs...), clean...)) {
		if len(merged) >= MaxTrackedAddrs {
			break
		}
		if !contains(merged, a) {
			merged = append(merged, a)
		}
	}
	e.Addrs = merged
	e.SeenAt = time.Now().UTC().Format(time.RFC3339)
	s.byID[peerID] = e
	return s.saveLocked()
}

// usableAddrs keeps addresses worth dialling on another machine.
func usableAddrs(addrs []string) []string {
	var out []string
	for _, a := range addrs {
		if a == "" {
			continue
		}
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			continue
		}
		// Loopback and unspecified addresses never help across machines.
		if ip, err := manet.ToIP(ma); err == nil && (ip.IsLoopback() || ip.IsUnspecified()) {
			continue
		}
		out = append(out, a)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TrustStore persists paired devices in <dataDir>/trusted_peers.json.
type TrustStore struct {
	mu   sync.Mutex
	path string
	byID map[string]TrustEntry
}

// NewTrustStore loads path (missing file = empty store).
func NewTrustStore(dataDir string) (*TrustStore, error) {
	if dataDir == "" {
		// Pairing records must survive a reboot; see paths.Data.
		dataDir = paths.Data()
	}
	if err := os.MkdirAll(dataDir, storage.PrivateDirPerm); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	s := &TrustStore{path: filepath.Join(dataDir, "trusted_peers.json"), byID: make(map[string]TrustEntry)}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read trust store: %w", err)
	}
	var list []TrustEntry
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse trust store: %w", err)
	}
	for _, e := range list {
		if e.PeerID != "" {
			// An entry written before tiers existed has no tier. Default it
			// rather than rejecting the file, so an upgrade does not lose
			// pairings; the effective tier becomes the safe default.
			if e.Tier == "" {
				e.Tier = DefaultTier
			}
			s.byID[e.PeerID] = e
		}
	}
	return s, nil
}

func (s *TrustStore) saveLocked() error {
	list := make([]TrustEntry, 0, len(s.byID))
	for _, e := range s.byID {
		list = append(list, e)
	}
	// A stable order keeps the file readable and its diffs meaningful.
	sort.Slice(list, func(i, j int) bool { return list[i].PeerID < list[j].PeerID })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".trusted-peers-*")
	if err != nil {
		return fmt.Errorf("create trust store: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(storage.PrivateFilePerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect trust store: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write trust store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close trust store: %w", err)
	}
	return os.Rename(tmpPath, s.path)
}

// TrustSpec describes a pairing: who, what they are called, what they may do,
// and where they may write.
type TrustSpec struct {
	PeerID        string
	Alias         string
	Tier          string
	WritableRoots []string
	// DeviceRoots is this device's own --writable-dirs. Per-peer roots are
	// checked against it, so a peer can only ever be narrowed.
	DeviceRoots []string
}

// Add pairs peerID, validating the tier and confining the writable roots.
func (s *TrustStore) Add(spec TrustSpec) (TrustEntry, error) {
	peerID := strings.TrimSpace(spec.PeerID)
	if peerID == "" {
		return TrustEntry{}, fmt.Errorf("peer_id must not be empty")
	}
	tier, err := ParseTier(spec.Tier)
	if err != nil {
		return TrustEntry{}, err
	}
	roots, err := confineRoots(spec.WritableRoots, spec.DeviceRoots)
	if err != nil {
		return TrustEntry{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	e := TrustEntry{
		PeerID:        peerID,
		Alias:         strings.TrimSpace(spec.Alias),
		AddedAt:       now,
		Tier:          tier,
		WritableRoots: roots,
	}
	// Re-pairing an existing device keeps its added date and cached addresses,
	// so granting a tier back does not read as a brand new pairing.
	if prev, ok := s.byID[peerID]; ok {
		e.AddedAt = prev.AddedAt
		e.Addrs = prev.Addrs
		e.SeenAt = prev.SeenAt
	}
	s.byID[peerID] = e
	return e, s.saveLocked()
}

// SetTier changes one paired device's tier, leaving everything else alone.
func (s *TrustStore) SetTier(peerID, tier string) (TrustEntry, error) {
	t, err := ParseTier(tier)
	if err != nil {
		return TrustEntry{}, err
	}
	peerID = peerIDOf(peerID)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[peerID]
	if !ok {
		return TrustEntry{}, fmt.Errorf("peer not found")
	}
	e.Tier = t
	s.byID[peerID] = e
	return e, s.saveLocked()
}

// SetWritableRoots confines one paired device to a subset of this device's
// writable roots. Passing none clears the restriction, returning the device to
// its full writable roots.
func (s *TrustStore) SetWritableRoots(peerID string, roots, deviceRoots []string) (TrustEntry, error) {
	confined, err := confineRoots(roots, deviceRoots)
	if err != nil {
		return TrustEntry{}, err
	}
	peerID = peerIDOf(peerID)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[peerID]
	if !ok {
		return TrustEntry{}, fmt.Errorf("peer not found")
	}
	e.WritableRoots = confined
	s.byID[peerID] = e
	return e, s.saveLocked()
}

// confineRoots keeps only roots that sit inside one of the device's own
// writable roots.
//
// Per-peer configuration may narrow the device's writable set and never
// widen it. That direction is what makes it safe to let a pairing record carry
// paths at all: a peer cannot name its way past --writable-dirs, even if the
// record is edited by hand or synced in from another machine.
func confineRoots(wanted, deviceRoots []string) ([]string, error) {
	wanted = splitNonEmpty(wanted)
	if len(wanted) == 0 {
		return nil, nil
	}
	if len(deviceRoots) == 0 {
		return nil, fmt.Errorf("this device has no writable roots, so no per-peer writable root can be inside them")
	}
	resolved := make([]string, 0, len(deviceRoots))
	for _, r := range deviceRoots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("resolve writable root: %w", err)
		}
		resolved = append(resolved, abs)
	}
	var kept []string
	for _, w := range wanted {
		abs, err := filepath.Abs(w)
		if err != nil {
			return nil, fmt.Errorf("resolve writable root: %w", err)
		}
		ok := false
		for _, r := range resolved {
			if pathInside(abs, r) {
				ok = true
				kept = append(kept, abs)
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("writable root %q is outside this device's writable dirs (%s); a peer can only be narrowed, never widened",
				abs, strings.Join(resolved, ", "))
		}
	}
	return kept, nil
}

// pathInside reports whether path is root or sits beneath it. Per-peer roots
// are compared the same way the fs resolver compares destinations, so a peer
// cannot use a shared prefix to escape: /srv/inbox-evil does not sit inside
// /srv/inbox.
func pathInside(path, root string) bool {
	if path == root {
		return true
	}
	sep := string(os.PathSeparator)
	if strings.HasSuffix(root, sep) {
		return len(path) > len(root) && strings.EqualFold(path[:len(root)], root)
	}
	if len(path) <= len(root)+len(sep) {
		return false
	}
	return strings.EqualFold(path[:len(root)+len(sep)], root+sep)
}

func splitNonEmpty(in []string) []string {
	var out []string
	for _, v := range in {
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

// Remove unpairs peerID; false when unknown.
func (s *TrustStore) Remove(peerID string) bool {
	peerID = peerIDOf(peerID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[peerID]; !ok {
		return false
	}
	delete(s.byID, peerID)
	_ = s.saveLocked()
	return true
}

// List returns paired devices.
func (s *TrustStore) List() []TrustEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TrustEntry, 0, len(s.byID))
	for _, e := range s.byID {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PeerID < out[j].PeerID })
	return out
}

// Get returns one paired device's entry.
func (s *TrustStore) Get(peerID string) (TrustEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[peerIDOf(peerID)]
	return e, ok
}

// Tier returns one paired device's tier. An unknown peer reads as TierNone, so
// an unpaired device is refused rather than defaulting to some capability.
func (s *TrustStore) Tier(peerID string) AccessTier {
	e, ok := s.Get(peerID)
	if !ok {
		return TierNone
	}
	if e.Tier == "" {
		return DefaultTier
	}
	return e.Tier
}

// CanRead reports whether a peer may read inside the shared dirs.
func (s *TrustStore) CanRead(peerID string) bool { return s.Tier(peerID).CanRead() }

// CanWrite reports whether a peer is at a tier that permits writes. It says
// nothing about whether this device allows writes at all: that is the daemon's
// own WritesEnabled gate, and both must be true for a write to land.
func (s *TrustStore) CanWrite(peerID string) bool { return s.Tier(peerID).CanWrite() }

// Trusted reports whether peerID is paired. It is the pairing gate, not an
// access grant: a paired device at TierNone is still paired.
func (s *TrustStore) Trusted(peerID string) bool {
	_, ok := s.Get(peerID)
	return ok
}
