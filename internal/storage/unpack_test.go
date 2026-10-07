package storage

// Adversarial tests for extracting a pushed archive.
//
// An archive is remote input: a peer chose every name in it, every mode, and
// every size. These attack that input rather than exercising a happy path.

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testPerm is a permissive mode set, used where the modes themselves are not
// what a test is about. The push path supplies its own inherited modes.
var testPerm = ExtractPerm{Dir: 0o755, File: 0o644}

// buildZip writes an archive from name -> content entries. A name ending in
// "/" is written as a directory, and a name prefixed with "symlink:" is written
// as a symbolic link, so the awkward cases are constructible.
func buildZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range entries {
		switch {
		case strings.HasPrefix(name, "symlink:"):
			target := strings.TrimPrefix(name, "symlink:")
			hdr := &zip.FileHeader{Name: target}
			hdr.SetMode(os.ModeSymlink | 0o777)
			w, err := zw.CreateHeader(hdr)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		case strings.HasSuffix(name, "/"):
			hdr := &zip.FileHeader{Name: name}
			hdr.SetMode(os.ModeDir | 0o755)
			if _, err := zw.CreateHeader(hdr); err != nil {
				t.Fatal(err)
			}
		default:
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertAbsent(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s exists at %s, but the archive should have been refused", what, path)
	}
}

// A normal archive extracts with its structure intact.
func TestUnpackZipExtractsTree(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "a.zip")
	buildZip(t, zipPath, map[string]string{
		"top.txt":        "top level",
		"sub/nested.txt": "one down",
		"sub/deep/x.txt": "two down",
		"emptydir/":      "",
	})

	dst := filepath.Join(t.TempDir(), "out")
	if err := UnpackZip(zipPath, dst, testPerm); err != nil {
		t.Fatalf("a well-formed archive must extract: %v", err)
	}
	for name, want := range map[string]string{
		"top.txt":        "top level",
		"sub/nested.txt": "one down",
		"sub/deep/x.txt": "two down",
	} {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s did not land: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if fi, err := os.Stat(filepath.Join(dst, "emptydir")); err != nil || !fi.IsDir() {
		t.Errorf("an empty directory should be preserved: %v", err)
	}
}

// Zip slip: every spelling of an escape must be refused, and nothing may be
// written outside the destination.
func TestUnpackZipRefusesEscapes(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "precious.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	escapes := map[string]string{
		"dotdot file":      "../escaped.txt",
		"deep dotdot":      "sub/../../escaped.txt",
		"absolute path":    "/etc/escaped.txt",
		"absolute tmp":     filepath.Join(os.TempDir(), "escaped-tmp.txt"),
		"leading dotdot":   "../" + filepath.Base(victim),
		"backslash path":   `..\escaped.txt`,
		"drive component":  `C:\escaped.txt`,
		"mixed separators": `sub/..\..\escaped.txt`,
		"deep escape":      "a/b/c/../../../../escaped.txt",
	}
	for name, entry := range escapes {
		t.Run(name, func(t *testing.T) {
			zipPath := filepath.Join(t.TempDir(), "e.zip")
			// A harmless entry alongside it, so a refusal cannot be explained
			// by the archive being empty.
			buildZip(t, zipPath, map[string]string{
				"ok.txt": "fine",
				entry:    "pwned",
			})
			dst := filepath.Join(t.TempDir(), "out")
			if err := UnpackZip(zipPath, dst, testPerm); err == nil {
				t.Errorf("entry %q must be refused", entry)
			}
			// Nothing may be created outside the destination, and nothing
			// inside it either, since names are validated before any write.
			assertAbsent(t, filepath.Join(outside, "escaped.txt"), "escaped file")
			if got, err := os.ReadFile(victim); err != nil || string(got) != "original" {
				t.Fatalf("the victim file was modified: %q %v", got, err)
			}
			assertAbsent(t, filepath.Join(dst, "ok.txt"), "a partial extraction")
		})
	}
}

// A symbolic link in an archive must be refused: a pushed archive must not be
// able to plant one that later redirects a write.
func TestUnpackZipRefusesSymlinks(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "s.zip")
	buildZip(t, zipPath, map[string]string{
		"ok.txt":         "fine",
		"symlink:escape": "/etc/passwd",
	})

	dst := filepath.Join(t.TempDir(), "out")
	err := UnpackZip(zipPath, dst, testPerm)
	if err == nil {
		t.Fatal("an archive containing a symlink must be refused")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("the refusal should name the problem: %v", err)
	}
	assertAbsent(t, filepath.Join(dst, "escape"), "the symlink")
	assertAbsent(t, filepath.Join(dst, "ok.txt"), "a partial extraction")
}

