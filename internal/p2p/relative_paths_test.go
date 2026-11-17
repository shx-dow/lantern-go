package p2p

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// fsRootsPair is fsPair for a provider with several shared roots.
func fsRootsPair(t *testing.T, roots []string) (*Node, peer.AddrInfo) {
	t.Helper()
	provider := fsHost(t)
	requester := fsHost(t)
	provider.SetReadAccess(allowRootsFor(roots, requester.Host.ID().String()))
	provider.SetWriteAccess(grantEveryone(nil))
	provider.RegisterFSHandler()
	provider.RegisterListHandler()
	return requester, peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
}

// A peer names a shared file by name. The name is resolved inside the shared
// roots, never against the daemon's working directory, so the names a listing
// shows are usable exactly as printed.
func TestFSReadRelativePath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.md"), "hello")
	requester, pi := fsPair(t, root)

	_, entry, data, eof, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: "notes.md"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("got %q", data)
	}
	if !eof {
		t.Fatal("expected eof on a short file")
	}
	if entry == nil {
		t.Fatal("expected the entry with a relative read")
	}
}

func TestFSReadNestedRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "sub", "deep.txt"), "nested")
	requester, pi := fsPair(t, root)

	_, _, data, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: filepath.Join("sub", "deep.txt")})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "nested" {
		t.Fatalf("got %q", data)
	}
}

func TestFSStatRelativePath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.md"), "hello")
	requester, pi := fsPair(t, root)

	_, entry, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpStat, Path: "notes.md"})
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.Size != 5 || entry.IsDir {
		t.Fatalf("bad stat: %+v", entry)
	}
}

// A listing of a relative directory name answers with that directory's
// contents rather than failing on the daemon's cwd.
func TestFSListRelativeSubdir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "sub", "inner.txt"), "x")
	requester, pi := fsRootsPair(t, []string{root})

	files, err := requester.ListRemote(fsCtx(t), pi, "sub")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "inner.txt" {
		t.Fatalf("got %+v", files)
	}
}

// A root listing prints each root by its own name, so that name has to work
// when typed back in.
func TestFSListRootByBasename(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	requester, pi := fsRootsPair(t, []string{root})

	files, err := requester.ListRemote(fsCtx(t), pi, filepath.Base(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "a.txt" {
		t.Fatalf("got %+v", files)
	}
}

// Two roots holding the same name make a bare name ambiguous. Guessing would
// silently read the wrong file, so the request is refused and both candidates
// are named.
func TestFSReadRelativePathAmbiguous(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(first, "notes.md"), "first")
	writeFile(t, filepath.Join(second, "notes.md"), "second")
	requester, pi := fsRootsPair(t, []string{first, second})

	_, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: "notes.md"})
	if err == nil {
		t.Fatal("expected an ambiguous path to be refused")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error should say the path is ambiguous: %v", err)
	}
}

func TestFSListRelativePathAmbiguous(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for _, root := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	requester, pi := fsRootsPair(t, []string{first, second})

	_, err := requester.ListRemote(fsCtx(t), pi, "sub")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected an ambiguous listing to be refused, got %v", err)
	}
}

// A relative path with a parent component stays inside the roots or fails; it
// never walks up from a root.
func TestFSRelativeParentRefused(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(parent, "outside.txt"), "secret")
	requester, pi := fsPair(t, root)

	escape := filepath.Join("..", "outside.txt")
	if _, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: escape}); err == nil {
		t.Fatal("expected a parent-relative read to be refused")
	}
	if _, err := requester.ListRemote(fsCtx(t), pi, ".."); err == nil {
		t.Fatal("expected a parent-relative listing to be refused")
	}
}

// A symlink inside a root that points outside it is still outside: the name
// resolves, but containment does not.
func TestFSRelativeSymlinkEscapeRefused(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(parent, "outside.txt"), "secret")
	if err := os.Symlink(filepath.Join(parent, "outside.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	requester, pi := fsPair(t, root)

	if _, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: "link.txt"}); err == nil {
		t.Fatal("expected a symlink out of the root to be refused")
	}
}

func TestFSReadRelativeMissing(t *testing.T) {
	requester, pi := fsPair(t, t.TempDir())

	_, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: "nope.md"})
	if err == nil {
		t.Fatal("expected a missing relative path to fail")
	}
	if !strings.Contains(err.Error(), "not in any shared dir") {
		t.Fatalf("error should say the path is not shared: %v", err)
	}
}

// An absolute path outside the roots is still refused, so adding relative
// resolution did not loosen the absolute case.
func TestFSAbsolutePathOutsideRootsRefused(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.txt")
	writeFile(t, outside, "secret")
	requester, pi := fsPair(t, root)

	if _, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: outside}); err == nil {
		t.Fatal("expected an absolute path outside the root to be refused")
	}
}
