package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrustStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("expected empty store, got %v", got)
	}
	e, err := s.Add("peer-abc", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if e.PeerID != "peer-abc" || e.Alias != "laptop" {
		t.Fatalf("bad entry: %+v", e)
	}
	if !s.Trusted("peer-abc") || s.Trusted("peer-nope") {
		t.Fatal("Trusted mismatch")
	}
	again, err := NewTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Trusted("peer-abc") {
		t.Fatal("entry did not persist")
	}
	if !s.Remove("peer-abc") || s.Remove("peer-abc") {
		t.Fatal("remove semantics wrong")
	}
	if _, err := s.Add("", "x"); err == nil {
		t.Fatal("expected error for empty peer ID")
	}
}

func TestListSharedFilesConfined(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	ents, err := ListSharedFiles([]string{root}, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 2 || ents[0].Name != "a.txt" || ents[1].Name != "sub" {
		t.Fatalf("bad listing: %+v", ents)
	}
	if _, err := ListSharedFiles([]string{root}, "/etc"); err == nil {
		t.Fatal("expected breakout rejection")
	}
	roots, err := ListSharedFiles([]string{root}, "")
	if err != nil || len(roots) != 1 {
		t.Fatalf("root listing failed: %v %+v", err, roots)
	}
}
