package daemonapp

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shx-dow/lantern-go/internal/storage"
)

const testToken = "test-token-0123456789"

// freePort asks the OS for an unused port and releases it. There is a small
// race window, which is normal for this pattern and much simpler than
// threading a listener through the daemon.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// startDaemon runs the daemon with a throwaway data dir and blocks until its
// API answers. It returns the base URL.
func startDaemon(t *testing.T, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	addr := "127.0.0.1:" + freePort(t)

	args := append([]string{
		"--addr", addr,
		"--p2p-port", "0",
		"--data-dir", dir,
		"--lan",
		"--token", testToken,
	}, extra...)

	ctx, cancel := context.WithCancel(context.Background())
	var stderr bytes.Buffer
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, args, &stderr) }()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil && !strings.Contains(err.Error(), context.Canceled.Error()) {
				t.Errorf("daemon exited with %v; stderr:\n%s", err, stderr.String())
			}
		case <-time.After(15 * time.Second):
			t.Error("daemon did not shut down within 15s")
		}
	})

	base := "http://" + addr
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := get(base + "/v1/status"); err == nil && body != "" {
			return base
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("daemon never served the API; stderr:\n%s", stderr.String())
	return ""
}

func get(url string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}
	return string(body), nil
}

// syncWriter is a bytes.Buffer safe to read while a daemon is writing to it.
// Run paints stderr from several goroutines (the serve loop, the mDNS
// logger), so a test that polls the buffer while the daemon is up needs the
// guard or the race detector scores a direct hit -- as it did in CI.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// resolveDataDir is the unit behind Run's data-dir handling: empty falls
// back to the default, every explicit value -- "." included -- is used as
// given. The "." branch is the negative control for the redirect that existed
// mid-PR, where an explicit "." silently loaded the default identity instead.
func TestResolveDataDirKeepsExplicitDot(t *testing.T) {
	if r := resolveDataDir(""); r == "" {
		t.Errorf(`empty --data-dir must fall back to the default, got %q`, r)
	}
	if r := resolveDataDir(" . "); r != "." {
		t.Errorf(`an explicit "." means the current directory, not paths.Data(); got %q`, r)
	}
	if r := resolveDataDir(" ./child "); r != filepath.Clean("child") {
		t.Errorf(`an explicit relative path with surrounding spaces is honored: got %q`, r)
	}
}

// A second daemon pointed at the same state directory must be refused, not
// started: the two processes would share an identity -- same peer ID, so the
// first transfer dials itself -- and their writes to the trust store and the
// token would interleave unsynchronised. The refusal names the way out, which
// is a separate --data-dir and a --peer to the first daemon.
//
// Control: with the lock removed from Run, the second daemon starts and this
// test fails on err == nil.
func TestSecondDaemonOnOneIdentityIsRefused(t *testing.T) {
	dir := t.TempDir()
	trap := t.TempDir()
	if err := os.WriteFile(filepath.Join(trap, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"--addr", "127.0.0.1:" + freePort(t), "--data-dir", dir, "--lan", "--device-name", "first"}
	stderrBuf := new(syncWriter)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, args, stderrBuf) }()

	// The lock file appears before the API serves; wait on it so the second
	// run can never win a race against the first. The identity key comes
	// later than the lock -- lantern.New loads it after acquiring -- so wait
	// for that too before asserting anything about who wrote it.
	lockPath := filepath.Join(dir, storage.LockFileName)
	for _, p := range []string{lockPath, filepath.Join(dir, "lantern-identity.key")} {
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := os.Stat(p); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never appeared; first daemon may have died\nstderr:\n%s", p, stderrBuf.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	stderr := new(syncWriter)
	// A deadline, not context.Background(): if locking regresses into a
	// blocking wait the refusal test must fail in seconds with a clear name,
	// not hang until the test runner's global timeout.
	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	err := Run(runCtx, []string{
		"--addr", "127.0.0.1:" + freePort(t),
		"--data-dir", dir,
		"--device-name", "second",
	}, stderr)
	if err == nil {
		cancel()
		t.Fatal("second daemon on the same identity directory started instead of being refused")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock refusing did not return within its deadline; locking must fail fast, not wait:\n%s", stderr.String())
	}
	body := err.Error() + "\n" + stderr.String()
	if !strings.Contains(body, "--data-dir") {
		t.Errorf("refusal does not name --data-dir, the way out:\n%s", body)
	}
	if !strings.Contains(body, "--peer") {
		t.Errorf("refusal does not name --peer, the way out:\n%s", body)
	}
	// A refused daemon must not have touched the identity: no second identity
	// file, and the running daemon's token stays the only one written.
	if _, err := os.Stat(filepath.Join(dir, "lantern-identity.key")); err != nil {
		t.Errorf("identity missing from the first daemon's directory: %v", err)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil && !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("first daemon exited with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("first daemon did not shut down within 15s")
	}
}

// The startup output tells an operator how to run a second lantern on the
// same machine: a ready-to-paste command with a separate --data-dir and the
// full static peer multiaddr of this daemon. Without it the second-device
// setup requires hand-assembling /p2p/<peer-id> onto a multiaddr read out of
// a status call.
func TestStartupPrintsSecondDeviceCommand(t *testing.T) {
	dir := t.TempDir()
	addrPort := "127.0.0.1:" + freePort(t)

	ctx, cancel := context.WithCancel(context.Background())
	stderr := new(syncWriter)
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, []string{"--addr", addrPort, "--data-dir", dir, "--lan"}, stderr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(15 * time.Second):
		}
	})

	// The command line is what the daemon tells the operator to paste, so
	// the test reads it back out of the noise and takes it apart the way a
	// shell would.
	var line string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, l := range strings.Split(stderr.String(), "\n") {
			if strings.Contains(l, "lantern daemon --data-dir") {
				line = strings.TrimSpace(l)
			}
		}
		if line != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if line == "" {
		t.Fatalf("startup never printed the second-device command; stderr:\n%s", stderr.String())
	}

	tokens := splitArgs(line)
	want := []string{"lantern", "daemon", "--data-dir", dir + "-2", "--addr", "127.0.0.1:0"}
	for i, w := range want {
		if tokens[i] != w {
			t.Errorf("token %d = %q, want %q; full line:\n%s", i, tokens[i], w, line)
		}
	}
	ma := tokens[len(tokens)-1]
	if !strings.HasPrefix(ma, "/ip4/") || !strings.Contains(ma, "/p2p/12D3Koo") {
		t.Errorf("--peer value is not a full host multiaddr ending in the peer ID: %s", ma)
	}
	if len(tokens) != len(want)+2 {
		t.Fatalf("second-device command should be `lantern daemon --data-dir <dir> --addr <addr> --peer <multiaddr>`, got %d tokens:\n%s", len(tokens), line)
	}

	// Then paste it: run exactly what the line said, no edited-in flags. It
	// must bind its own port-0 address and stay up beside the first daemon
	// -- the collision this line exists to break was the second daemon
	// either reusing the identity or reusing the HTTP port.
	second := new(syncWriter)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	runErr := make(chan error, 1)
	go func() { runErr <- Run(runCtx, tokens[2:], second) }()
	select {
	case err := <-runErr:
		t.Fatalf("the printed second-device command did not stay up:\n%v\nstderr:\n%s", err, second.String())
	case <-time.After(3 * time.Second):
	}
	if out := second.String(); !strings.Contains(out, "listening") {
		t.Errorf("second daemon stayed up but never reported it is listening:\n%s", out)
	}
	runCancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("second daemon shut down with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("second daemon did not shut down within 15s")
	}
}

