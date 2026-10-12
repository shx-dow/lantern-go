package p2p

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func testListNode(t *testing.T, root string, trusted map[string]bool) *Node {
	t.Helper()
	n, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	n.SetReadAccess(func(id string) []string {
		if trusted[id] {
			return []string{root}
		}
		return nil
	})
	n.RegisterListHandler()
	return n
}

func TestListRemoteAllowed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := testListNode(t, root, nil)
	// Trust the requester after boot (its ID is stable per temp dir key,
	// but here both nodes are fresh; allow by actual requester ID below).
	requester, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requester.Close() })
	provider.SetReadAccess(allowRootsFor([]string{root}, requester.Host.ID().String()))

	pi := peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	files, err := requester.ListRemote(ctx, pi, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "a.txt" {
		t.Fatalf("bad listing: %+v", files)
	}
}

func TestListRemoteDeniedWhenUnpaired(t *testing.T) {
	root := t.TempDir()
	provider := testListNode(t, root, map[string]bool{})
	requester, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requester.Close() })
	pi := peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := requester.ListRemote(ctx, pi, root); err == nil {
		t.Fatal("expected pairing denial")
	}
}

func TestListRemoteRejectsBreakout(t *testing.T) {
	root := t.TempDir()
	provider := testListNode(t, root, nil)
	requester, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requester.Close() })
	provider.SetReadAccess(allowAllRoots(root))
	pi := peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := requester.ListRemote(ctx, pi, "/etc"); err == nil {
		t.Fatal("expected breakout rejection")
	}
}

// Paths are built with filepath.Join rather than written as literals. The
// comparison is separator-aware by design, so a hard-coded "/srv/root" is not a
// Windows path at all — it does not match against "\\srv\\root" and the test
// fails for a reason that has nothing to do with the logic under test.
func TestPathWithin(t *testing.T) {
	// Built from segments joined with the host separator, so each case is a real
	// path on the platform running it. Hard-coded "/srv/root" literals are not
	// Windows paths at all, and the comparison is separator-aware by design, so
	// they fail for a reason unrelated to the logic under test.
	root := filepath.Join(string(filepath.Separator), "srv", "root")
	cases := []struct {
		path, root string
		want       bool
		why        string
	}{
		{root, root, true, "the root itself"},
		{filepath.Join(root, "a.txt"), root, true, "a child"},
		{filepath.Join(root, "deep", "a.txt"), root, true, "a grandchild"},
		{filepath.Join(string(filepath.Separator), "srv", "rooted", "a.txt"), root, false, "a sibling with a shared prefix"},
		{filepath.Join(string(filepath.Separator), "srv", "root2"), root, false, "a similar name"},
		{filepath.Join(string(filepath.Separator), "srv"), root, false, "a parent"},
		{filepath.Join(string(filepath.Separator), "etc", "passwd"), root, false, "an unrelated path"},
		{root, filepath.Join(root, "deeper"), false, "the other way round"},
		{string(filepath.Separator), string(filepath.Separator), true, "filesystem root"},
	}
	for _, c := range cases {
		if got := pathWithin(c.path, c.root); got != c.want {
			t.Errorf("pathWithin(%q, %q) = %v, want %v (%s)", c.path, c.root, got, c.want, c.why)
		}
	}
}

// The separator is what separates a child from a merely similar sibling, so
// this must hold regardless of how the host compares casing.
func TestPathWithinRejectsPrefixSiblingRegardlessOfCase(t *testing.T) {
	root := filepath.Join("/srv", "root")
	if !pathWithin(filepath.Join(root, "a"), root) {
		t.Fatal("a genuine child was rejected")
	}
	if pathWithin(filepath.Join("/srv", "rooted"), root) {
		t.Fatal("a sibling directory sharing a prefix was accepted")
	}
	if pathWithin("/SRV/ROOTED/x", "/srv/root") {
		t.Fatal("a case-shifted sibling directory was accepted")
	}
}

// A shared root of the filesystem root must work. Appending a separator to a
// root that already ends in one produces "//", which no path begins with, so
// this used to make an operator who deliberately shared "/" find nothing
// readable at all.
func TestPathWithinFilesystemRoot(t *testing.T) {
	sep := string(os.PathSeparator)
	if !pathWithin(sep+"etc", sep) {
		t.Error("a path beneath the filesystem root must be inside it")
	}
	if !pathWithin(sep, sep) {
		t.Error("the filesystem root must contain itself")
	}
	if pathWithin("relative", sep) {
		t.Error("a relative path must not be inside the filesystem root")
	}
}

// A root that already ends in a separator, as a config file may well supply,
// must behave the same as one that does not.
func TestPathWithinTrailingSeparator(t *testing.T) {
	sep := string(os.PathSeparator)
	if !pathWithin(sep+"srv"+sep+"root"+sep+"a.txt", sep+"srv"+sep+"root"+sep) {
		t.Error("a child of a root with a trailing separator must be inside it")
	}
	if pathWithin(sep+"srv"+sep+"rooted", sep+"srv"+sep+"root"+sep) {
		t.Error("a sibling of a root with a trailing separator must not be inside it")
	}
}
