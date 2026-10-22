package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shx-dow/lantern-go/internal/daemonapp"
	"github.com/shx-dow/lantern-go/pkg/lanternclient"
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

// The daemon prints the token file path on startup, and that file is the only
// credential a fresh client has. A command run with no token flag and no token
// environment variable must find it on its own, or onboarding stops at a 401
// the user has no reason to understand.
//
// Control: without the token-file fallback the CLI sends no bearer token, the
// daemon answers 401, and this test fails on the exit code. Everything else
// here is the regression net: the printed path is the one the client reads,
// and it is under the daemon's data directory rather than the default.
func TestStatusFindsDaemonTokenFile(t *testing.T) {
	lanternBinary(t)

	// Point every platform's per-user data directory at a temp root, so the
	// daemon's default --data-dir and the CLI's default token path agree and
	// neither touches the real one.
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("USERPROFILE", filepath.Join(root, "home"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "localappdata"))
	t.Setenv("LANTERN_DAEMON_TOKEN", "")
	t.Setenv("LANTERND_TOKEN", "")
	t.Setenv("LANTERN_DAEMON_TOKEN_FILE", "")

	// Pick a port for the daemon's HTTP listener. It has a small race with
	// anything else binding between now and startup, which is the normal cost
	// of not threading a listener through the daemon.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	stderr := new(lockedWriter)
	errCh := make(chan error, 1)
	go func() {
		errCh <- daemonapp.Run(ctx, []string{"--addr", addr, "--p2p-port", "0", "--lan"}, stderr)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("daemon exited with %v; stderr:\n%s", err, stderr.String())
			}
		case <-time.After(20 * time.Second):
			t.Error("daemon did not shut down within 20s")
		}
	})

	var tokenPath string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if p := printedTokenPath(stderr.String()); p != "" {
			tokenPath = p
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if tokenPath == "" {
		t.Fatalf("daemon printed no token path; stderr:\n%s", stderr.String())
	}
	if want := lanternclient.TokenPath(); tokenPath != want {
		t.Errorf("printed token path = %q, want the default data dir's %q", tokenPath, want)
	}
	if _, err := os.Stat(tokenPath); err != nil {
		t.Fatalf("the printed token file does not exist: %v", err)
	}

	// The startup line is printed before ListenAndServe, so the token path is
	// not proof the API is answering yet. Poll with the token the daemon just
	// wrote; otherwise a slow start turns the one-shot CLI call into a
	// connection-refused failure that looks like a token bug.
	tokenData, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(tokenData))
	deadline = time.Now().Add(20 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		req, reqErr := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/status", nil)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, doErr := http.DefaultClient.Do(req)
		if doErr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("daemon never answered its API; stderr:\n%s", stderr.String())
	}

	t.Setenv("LANTERND_URL", "http://"+addr)

	// No --daemon-token, no --token-file, no token environment variable.
	stdout, stderrOut, code := runCLI(t, "--json", "status")
	if code != 0 {
		t.Fatalf("status exited %d with no token supplied; stderr:\n%s", code, stderrOut)
	}
	if !strings.Contains(stdout, "peer_id") {
		t.Errorf("status did not report the daemon's payload: %q", stdout)
	}
}

// printedTokenPath pulls the path out of the daemon's startup line. The line
// is human-facing, so the test reads it the way a person would rather than
// depending on a constant the daemon might later share with something else.
func printedTokenPath(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if i := strings.Index(line, "daemon token:"); i >= 0 {
			return strings.TrimSpace(line[i+len("daemon token:"):])
		}
	}
	return ""
}

// lockedWriter is a strings.Builder that a test may read while the daemon is
// still writing to it.
type lockedWriter struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
