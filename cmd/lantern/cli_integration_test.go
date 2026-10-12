package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
// The property under test is routing. Three things have to hold: the request
// reaches the daemon, no in-process node is built on the way, and the CLI never
// tells the user to pass a flag they did not need to pass.
//
// The daemon is stood up in-process rather than assumed absent. runCLI inherits
// the environment, so a LANTERND_URL in it — or a lanternd listening on the
// default port — would otherwise decide whether this test passed, and a
// developer's machine is not a test fixture. A real endpoint also proves more
// than an unreachable one: an unreachable port cannot tell you the request went
// to the *right* daemon, only that it went somewhere that was closed.
func TestDaemonCommandUsesTheDaemonWithoutLocalSetup(t *testing.T) {
	// Skips the whole test under -short, where there is no binary to run. Without
	// this the subtests each skip and the request count below still runs, against
	// a daemon nothing ever contacted.
	lanternBinary(t)

	const marker = "test-daemon-was-contacted"

	var requests int
	var mu sync.Mutex
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":%q}`, marker)
	}))
	defer daemon.Close()

	// The CLI reads both of these at parse time, so pinning them here is what
	// keeps the ambient environment out of the result.
	t.Setenv("LANTERND_URL", daemon.URL)
	t.Setenv("LANTERN_DAEMON_TOKEN", "")

	dataDir := t.TempDir()
	for _, command := range []string{"status", "discover", "peers", "files"} {
		t.Run(command, func(t *testing.T) {
			dir := filepath.Join(dataDir, "state-"+command)
			// No daemon flag: automatic routing is what is being tested.
			stdout, stderr, code := runCLI(t, "--json", "--data-dir", dir, command)

			if code == 0 {
				t.Fatalf("%s should not succeed against a failing daemon: %s", command, stdout)
			}
			out := stdout + stderr
			if !strings.Contains(out, marker) {
				t.Errorf("%s did not report the daemon's error, so it may never have reached the daemon: %q", command, out)
			}
			if strings.Contains(out, "pass --daemon") {
				t.Errorf("%s still demands the --daemon flag: %q", command, out)
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

	mu.Lock()
	defer mu.Unlock()
	if requests != 4 {
		t.Errorf("the test daemon saw %d requests, want one per command (4)", requests)
	}
}
