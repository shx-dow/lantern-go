package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multiaddr/net"

	"github.com/shx-dow/lantern-go/internal/storage"
)

// TrustEntry is one paired device: its stable libp2p peer ID plus a human
// alias. The peer ID survives restarts via the persisted identity key.
//
// Addrs are the last addresses this device was seen at, with SeenAt saying
// when. They are a cache, not a promise: they let a restart reach a paired
// device immediately instead of waiting for the network to announce it
// again. Anything recorded here may be stale, so a dial is still expected
// to fail and be retried by discovery.
type TrustEntry struct {
	PeerID  string   `json:"peer_id"`
	Alias   string   `json:"alias,omitempty"`
	AddedAt string   `json:"added_at"`
	Addrs   []string `json:"addrs,omitempty"`
	SeenAt  string   `json:"seen_at,omitempty"`
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
		dataDir = os.TempDir()
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

// Add pairs peerID with an optional alias.
func (s *TrustStore) Add(peerID, alias string) (TrustEntry, error) {
	if peerID == "" {
		return TrustEntry{}, fmt.Errorf("peer_id must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := TrustEntry{PeerID: peerID, Alias: alias, AddedAt: time.Now().UTC().Format(time.RFC3339)}
	s.byID[peerID] = e
	return e, s.saveLocked()
}

// Remove unpairs peerID; false when unknown.
func (s *TrustStore) Remove(peerID string) bool {
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
	return out
}

// Trusted reports whether peerID is paired.
func (s *TrustStore) Trusted(peerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byID[peerID]
	return ok
}
