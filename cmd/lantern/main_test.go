package main

import (
	"os"
	"testing"
)

// parseArgs reads the daemon URL and token out of the environment before it
// looks at any flag, so an ambient LANTERND_URL or LANTERN_DAEMON_TOKEN
// changes what the tests see. Clearing them here makes the package hermetic by
// default: a test that wants one sets it with t.Setenv, which runs after this.
//
// Without this, `go test` on a machine that talks to a real lanternd fails on
// assertions that have nothing to do with what is being tested. That is worse
// than a flake, because it looks like a regression.
//
// HOME is deliberately left alone: lanternBinary runs `go build` with the
// inherited environment, and moving the home directory would move the Go build
// and module caches with it. A test that needs a different data directory sets
// the platform's variables itself.
func TestMain(m *testing.M) {
	for _, name := range []string{"LANTERND_URL", "LANTERN_DAEMON_TOKEN", "LANTERND_TOKEN", "LANTERN_DAEMON_TOKEN_FILE"} {
		if err := os.Unsetenv(name); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}
