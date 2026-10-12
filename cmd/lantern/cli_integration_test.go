package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// These exercise the built binary rather than parseArgs.
//
// The property under test is an ordering one — that a command needing a daemon
// is refused *before* any local node is created — and nothing at the function
// level can see it. main calls fatal, which exits, so the only way to observe
// what the process did is to run it.
//
// Building is guarded so `go test -short` stays fast; the platform CI runs the
// full suite, so this is not the only signal.

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

func lanternBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary; skipped under -short")
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "lantern-cli-")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "lantern")
		if runtime.GOOS == "windows" {
			binPath += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", binPath, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = err
			t.Logf("go build: %s", out)
		}
	})
	if buildErr != nil {
		t.Skipf("cannot build the binary: %v", buildErr)
	}
	return binPath
}

// runCLI runs the built binary and returns stdout, stderr and the exit code.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(lanternBinary(t), args...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code = 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running the binary: %v", err)
	}
	return outBuf.String(), errBuf.String(), code
}

// A daemon-backed command asked for without --daemon goes to the daemon, and
// must not bring up an in-process node on the way.
//
// The second half is the point. lantern.New starts a p2p host and writes an
// identity key and a data directory; a status query has no use for any of that,
// and creating it meant `lantern status` left a device identity behind on
// whatever machine ran it.
//
// No daemon is listening here, so the observable outcome is a connection error
// naming the daemon URL — which is the real problem, and is what a user who typed
// `lantern status` needs to be told.
func TestDaemonCommandUsesTheDaemonWithoutLocalSetup(t *testing.T) {
	dataDir := t.TempDir()

	for _, command := range []string{"status", "discover", "peers", "files"} {
		t.Run(command, func(t *testing.T) {
			dir := filepath.Join(dataDir, "state-"+command)
			stdout, _, code := runCLI(t, "--json", "--data-dir", dir, command)

			if code == 0 {
				t.Fatalf("%s should not succeed with no daemon running: %s", command, stdout)
			}
			if !strings.Contains(stdout, defaultDaemonURL) && !strings.Contains(stdout, "lanternd") {
				t.Errorf("%s did not report a daemon connection problem, got %q", command, stdout)
			}
			if strings.Contains(stdout, "pass --daemon") {
				t.Errorf("%s still demands the --daemon flag: %q", command, stdout)
			}
			if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
				names := make([]string, 0, len(entries))
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("%s created local state for a daemon command: %v", command, names)
			}
		})
	}
}

// A command that really does not exist must still be reported as unknown, so
// the new guidance does not swallow the genuine case.
func TestUnknownCommandStillReportedAsUnknown(t *testing.T) {
	_, stderr, code := runCLI(t, "wibble")
	if code == 0 {
		t.Fatal("an unknown command should exit non-zero")
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("expected an unknown-command error, got %q", stderr)
	}
	if strings.Contains(stderr, "pass --daemon") {
		t.Error("an unknown command should not be reported as a missing daemon flag")
	}
}
