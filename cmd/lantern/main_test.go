package main

import (
	"os"
	"testing"
)

// parseArgs reads the daemon URL and token out of the environment before it
// looks at any flag, so an ambient LANTERND_URL or LANTERN_DAEMON_TOKEN
// changes what the tests see. Clearing both here makes the package hermetic by
// default: a test that wants one sets it with t.Setenv, which runs after this.
//
// Without this, `go test` on a machine that talks to a real lanternd fails on
// assertions that have nothing to do with what is being tested. That is worse
// than a flake, because it looks like a regression.
func TestMain(m *testing.M) {
	for _, name := range []string{"LANTERND_URL", "LANTERN_DAEMON_TOKEN"} {
		if err := os.Unsetenv(name); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}
