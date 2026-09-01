package storage

import (
	"os"
	"testing"
)

func TestResumeStateRoundTripIsPrivate(t *testing.T) {
	dir := t.TempDir()
	name := "photo.bin"
	if err := os.WriteFile(PartialPath(dir, "code", name), make([]byte, 10), 0600); err != nil {
		t.Fatal(err)
	}
	want := ResumeState{Code: "code", FileName: name, FileSize: 20, Offset: 10}

	if err := SaveResume(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LoadResume(dir, "code")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != want {
		t.Fatalf("got (%+v, %v), want (%+v, true)", got, ok, want)
	}

	info, err := os.Stat(statePath(dir, "code"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("resume state permissions are %o, want 600", info.Mode().Perm())
	}
}

func TestLoadResumeDiscardsInvalidState(t *testing.T) {
	dir := t.TempDir()
	path := statePath(dir, "code")
	if err := os.WriteFile(path, []byte(`{"code":"code","file_name":"../secret","file_size":10,"offset":2}`), 0600); err != nil {
		t.Fatal(err)
	}

	_, ok, err := LoadResume(dir, "code")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("invalid state was accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid state still exists: %v", err)
	}
}

func TestSaveResumeRejectsOffsetPastFileSize(t *testing.T) {
	err := SaveResume(t.TempDir(), ResumeState{Code: "code", FileName: "file", FileSize: 1, Offset: 2})
	if err == nil {
		t.Fatal("expected invalid offset error")
	}
}

func TestSaveResumeRejectsPathLikeCode(t *testing.T) {
	err := SaveResume(t.TempDir(), ResumeState{Code: "../escape", FileName: "file", FileSize: 1, Offset: 0})
	if err == nil {
		t.Fatal("path-like code was accepted")
	}
}
