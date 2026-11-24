package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The page was written for a desktop shell that no longer exists. Its script
// tried to import generated bindings from cmd/lantern-gui before falling back
// to HTTP. Nothing in this repository produces that path, so the import is a
// slow failure on every load and a claim the product cannot keep.
func TestUIPageHasNoDesktopShellBindings(t *testing.T) {
	page := string(uiPage)
	for _, dead := range []string{
		"guiservice",
		"bindings/",
		"__LANTERN__",
		"wails",
		"frontend/dist",
	} {
		if strings.Contains(page, dead) {
			t.Errorf("the page still refers to the removed desktop shell: %q", dead)
		}
	}
}

// Load the page in a JavaScript runtime against a fake daemon. Fetching /ui
// only proves the bytes were served; this proves the script runs, reads the
// stored token, calls the HTTP API, and renders what came back.
func TestUIPageLoadsAgainstAFakeAPI(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found; the UI smoke test needs a JavaScript runtime")
	}

	page := filepath.Join(t.TempDir(), "index.html")
	if err := os.WriteFile(page, uiPage, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(node, filepath.Join("testdata", "ui_smoke.mjs"), page)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the page did not load against a fake API: %v\n%s", err, out)
	}
}
