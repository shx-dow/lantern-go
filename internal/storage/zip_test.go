package storage

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestZipDirRoundTrip(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("world"), 0600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out.zip")
	if err := ZipDir(src, dst); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	names := map[string]bool{}
	for _, f := range r.File {
		names[f.Name] = true
	}
	for _, want := range []string{"a.txt", "sub/", "sub/b.txt"} {
		if !names[want] {
			t.Fatalf("missing %q in %v", want, names)
		}
	}
}

func TestZipDirToTempNamesZip(t *testing.T) {
	src := t.TempDir()
	// ZipDirToTemp uses the source base name; point it at a named subdir.
	named := filepath.Join(src, "project")
	if err := os.Mkdir(named, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(named, "f.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	zp, cleanup, err := ZipDirToTemp(named)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if filepath.Base(zp) != "project.zip" {
		t.Fatalf("zip name = %q, want project.zip", filepath.Base(zp))
	}
	if _, err := os.Stat(zp); err != nil {
		t.Fatal(err)
	}
}