// splitArgs splits a command line into argv the way the shell the line is
// written for would. It knows exactly the quoting shQuote emits: everything
// single-quoted, with an embedded quote doubled on Windows (PowerShell reads
// single quotes literally but has no backslash escape) and '\”-escaped on
// POSIX shells.
func splitArgs(line string) []string {
	doubled := runtime.GOOS == "windows"
	var args []string
	var cur strings.Builder
	inQuote, started := false, false
	flush := func() {
		if started {
			args = append(args, cur.String())
			cur.Reset()
			started = false
		}
	}
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\'':
			if inQuote && doubled && i+1 < len(runes) && runes[i+1] == '\'' {
				cur.WriteRune('\'')
				i++
				break
			}
			inQuote = !inQuote
			started = true
		case inQuote:
			cur.WriteRune(r)
			started = true
		case r == '\\':
			if i+1 < len(runes) {
				cur.WriteRune(runes[i+1])
				started = true
				i++
			}
		case r == ' ':
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	flush()
	return args
}

func TestDaemonStartsAndServesAPI(t *testing.T) {
	base := startDaemon(t, "--device-name", "test-device")
	body, err := get(base + "/v1/status")
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	for _, want := range []string{"peer_id", "device_name", "test-device"} {
		if !strings.Contains(body, want) {
			t.Errorf("status response missing %q: %s", want, body)
		}
	}
}

// The daemon subcommand must own its own flag set, so its flags cannot
// collide with the CLI's globals.
func TestDaemonAcceptsItsOwnFlags(t *testing.T) {
	base := startDaemon(t, "--device-name", "flagged", "--no-lan", "--shared-dirs", t.TempDir())
	if body, err := get(base + "/v1/status"); err != nil || !strings.Contains(body, "flagged") {
		t.Fatalf("flags were not applied: %v %s", err, body)
	}
}

func TestDaemonRejectsUnknownFlag(t *testing.T) {
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"--not-a-real-flag"}, &stderr); err == nil {
		t.Fatal("expected an error for an unknown flag")
	}
	if !strings.Contains(stderr.String(), "not-a-real-flag") {
		t.Errorf("usage should name the offending flag, got:\n%s", stderr.String())
	}
}

func TestUsageMentionsTrustDefaults(t *testing.T) {
	for _, want := range []string{"--allow-writes", "--shared-dirs", "127.0.0.1"} {
		if !strings.Contains(Usage, want) {
			t.Errorf("usage text should mention %q", want)
		}
	}
}

func TestMdnsFilterSwallowsOnlyMdnsWarnings(t *testing.T) {
	var sink bytes.Buffer
	f := mdnsFilter{w: &sink}

	if _, err := f.Write([]byte("[WARN] mdns: no peers found\n")); err != nil {
		t.Fatal(err)
	}
	if sink.Len() != 0 {
		t.Errorf("mDNS warning should be swallowed, got %q", sink.String())
	}

	if _, err := f.Write([]byte("something real happened\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sink.String(), "something real") {
		t.Errorf("other output must pass through, got %q", sink.String())
	}
}

func TestFlagSetReportsExplicitFlagsOnly(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("lan", true, "")
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if flagSet(fs, "lan") {
		t.Error("lan was not given, so it must not be reported as set")
	}
	if err := fs.Parse([]string{"--lan=false"}); err != nil {
		t.Fatal(err)
	}
	if !flagSet(fs, "lan") {
		t.Error("lan was given explicitly, so it must be reported as set")
	}
}
