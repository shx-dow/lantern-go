package p2p

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/shx-dow/lantern-go/internal/paths"
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
		// Never fall back to a temporary directory: the identity key is the
		// device's only identity, and a reboot that clears it would silently
		// invalidate every pairing this device has.
		dir = paths.Data()
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
		// The file exists but cannot be read as a key. Replacing it would mint
		// a new peer ID, which every device paired with this one would see as a
		// stranger. Refuse instead, and keep the damaged file where an operator
		// can inspect or restore it.
		return nil, fmt.Errorf("identity key %s is unreadable: %w\n"+
			"refusing to replace it: every pairing is tied to this identity\n"+
			"restore the file from a backup, or move it aside and restart to create a new identity and pair again", path, err)
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
	// Flush the bytes before the rename publishes the name. Without this, a
	// power loss can leave the new identity reachable but empty, which is the
	// corruption this file now refuses to paper over.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("sync identity: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close identity: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("publish identity: %w", err)
	}
	return priv, nil
}
