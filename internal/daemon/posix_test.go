package daemon

import (
	"runtime"
	"testing"
)

// posixOnly skips a test that asserts Unix permission bits.
//
// Windows has no POSIX mode: os.Stat reports 0666 for a file the caller just
// made private, and Chmod only toggles the read-only attribute. The assertions
// these tests make are meaningless there, not merely flaky — and CI running on
// Linux alone would never have noticed that.
func posixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}
}
