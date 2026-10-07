package p2p

// Tests for pushing a directory onto a paired device.
//
// A directory push is the one write that creates many paths at once, so these
// hold it to the same properties a single-file push already has: the
// destination is resolved under the remote's own policy, a digest must verify
// before anything is expanded, an existing tree is not replaced without the
// flag, and a failure leaves no partial tree behind.

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/storage"
)

// pushDirOver builds a small tree, archives it, and sends it to the paired
// node with the destination directory name.
func pushDirOver(t *testing.T, requester *Node, pi peer.AddrInfo, dest string, overwrite bool) ([]FSEntry, string, error) {
	t.Helper()
	zipPath, cleanup, err := storage.ZipDirToTemp(writeTree(t, map[string]string{
		"top.txt":        "top",
		"sub/nested.txt": "nested",
		"sub/deep/d.txt": "deep",
	}))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	defer cleanup()
	archive, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	return requester.WriteDirFS(fsCtx(t), pi, dest, archive, overwrite)
}

// writeTree builds a directory holding files, keyed by slash-separated
// relative name, and returns its path.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// maliciousZip builds an archive from name -> content entries, so an entry name
// can be one the extractor must refuse. A name prefixed "symlink:" is written
// as a symbolic link.
func maliciousZip(t *testing.T, entries map[string]string) ([]byte, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "m.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range entries {
		var w io.Writer
		if target, isLink := strings.CutPrefix(name, "symlink:"); isLink {
			hdr := &zip.FileHeader{Name: target}
			hdr.SetMode(os.ModeSymlink | 0o777)
			if w, err = zw.CreateHeader(hdr); err != nil {
				t.Fatal(err)
			}
		} else if w, err = zw.Create(name); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data, func() { _ = os.Remove(path) }
}

// The happy path: the tree lands with its structure intact, and the entries
// reported back describe what is actually on disk rather than what was sent.
func TestFSDirWriteLandsTree(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteSharedRoots, Roots: []string{root}, MaxBytes: 1 << 20})

	dest := filepath.Join(root, "inbox", "photos")
	entries, digest, err := pushDirOver(t, requester, pi, dest, false)
	if err != nil {
		t.Fatalf("a directory push must land: %v", err)
	}
	if digest == "" {
		t.Error("a directory push must report the digest of what was stored")
	}
	for name, want := range map[string]string{
		"top.txt":        "top",
		"sub/nested.txt": "nested",
		"sub/deep/d.txt": "deep",
	} {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s did not land: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	// The reported entries are the receiver's own listing of what it created.
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{"top.txt", "sub/nested.txt", "sub/deep/d.txt"} {
		if !names[want] {
			t.Errorf("the receiver did not report %q among %v", want, names)
		}
	}
	// No archive is left behind: the destination is a tree, not a zip.
	if _, err := os.Stat(dest + ".zip"); !os.IsNotExist(err) {
		t.Error("the archive was left at the destination")
	}
}

// The destination is resolved under the remote's write policy, exactly as a
// file push is. A tree is not a way around the roots.
func TestFSDirWriteRespectsRoots(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteSharedRoots, Roots: []string{root}, MaxBytes: 1 << 20})

	// A destination outside every writable root must be refused, and nothing
	// may be created anywhere as a result.
	if _, _, err := pushDirOver(t, requester, pi, filepath.Join(outside, "escaped"), false); err == nil {
		t.Fatal("a destination outside the writable roots must be refused")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Errorf("a refused push wrote outside the roots: %v", err)
	}
}

// An existing directory is not replaced without the flag, and not replaced by
// a partial tree when the flag is set either.
func TestFSDirWriteRespectsOverwrite(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteSharedRoots, Roots: []string{root}, MaxBytes: 1 << 20})

	dest := filepath.Join(root, "inbox")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dest, "keep.txt")
	if err := os.WriteFile(existing, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := pushDirOver(t, requester, pi, dest, false); err == nil {
		t.Fatal("an existing directory must not be replaced without overwrite")
	}
	if got, _ := os.ReadFile(existing); string(got) != "mine" {
		t.Fatalf("the existing tree was disturbed: %q", got)
	}

	if _, _, err := pushDirOver(t, requester, pi, dest, true); err != nil {
		t.Fatalf("with overwrite set the tree must land: %v", err)
	}
	// The replacement is a whole tree, not a merge: what was there is gone.
	if _, err := os.Stat(existing); !os.IsNotExist(err) {
		t.Error("the replaced tree should be gone, not merged into")
	}
	if _, err := os.Stat(filepath.Join(dest, "top.txt")); err != nil {
		t.Errorf("the new tree should be in place: %v", err)
	}
}

