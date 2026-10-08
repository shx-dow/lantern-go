package p2p

// Adversarial tests for how a push lands on disk: permissions, created
// directories, and the symlink behaviour of the widest write mode. These are
// about the blast radius of a file that a paired peer asked to be written,
// rather than about whether it arrives.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func posixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}
}

// A pushed file is readable by anyone who can reach the shared dir. If the
// operator's shared root is private, a push must not widen that.
func TestAuditPushedFilePermissionsRespectRoot(t *testing.T) {
	posixOnly(t)
	root := filepath.Join(t.TempDir(), "private-share")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	dst := filepath.Join(root, "secret.txt")
	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("sensitive"), false); err != nil {
		t.Fatal(err)
	}
	// A push must not widen a private root. This used to be a t.Logf, which
	// meant the test could not fail: the finding it reported was real at the
	// time it was written, and the fix that answered it (a push inherits the
	// landing directory's mode instead of hardcoding 0644) left the assertion
	// behind as a log line. The sibling test below already asserts this same
	// property with t.Errorf for created directories.
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("push created %s with mode %o inside a 0700 root; the file mode must be inherited, never widened", dst, perm)
	} else {
		t.Logf("pushed file mode: %o, inherited from the 0700 root", fi.Mode().Perm())
	}
}

// Missing parent directories are created by the push. Their mode matters:
// a nested directory created world-readable inside a private root is a
// widening the operator never asked for.
func TestAuditCreatedDirectoriesAreNotWorldAccessible(t *testing.T) {
	posixOnly(t)
	root := filepath.Join(t.TempDir(), "private-share")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	dst := filepath.Join(root, "nested", "deeper", "file.txt")
	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("x"), false); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(root, "nested"),
		filepath.Join(root, "nested", "deeper"),
	} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("push created %s with mode %o inside a 0700 root", dir, perm)
		}
	}
}

// WriteAnywhere, which allowed writes to any path on the disk, is gone. It was
// reachable only from tests, and it was the last write mode that skipped the
// shared-root resolver — the one boundary that makes a pairing reviewable.
//
// Its removal is asserted rather than assumed: a policy carrying the old mode
// must fail closed, not fall through to an unconstrained write.
func TestAuditRemovedWriteModeFailsClosed(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: "anywhere", Roots: []string{root}, MaxBytes: 1024})

	_, _, err := requester.WriteFS(fsCtx(t), pi, filepath.Join(root, "escape.txt"), []byte("x"), false)
	if err == nil {
		t.Fatal("the removed write mode must refuse, not allow an unconstrained write")
	}
	if _, statErr := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(statErr) {
		t.Error("a refused write created a file")
	}
}

// A shared root that is itself a symlink is an operator choice, and the
// boundary must follow the resolved location rather than the spelling. This
// pins that down, because it is the case where "inside the root" is arguable.
func TestAuditSymlinkedRootResolvesToTarget(t *testing.T) {
	target := t.TempDir()
	parent := t.TempDir()
	link := filepath.Join(parent, "share")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Write through the symlinked root: allowed.
	requester, pi := fsPairWith(t, parent, writePolicy(link, 0))
	dst := filepath.Join(link, "inside.txt")
	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("ok"), false); err != nil {
		t.Fatalf("a symlinked root is the operator's choice: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "inside.txt")); err != nil || string(got) != "ok" {
		t.Fatalf("write did not land in the resolved root: %q %v", got, err)
	}

	// And a path outside the resolved root is still refused, even though it
	// shares the symlink's parent directory.
	outside := filepath.Join(parent, "sibling.txt")
	if _, _, err := requester.WriteFS(fsCtx(t), pi, outside, []byte("nope"), false); err == nil {
		t.Fatal("a sibling of the symlinked root must be refused")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("a sibling of the symlinked root was written")
	}
}

