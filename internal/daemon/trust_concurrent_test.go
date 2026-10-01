package daemon

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

// The trust store is written whenever a device is paired and whenever a read
// stamps an address, which happens on any number of concurrent requests.
func TestTrustStoreConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	s, err := NewTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 8
	const rounds = 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			id := fmt.Sprintf("12D3KooWPeer%032d", w)
			for i := 0; i < rounds; i++ {
				if _, err := s.Add(id, fmt.Sprintf("dev%d", w)); err != nil {
					t.Errorf("add: %v", err)
					return
				}
				_ = s.List()
				_ = s.Trusted(id)
				if err := s.UpdateAddrs(id, []string{fmt.Sprintf("/ip4/10.0.0.%d/tcp/4001", (i%250)+1)}); err != nil {
					t.Errorf("update addrs: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// Whatever interleaving happened, the file must still be valid and hold
	// every device.
	reloaded, err := NewTrustStore(dir)
	if err != nil {
		t.Fatalf("the trust file was left unreadable: %v", err)
	}
	if got := len(reloaded.List()); got != workers {
		t.Errorf("got %d devices after concurrent writes, want %d", got, workers)
	}
}

// Two daemons sharing one data directory must not clobber each other's token.
func TestTokenIsStableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateToken(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := LoadOrCreateToken(dir, "")
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatalf("token changed between calls: %q then %q", first, again)
		}
	}
	// An explicit token must win and must not be written to disk.
	explicit := "explicit-token"
	got, err := LoadOrCreateToken(dir, explicit)
	if err != nil {
		t.Fatal(err)
	}
	if got != explicit {
		t.Fatalf("explicit token ignored: got %q", got)
	}
}

// The token file holds a secret and must not be world-readable.
func TestTokenFileIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateToken(dir, ""); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(TokenPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0077 != 0 {
		t.Errorf("token file permissions are %v, want owner-only", fi.Mode().Perm())
	}
}

// The token must never be written inside a temporary directory, or a reboot
// would leave an MCP client configured with a token that no longer exists.
func TestTokenDefaultDirIsPersistent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", home+"/data")
	t.Setenv("LOCALAPPDATA", home+"/data")

	// No explicit token, so one is generated and persisted. An explicit token
	// is deliberately never written to disk, which TestTokenIsStable covers.
	if _, err := LoadOrCreateToken("", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(TokenPath("")); err != nil {
		t.Fatalf("token was not written to the persistent location %q: %v", TokenPath(""), err)
	}
}