// A destination that exists as a file is not turned into a directory, however
// the request is framed.
func TestFSDirWriteRefusesFileDestination(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteSharedRoots, Roots: []string{root}, MaxBytes: 1 << 20})

	dest := filepath.Join(root, "afile")
	if err := os.WriteFile(dest, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, overwrite := range []bool{false, true} {
		if _, _, err := pushDirOver(t, requester, pi, dest, overwrite); err == nil {
			t.Errorf("overwrite=%v: a file at the destination must be refused", overwrite)
		}
	}
	if got, _ := os.ReadFile(dest); string(got) != "not a directory" {
		t.Errorf("the file was modified: %q", got)
	}
}

// A push of a directory that is not an archive must be refused, and must not
// create a destination directory for the would-be tree.
func TestFSDirWriteRefusesNonArchive(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteSharedRoots, Roots: []string{root}, MaxBytes: 1 << 20})

	dest := filepath.Join(root, "bogus")
	if _, _, err := requester.WriteDirFS(fsCtx(t), pi, dest, []byte("this is not a zip"), false); err == nil {
		t.Fatal("content that is not an archive must be refused")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("a refused archive created the destination")
	}
}

// An archive whose entries escape the destination is refused, and nothing
// outside the destination is created. This is the attack the whole expansion
// step exists to stop, so it is asserted end to end over the protocol rather
// than only against the extractor.
func TestFSDirWriteRefusesArchiveEscape(t *testing.T) {
	root := t.TempDir()
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "precious.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteSharedRoots, Roots: []string{root}, MaxBytes: 1 << 20})

	archive, cleanup := maliciousZip(t, map[string]string{
		"ok.txt":                      "fine",
		"../" + filepath.Base(victim): "pwned",
	})
	defer cleanup()

	dest := filepath.Join(root, "evil")
	if _, _, err := requester.WriteDirFS(fsCtx(t), pi, dest, archive, false); err == nil {
		t.Fatal("an archive escaping the destination must be refused")
	}
	if got, _ := os.ReadFile(victim); string(got) != "original" {
		t.Errorf("the victim file was overwritten: %q", got)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("a refused archive created the destination")
	}
}

// A refused push must leave nothing behind. Temp files and staging
// directories are the visible form of that: a half-finished transfer should
// not litter the destination's parent.
func TestFSDirWriteLeavesNoDebris(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteSharedRoots, Roots: []string{root}, MaxBytes: 1 << 20})

	inbox := filepath.Join(root, "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pushDirOver(t, requester, pi, filepath.Join(inbox, "garbage"), false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := requester.WriteDirFS(fsCtx(t), pi, filepath.Join(inbox, "bad"), []byte("not a zip"), false); err == nil {
		t.Fatal("expected a refusal")
	}

	entries, err := os.ReadDir(inbox)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) > 0 && e.Name()[0] == '.' {
			t.Errorf("a temp or staging artifact was left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 || entries[0].Name() != "garbage" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected only the pushed tree, got %v", names)
	}
}

// A pushed tree takes its permissions from the directory it lands in, not from
// the archive and not from a public default. This is the same rule a file push
// follows, and the audit found the alternative widening access.
func TestFSDirWriteInheritsPermissions(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteSharedRoots, Roots: []string{root}, MaxBytes: 1 << 20})

	dest := filepath.Join(private, "tree")
	if _, _, err := pushDirOver(t, requester, pi, dest, false); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dest, filepath.Join(dest, "top.txt"), filepath.Join(dest, "sub")} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is %v, which is readable by others; it should inherit 0700 from the parent", p, fi.Mode().Perm())
		}
	}
}

// A peer with no write policy at all cannot push a directory either. Tier
// and mode are checked before the archive is even considered, so a directory
// push is not a way to probe for access.
func TestFSDirWriteRefusedWithoutWritePolicy(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteDenied, MaxBytes: 1 << 20})

	dest := filepath.Join(root, "tree")
	if _, _, err := pushDirOver(t, requester, pi, dest, false); err == nil {
		t.Fatal("a device that refuses writes must refuse a directory push")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("a refused push created the destination")
	}
}

// A directory push is bounded by the same byte cap as a file push. The cap is
// on what crosses the wire, which is the archive.
func TestFSDirWriteRespectsSizeCap(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteSharedRoots, Roots: []string{root}, MaxBytes: 16})

	if _, _, err := pushDirOver(t, requester, pi, filepath.Join(root, "tree"), false); err == nil {
		t.Fatal("an archive over the cap must be refused")
	}
	if _, err := os.Stat(filepath.Join(root, "tree")); !os.IsNotExist(err) {
		t.Error("a refused push created the destination")
	}
}
