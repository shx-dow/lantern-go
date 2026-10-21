package storage

import (
	"errors"
	"testing"
)

// A second daemon on the same directory must be refused, and freeing the dir
// must make it acquirable again. Controls the wiring in daemonapp, where the
// refusal is what stops two processes sharing one identity.
func TestAcquireDirLock(t *testing.T) {
	dir := t.TempDir()

	release, err := AcquireDirLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	_, err = AcquireDirLock(dir)
	if !errors.Is(err, ErrDirLocked) {
		t.Fatalf("second acquire: got %v, want ErrDirLocked", err)
	}

	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	release2, err := AcquireDirLock(dir)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	_ = release2()
}