// A destination whose final component is a symlink pointing *inside* the root
// is still refused: the refusal is about following links, not about escaping.
func TestAuditSymlinkWithinRootAlsoRefused(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real.txt")
	if err := os.WriteFile(real, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	if _, _, err := requester.WriteFS(fsCtx(t), pi, link, []byte("pwned"), true); err == nil {
		t.Fatal("writing through a symlink must be refused even when it stays inside the root")
	}
	if got, _ := os.ReadFile(real); string(got) != "original" {
		t.Fatalf("symlink target modified: %q", got)
	}
}

// A push must not be able to create a file whose name would be interpreted
// differently by a later reader, such as one containing a newline or a path
// separator smuggled through the base name.
func TestAuditOddDestinationNamesAreContained(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	for _, base := range []string{"with space.txt", "with\nnewline.txt", "with\ttab.txt", "..hidden.txt", "a.b.c.txt"} {
		dst := filepath.Join(root, base)
		if strings.ContainsAny(base, "/\\") {
			continue
		}
		if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("x"), false); err != nil {
			t.Fatalf("%q should be a legal name: %v", base, err)
		}
		// Whatever happened, it must be a direct child of root.
		ents, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range ents {
			if e.Name() == base {
				found = true
			}
		}
		if !found {
			t.Errorf("%q did not land as a direct child of the root", base)
		}
	}
}

// The rule is inheritance, not a fixed mode: a permissive root still gets
// permissive content, so an operator who shares a folder for a group is not
// silently broken by this.
func TestAuditPushInheritsPermissiveRoot(t *testing.T) {
	posixOnly(t)
	root := filepath.Join(t.TempDir(), "open-share")
	if err := os.MkdirAll(root, 0777); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	dst := filepath.Join(root, "sub", "file.txt")
	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("x"), false); err != nil {
		t.Fatal(err)
	}

	// Compare against the root's real mode, which umask may have reduced.
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := rootInfo.Mode().Perm()
	wantFile := wantDir&^0o111 | 0o400

	di, err := os.Stat(filepath.Join(root, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != wantDir {
		t.Errorf("created dir mode %o, want the root's %o", perm, wantDir)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != wantFile {
		t.Errorf("stored file mode %o, want %o", perm, wantFile)
	}
}

// The file mode is the directory mode minus execute: readable but not runnable.
func TestAuditInheritedPermStripsExecuteFromFile(t *testing.T) {
	cases := []struct {
		dir, file os.FileMode
	}{
		{0o700, 0o600},
		{0o750, 0o640},
		{0o755, 0o644},
		{0o777, 0o666},
		// A root with no owner write is inherited as-is, so the push fails
		// at the create rather than widening rights the operator removed.
		{0o500, 0o400},
		{0o555, 0o444},
	}
	for _, tc := range cases {
		base := filepath.Join(t.TempDir(), "root")
		if err := os.MkdirAll(base, tc.dir); err != nil {
			t.Fatal(err)
		}
		// MkdirAll applies umask, so set the mode the test is about.
		if err := os.Chmod(base, tc.dir); err != nil {
			t.Fatal(err)
		}
		perm, err := inheritedPerm(filepath.Join(base, "nested", "deeper"))
		if err != nil {
			t.Fatalf("root %o: %v", tc.dir, err)
		}
		if perm.file != tc.file {
			t.Errorf("root %o: file mode %o, want %o", tc.dir, perm.file, tc.file)
		}
		// The directory mode is the ancestor's mode, unchanged: a push must
		// never add access the operator did not grant.
		if perm.dir != tc.dir {
			t.Errorf("root %o: created dir %o, want %o", tc.dir, perm.dir, tc.dir)
		}
	}
}

// The mode comes from the nearest existing ancestor, so a deeper existing
// directory wins over the root.
func TestAuditInheritedPermUsesNearestExistingAncestor(t *testing.T) {
	posixOnly(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mid := filepath.Join(root, "mid")
	if err := os.MkdirAll(mid, 0o700); err != nil {
		t.Fatal(err)
	}
	perm, err := inheritedPerm(filepath.Join(mid, "deeper"))
	if err != nil {
		t.Fatal(err)
	}
	if perm.dir != 0o700 || perm.file != 0o600 {
		t.Fatalf("expected the 0700 ancestor to win, got dir=%o file=%o", perm.dir, perm.file)
	}
}

// An ancestor that is a file, not a directory, is an error rather than a
// silent fallback to a permissive mode.
func TestAuditInheritedPermRejectsNonDirectoryAncestor(t *testing.T) {
	posixOnly(t)
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inheritedPerm(filepath.Join(file, "under")); err == nil {
		t.Fatal("expected an error when the nearest ancestor is a file")
	}
}
