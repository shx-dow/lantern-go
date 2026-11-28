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

// LoadOrCreatePrivKey returns the stable private key in dir, generating and
// persisting a fresh Ed25519 key when none exists.
//
// The key lives at paths.IdentityKeyFileName and holds the marshalled private
// key bytes with owner-only permissions. It is the file an identity backup
// carries, so its name is shared rather than repeated here.
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
	path := filepath.Join(dir, paths.IdentityKeyFileName)
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
	// WritePrivateFile flushes the bytes before the rename publishes the name.
	// Without that, a power loss can leave the new identity reachable but
	// empty, which is the corruption this file refuses to paper over.
	if err := storage.WritePrivateFile(path, raw); err != nil {
		return nil, fmt.Errorf("write identity: %w", err)
	}
	return priv, nil
}
