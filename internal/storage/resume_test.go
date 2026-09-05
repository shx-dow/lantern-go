package storage

import (
	"encoding/json"
	"os"
	"testing"
)

func TestResumeStateRoundTripIsPrivate(t *testing.T) {
	dir := t.TempDir()
	name := "photo.bin"
	partial, err := PartialPath(dir, "code", name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, make([]byte, 10), 0600); err != nil {
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

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var stateFile os.FileInfo
	for _, e := range entries {
		if e.Name() == ".code.lantern-state" {
			stateFile, err = e.Info()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if stateFile == nil {
		t.Fatal("resume state file was not created")
	}
	if stateFile.Mode().Perm() != 0600 {
		t.Fatalf("resume state permissions are %o, want 600", stateFile.Mode().Perm())
	}
}

func TestLoadResumeDiscardsInvalidState(t *testing.T) {
	cases := map[string]ResumeState{
		"traversal filename": {Code: "code", FileName: "../secret", FileSize: 10, Offset: 2},
		"dot filename":       {Code: "code", FileName: ".", FileSize: 10, Offset: 2},
		"empty filename":     {Code: "code", FileName: "", FileSize: 10, Offset: 2},
		"offset past size":   {Code: "code", FileName: "f", FileSize: 10, Offset: 11},
		"negative offset":    {Code: "code", FileName: "f", FileSize: 10, Offset: -1},
		"negative size":      {Code: "code", FileName: "f", FileSize: -1, Offset: 0},
		"code mismatch":      {Code: "other", FileName: "f", FileSize: 10, Offset: 2},
	}
	for name, state := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path, err := statePath(dir, "code")
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}

			if _, ok, err := LoadResume(dir, "code"); err != nil || ok {
				t.Fatalf("invalid state accepted (ok=%v, err=%v)", ok, err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("invalid state still exists: %v", err)
			}
		})
	}
}

func TestLoadResumeDiscardsCorruptState(t *testing.T) {
	dir := t.TempDir()
	path, err := statePath(dir, "code")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{not json`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := LoadResume(dir, "code"); err != nil || ok {
		t.Fatalf("corrupt state accepted (ok=%v, err=%v)", ok, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("corrupt state still exists: %v", err)
	}
}

func TestLoadResumeTreatsMissingPartialAsFresh(t *testing.T) {
	dir := t.TempDir()
	// Valid state but the .part file was deleted: resume must be refused
	// and the stale state removed rather than failing the transfer.
	if err := SaveResume(dir, ResumeState{Code: "code", FileName: "f", FileSize: 10, Offset: 5}); err != nil {
		t.Fatal(err)
	}
	partial, err := PartialPath(dir, "code", "f")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(partial)
	if _, ok, err := LoadResume(dir, "code"); err != nil || ok {
		t.Fatalf("stale state accepted (ok=%v, err=%v)", ok, err)
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

func TestPartialPathRejectsPathLikeCode(t *testing.T) {
	if _, err := PartialPath(t.TempDir(), "../escape", "file"); err == nil {
		t.Fatal("path-like code was accepted")
	}
	if _, err := PartialPath(t.TempDir(), "", "file"); err == nil {
		t.Fatal("empty code was accepted")
	}
}
