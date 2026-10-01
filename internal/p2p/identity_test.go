package p2p

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shx-dow/lantern-go/internal/paths"
)

func TestIdentityPersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreatePrivKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreatePrivKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Equals(b) {
		t.Fatal("same dir returned different keys")
	}
	other, err := LoadOrCreatePrivKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a.Equals(other) {
		t.Fatal("different dirs returned identical keys")
	}
}

func TestNodesSharePeerIDWithSameKey(t *testing.T) {
	key, err := LoadOrCreatePrivKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewNode(0, []string{"none"}, key, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewNode(0, []string{"none"}, key, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.Host.ID() != b.Host.ID() {
		t.Fatalf("peer IDs differ: %s vs %s", a.Host.ID(), b.Host.ID())
	}
}

func TestNilKeyGivesEphemeralIdentity(t *testing.T) {
	a, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.Host.ID() == b.Host.ID() {
		t.Fatal("ephemeral nodes share a peer ID")
	}
}

// The regression that started this: with no data directory given, the identity
// key must still land somewhere that survives a reboot. A temporary directory
// means a new peer ID on every boot and every pairing silently invalidated.
func TestIdentityDefaultDirIsPersistent(t *testing.T) {
	// Point the default at a scratch directory rather than writing an
	// identity key into the developer's real home directory.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "data"))

	k1, err := LoadOrCreatePrivKey("")
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreatePrivKey("")
	if err != nil {
		t.Fatal(err)
	}
	if !k1.Equals(k2) {
		t.Fatal("two calls with the default directory produced different keys")
	}

	// The key must be readable back from the default location.
	again, err := LoadOrCreatePrivKey("")
	if err != nil {
		t.Fatal(err)
	}
	if !again.Equals(k1) {
		t.Fatal("the default identity did not persist between calls")
	}

	path := filepath.Join(paths.Data(), identityFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("identity key is not at the persistent location %q: %v", path, err)
	}
}

func TestIdentityKeyIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreatePrivKey(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, identityFileName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0077 != 0 {
		t.Errorf("identity key permissions are %v, want owner-only", fi.Mode().Perm())
	}
}