// An entry that already exists on disk must not be replaced. The archive does
// not get to decide that; the caller's overwrite policy does.
func TestUnpackZipRefusesToReplaceExisting(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dst, "keep.txt")
	if err := os.WriteFile(existing, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	zipPath := filepath.Join(t.TempDir(), "k.zip")
	buildZip(t, zipPath, map[string]string{"keep.txt": "replaced"})
	if err := UnpackZip(zipPath, dst, testPerm); err == nil {
		t.Fatal("an archive must not replace an existing file")
	}
	if got, _ := os.ReadFile(existing); string(got) != "original" {
		t.Fatalf("the existing file was replaced: %q", got)
	}
}

// A device file or fifo in an archive is neither a file nor a directory, and
// must be refused rather than created.
func TestUnpackZipRefusesIrregularEntries(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "i.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	hdr := &zip.FileHeader{Name: "device"}
	hdr.SetMode(os.ModeDevice | 0o600)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(nil); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dst := filepath.Join(t.TempDir(), "out")
	if err := UnpackZip(zipPath, dst, testPerm); err == nil {
		t.Fatal("a device entry must be refused")
	}
}

// Round-trip: what ZipDir writes must come back out of UnpackZip unchanged.
func TestZipDirUnpackRoundTrip(t *testing.T) {
	src := t.TempDir()
	for name, content := range map[string]string{
		"one.txt":        "first",
		"sub/two.txt":    "second",
		"sub/deep/three": "third",
	} {
		full := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	zipPath := filepath.Join(t.TempDir(), "r.zip")
	if err := ZipDir(src, zipPath); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out")
	if err := UnpackZip(zipPath, dst, testPerm); err != nil {
		t.Fatalf("an archive Lantern produced must extract: %v", err)
	}
	for name, want := range map[string]string{
		"one.txt":        "first",
		"sub/two.txt":    "second",
		"sub/deep/three": "third",
	} {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s did not survive the round trip: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// A directory whose entries are all symlinks archives to nothing, and must not
// silently extract as an empty tree that looks like data loss.
func TestZipDirSkipsSymlinks(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	zipPath := filepath.Join(t.TempDir(), "s.zip")
	if err := ZipDir(src, zipPath); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out")
	if err := UnpackZip(zipPath, dst, testPerm); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, filepath.Join(dst, "link"), "a symlink")
	if _, err := os.Stat(filepath.Join(dst, "real.txt")); err != nil {
		t.Errorf("the real file should still be there: %v", err)
	}
}

// A corrupt or truncated archive must be reported, not half-extracted.
func TestUnpackZipRejectsCorruptArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.zip")
	if err := os.WriteFile(path, []byte("this is not a zip file at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UnpackZip(path, filepath.Join(t.TempDir(), "out"), testPerm); err == nil {
		t.Fatal("a corrupt archive must be reported")
	}
}

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	good := []string{"a.txt", "sub/b.txt", "sub/deep/c.txt", "dots..in..name.txt"}
	for _, name := range good {
		if _, err := safeJoin(root, name); err != nil {
			t.Errorf("safeJoin(%q) failed: %v", name, err)
		}
	}
	bad := []string{
		"", "..", "../x", "a/../../x", "/abs", `..\x`, `C:\x`, "sub/../..",
	}
	for _, name := range bad {
		if got, err := safeJoin(root, name); err == nil {
			t.Errorf("safeJoin(%q) = %q, want an error", name, got)
		}
	}
}

// within is the last-resort containment check, and the shared-prefix trap is
// the one thing it catches that the name-level checks above do not: a plain
// prefix test would accept /srv/inbox-evil as sitting inside /srv/inbox.
func TestWithinRejectsSharedPrefix(t *testing.T) {
	root := filepath.Join(string(os.PathSeparator), "srv", "inbox")
	cases := map[string]bool{
		root:                          true,
		filepath.Join(root, "a"):      true,
		filepath.Join(root, "a", "b"): true,
		root + "-evil":                false,
		root + "2":                    false,
		filepath.Dir(root):            false,
		"/":                           false,
		"":                            false,
	}
	for path, want := range cases {
		if got := within(path, root); got != want {
			t.Errorf("within(%q, %q) = %v, want %v", path, root, got, want)
		}
	}
}
