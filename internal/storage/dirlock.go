package storage

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// LockFileName is the advisory lock every daemon takes on its state
// directory before touching the identity.
const LockFileName = ".lantern.lock"

// ErrDirLocked is returned by AcquireDirLock when another process already
// holds the directory's lock.
var ErrDirLocked = errors.New("state directory is locked by another lantern process")

// AcquireDirLock takes an exclusive advisory lock on a state directory and
// returns the unlock function.
//
// A lantern identity lives in exactly one directory: the key, the token, and
// the trust store are all read and written there. Two daemons pointed at the
// same directory do not merge into one device; they collide -- same peer ID,
// so each dials itself, and their trust-store writes interleave unsynchronised
// on disk. The lock turns that from a confusing runtime failure into a
// refusal, and it is why the refusal is safe to ignore OnError only for
// callers that already hold the lock.
//
// The lock is advisory: it binds processes that all take it, and every
// lantern daemon does.
func AcquireDirLock(dir string) (release func() error, err error) {
	if err := os.MkdirAll(dir, PrivateDirPerm); err != nil {
		return nil, err
	}
	fl := flock.New(filepath.Join(dir, LockFileName))
	locked, err := fl.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ErrDirLocked
	}
	return fl.Unlock, nil
}
