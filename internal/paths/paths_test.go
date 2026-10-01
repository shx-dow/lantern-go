package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The whole point of this package. A temporary data directory means a reboot
// mints a new identity and silently invalidates every pairing, so no code
// path may fall back to one.
func TestDataIsNeverInTemp(t *testing.T) {
	dir := Data()
	tmp := os.TempDir()
	rel, err := filepath.Rel(tmp, dir)
	if err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("data dir %q is inside the temporary directory %q; state would be lost on reboot", dir, tmp)
	}
}

func TestDataIsAbsolute(t *testing.T) {
	if !filepath.IsAbs(Data()) {
		t.Fatalf("data dir %q must be absolute", Data())
	}
}

func TestDataEndsWithAppDir(t *testing.T) {
	if filepath.Base(Data()) != appDir {
		t.Fatalf("data dir %q should end in %q", Data(), appDir)
	}
}

func TestDataHonoursXDGDataHomeOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("platform uses its own convention")
	}
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	if got, want := Data(), filepath.Join(dir, appDir); got != want {
		t.Fatalf("Data() = %q, want %q", got, want)
	}
}

func TestConfigIsNeverInTemp(t *testing.T) {
	dir := Config()
	tmp := os.TempDir()
	rel, err := filepath.Rel(tmp, dir)
	if err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("config dir %q is inside the temporary directory %q", dir, tmp)
	}
}

func TestConfigAndDataDifferOnAppleAndWindows(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("Unix keeps config and data apart by convention already")
	}
	if Config() == Data() {
		t.Fatalf("config and data share a directory on %s: %q", runtime.GOOS, Config())
	}
}

func TestEnsureDataCreatesTheDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", filepath.Join(home, "local"))
	}
	t.Setenv("HOME", home)

	dir, err := EnsureData()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("EnsureData did not create %q: %v", dir, err)
	}
	if !fi.IsDir() {
		t.Fatalf("%q is not a directory", dir)
	}
	// The directory holds identity keys and pairings, so it must not be
	// world-readable.
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0077 != 0 {
		t.Errorf("data dir permissions are %v, want owner-only", fi.Mode().Perm())
	}
}
