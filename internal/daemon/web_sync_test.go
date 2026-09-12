package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The canonical web UI lives at frontend/dist/index.html so the Wails shell
// can serve it directly. web/index.html is the go:embed copy that keeps
// lanternd a single binary. This test fails on drift: edit the canonical
// file, then copy it over.
func TestWebUICopyInSync(t *testing.T) {
	page, _ := UI()
	want, err := os.ReadFile(filepath.Join("..", "..", "frontend", "dist", "index.html"))
	if err != nil {
		t.Fatalf("read canonical frontend: %v", err)
	}
	if !bytes.Equal(page, want) {
		t.Fatalf("web/index.html out of sync with frontend/dist/index.html (copy the canonical file over)")
	}
}
