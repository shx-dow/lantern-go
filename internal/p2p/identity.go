package p2p

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/shx-dow/lantern-go/internal/storage"
)

// identityFileName is the persisted libp2p private key for stable peer IDs
// across restarts. The file holds the marshalled private key bytes with
// owner-only permissions.
const identityFileName = "lantern-identity.key"

// LoadOrCreatePrivKey returns the stable private key in dir, generating and
// persisting a fresh Ed25519 key when none exists.
func LoadOrCreatePrivKey(dir string) (crypto.PrivKey, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, storage.PrivateDirPerm); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	path := filepath.Join(dir, identityFileName)
	if raw, err := os.ReadFile(path); err == nil {
		key, err := crypto.UnmarshalPrivateKey(raw)
		if err == nil {
			return key, nil
		}
		// Corrupt key: fall through and replace it so the node can boot.
		_ = os.Remove(path)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read identity: %w", err)
	}
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate identity: %w", err)
	}
	raw, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal identity: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".lantern-identity-*")
	if err != nil {
		return nil, fmt.Errorf("create identity: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(storage.PrivateFilePerm); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("protect identity: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write identity: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close identity: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("publish identity: %w", err)
	}
	return priv, nil
}
