package tray

import (
	"os"
	"testing"
)

func TestBrowserCmd(t *testing.T) {
	cases := map[string]string{
		"darwin":  "open",
		"windows": "rundll32",
		"linux":   "xdg-open",
		"freebsd": "xdg-open",
	}
	for goos, want := range cases {
		prog, args := browserCmd(goos, "http://x/")
		if prog != want || len(args) == 0 || args[len(args)-1] != "http://x/" {
			t.Fatalf("%s: got %s %v", goos, prog, args)
		}
	}
}

func TestIconARGB(t *testing.T) {
	const size = 24
	px := iconARGB(size)
	if len(px) != 4*size*size {
		t.Fatalf("got %d bytes, want %d", len(px), 4*size*size)
	}
	at := func(x, y int) []byte { return px[4*(y*size+x) : 4*(y*size+x)+4] }
	// Corners are border, center is fill, everything opaque.
	if at(0, 0)[0] != 0xFF || at(size/2, size/2)[0] != 0xFF {
		t.Fatal("icon must be opaque")
	}
	if string(at(0, 0)[1:]) == string(at(size/2, size/2)[1:]) {
		t.Fatal("border and fill must differ")
	}
	if string(at(size/2, size/2)[1:]) != string([]byte{0x7B, 0x56, 0xDB}) {
		t.Fatalf("fill changed: %v", at(size/2, size/2)[1:])
	}
}

func TestAvailableHeadless(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if Available() {
		t.Fatal("expected unavailable without a display")
	}
}

func TestRunHeadlessUnsupported(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	// Without a session bus there is nothing to register with. If the
	// sandbox unexpectedly provides one, there is still no watcher, so
	// either outcome is "unsupported" — but a bus WITH a watcher would
	// block; guard by skipping when a bus dials successfully.
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		t.Skip("session bus present; cannot prove headless path")
	}
	if err := Run(Config{Title: "t", UIURL: "http://x/"}); err != ErrNotSupported {
		t.Fatalf("got %v, want ErrNotSupported", err)
	}
}
