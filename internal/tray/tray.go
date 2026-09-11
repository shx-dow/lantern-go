// Package tray shows a minimal OS tray icon for lanternd.
//
// Interaction is deliberately tiny: clicking the icon opens the Lantern
// web UI in a browser. There is no tray menu in v1; the daemon quits via
// SIGINT/SIGTERM. Backends live in per-OS files behind build tags and are
// pure Go everywhere except darwin, which needs one small CGO/AppKit file.
package tray

import (
	"errors"
	"os/exec"
	"runtime"
	"sync"
)

// ErrNotSupported reports that no tray backend can run here (headless
// session, unknown OS, ...). Callers should fall back to console behavior.
var ErrNotSupported = errors.New("tray: not supported in this environment")

// Config describes the icon. UIURL is opened in a browser on click.
type Config struct {
	Title string
	UIURL string
}

var (
	stopCh   = make(chan struct{})
	stopOnce sync.Once
)

// Quit unblocks a Run in progress. Safe to call multiple times and before Run.
func Quit() {
	stopOnce.Do(func() { close(stopCh) })
}

func stopped() bool {
	select {
	case <-stopCh:
		return true
	default:
		return false
	}
}

// openBrowser opens url in the default browser, best-effort.
func openBrowser(url string) error {
	prog, args := browserCmd(runtime.GOOS, url)
	return exec.Command(prog, args...).Start()
}

// browserCmd maps GOOS to the opener program; split out for testing.
func browserCmd(goos, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}

// iconARGB renders a size×size lantern mark as ARGB32 pixels (alpha first):
// a purple square with a darker border on transparent background. Used by
// backends that must supply their own pixels (Linux StatusNotifierItem).
func iconARGB(size int) []byte {
	const (
		fillR, fillG, fillB = 0x7B, 0x56, 0xDB
		edgeR, edgeG, edgeB = 0x4A, 0x2F, 0xA3
	)
	px := make([]byte, 4*size*size)
	border := 2
	if size < 8 {
		border = 1
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			i := 4 * (y*size + x)
			if x < border || y < border || x >= size-border || y >= size-border {
				px[i], px[i+1], px[i+2], px[i+3] = 0xFF, edgeR, edgeG, edgeB
			} else {
				px[i], px[i+1], px[i+2], px[i+3] = 0xFF, fillR, fillG, fillB
			}
		}
	}
	return px
}
