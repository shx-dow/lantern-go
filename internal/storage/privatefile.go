package storage

import (
	"fmt"
	"os"
	"path/filepath"
)

// WritePrivateFile writes data to path with owner-only permissions, atomically.
//
// A reader sees either the previous file or the complete new one, never a
// partial write: the bytes go to a temporary file in the same directory, are
// flushed, and only then are renamed onto path. The flush matters for the
// identity key and the trust store, where a power loss that published an empty
// file would look like a damaged identity rather than a missing one.
func WritePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(PrivateFilePerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("flush temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return nil
}
