package storage

import (
	"runtime"
	"testing"
)

// posixOnly skips a test that asserts Unix permission bits. Windows has no
// POSIX mode, so os.Stat reports 0666 regardless of what was written.
func posixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}
}
