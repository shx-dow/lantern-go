package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shx-dow/lantern-go/internal/storage"
)

// TrustEntry is one paired device: its stable libp2p peer ID plus a human
// alias. The peer ID survives restarts via the persisted identity key.
type TrustEntry struct {
	PeerID  string `json:"peer_id"`
	Alias   string `json:"alias,omitempty"`
	AddedAt string `json:"added_at"`
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
