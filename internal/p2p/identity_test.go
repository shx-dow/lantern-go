package p2p

import (
	"os"
	"path/filepath"
	"strings"
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

// A file that exists but cannot be parsed must not be replaced. Replacing it
// mints a new peer ID, and every pairing on every other device silently stops
// recognising this one. The damaged file is also the only thing an operator
// could restore, so it must survive the failed load unchanged.
func TestIdentityRefusesToReplaceCorruptKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"garbage", "this is not a marshalled private key"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := LoadOrCreatePrivKey(dir); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, identityFileName)
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}

			_, err := LoadOrCreatePrivKey(dir)
			if err == nil {
				t.Fatal("a corrupt identity was silently replaced with a new one")
			}
			if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "refusing to replace") {
				t.Fatalf("error does not name the file and the refusal: %v", err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("the damaged identity must be left in place: %v", readErr)
			}
			if string(got) != tc.body {
				t.Fatalf("the damaged identity was changed from %q to %q", tc.body, got)
			}
		})
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
	posixOnly(t)

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
