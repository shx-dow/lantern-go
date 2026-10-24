package daemonapp

// Tests for the HTTP listen address.
//
// The daemon used to pass the requested address straight to
// http.Server.ListenAndServe and print it back. With --addr 127.0.0.1:0 that
// printed http://127.0.0.1:0/ui, an address nobody can dial, and /v1/status
// had no field that reported the port the kernel actually chose. The startup
// line is the only place a caller learns the port, so it has to be the real
// one.

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// waitForHTTPAddr returns the host:port from the daemon's first listening
// line. It waits rather than assumes the line is already there.
func waitForHTTPAddr(t *testing.T, stderr *syncWriter) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out := stderr.String()
		if i := strings.Index(out, "http://"); i >= 0 {
			rest := out[i+len("http://"):]
			if j := strings.Index(rest, "/ui"); j > 0 {
				return rest[:j]
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("startup never printed a listening address; stderr:\n%s", stderr.String())
	return ""
}

// A daemon asked to bind port 0 must print the port the kernel chose, and
// /v1/status must report the same address plus its state directory. Before
// the fix the line said 127.0.0.1:0, so the HTTP call below could not even be
// made, and the status fields did not exist.
func TestStartupPrintsRealHTTPAddr(t *testing.T) {
	dir := t.TempDir()
	stderr := new(syncWriter)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, []string{
			"--addr", "127.0.0.1:0",
			"--p2p-port", "0",
			"--data-dir", dir,
			"--lan",
			"--token", testToken,
		}, stderr)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(15 * time.Second):
			t.Error("daemon did not shut down within 15s")
		}
	})

	addr := waitForHTTPAddr(t, stderr)
	if strings.HasSuffix(addr, ":0") {
		t.Fatalf("startup printed the unresolved port instead of the bound one: %s", addr)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		t.Fatalf("startup address %q is not host:port: %v", addr, err)
	}

	base := "http://" + addr
	var body string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		b, err := get(base + "/v1/status")
		if err == nil && b != "" {
			body = b
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if body == "" {
		t.Fatalf("the printed address never served /v1/status; stderr:\n%s", stderr.String())
	}

	var status struct {
		HTTPAddr string `json:"http_addr"`
		StateDir string `json:"state_dir"`
	}
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.HTTPAddr != addr {
		t.Errorf("status http_addr = %q, want the printed %q", status.HTTPAddr, addr)
	}
	if status.StateDir != dir {
		t.Errorf("status state_dir = %q, want %q", status.StateDir, dir)
	}
}

// A non-loopback HTTP bind must be refused unless the operator opts in. The
// API authenticates with a bearer token and serves it over cleartext HTTP, so
// binding it to a shared interface exposes the token. Before the fix this
// started and stayed up until the test context expired.
func TestNonLoopbackAddrRefusedWithoutOptIn(t *testing.T) {
	stderr := new(syncWriter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A deadline, not Background: if the refusal regresses the test must fail
	// in seconds with a clear name rather than hang in the daemon loop.
	runCtx, runCancel := context.WithTimeout(ctx, 5*time.Second)
	defer runCancel()

	err := Run(runCtx, []string{
		"--addr", "0.0.0.0:0",
		"--p2p-port", "0",
		"--data-dir", t.TempDir(),
		"--lan",
		"--token", testToken,
	}, stderr)
	if err == nil {
		t.Fatalf("a non-loopback bind started without --allow-remote; stderr:\n%s", stderr.String())
	}
	if !strings.Contains(err.Error(), "--allow-remote") {
		t.Errorf("refusal does not name the opt-in flag: %v", err)
	}
}

// requireLoopbackAddr is the policy behind the refusal. Literal loopback is
// accepted, the wildcard and routable addresses are not, and a malformed
// address is left to net.Listen so its error is about the syntax rather than
// about an opt-in flag.
func TestRequireLoopbackAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:1", "127.5.6.7:1", "[::1]:1", "localhost:1"} {
		if err := requireLoopbackAddr(addr, false); err != nil {
			t.Errorf("%s should be accepted as loopback: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:1", "[::]:1", "192.0.2.1:1"} {
		if err := requireLoopbackAddr(addr, false); err == nil {
			t.Errorf("%s should be refused without the opt-in", addr)
		}
		if err := requireLoopbackAddr(addr, true); err != nil {
			t.Errorf("%s should be accepted with the opt-in: %v", addr, err)
		}
	}
	if err := requireLoopbackAddr("not-an-address", false); err != nil {
		t.Errorf("a malformed address should reach net.Listen, got %v", err)
	}
}

// With the opt-in, a non-loopback bind starts and says what it exposes. This
// is a regression net, not a control: the flag did not exist before the fix.
func TestAllowRemoteBindsAndWarns(t *testing.T) {
	dir := t.TempDir()
	stderr := new(syncWriter)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, []string{
			"--addr", "0.0.0.0:0",
			"--allow-remote",
			"--p2p-port", "0",
			"--data-dir", dir,
			"--lan",
			"--token", testToken,
		}, stderr)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil && !strings.Contains(err.Error(), context.Canceled.Error()) {
				t.Errorf("daemon exited with %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("daemon did not shut down within 15s")
		}
	})

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out := stderr.String()
		if strings.Contains(out, "listening") {
			if !strings.Contains(out, "not loopback-only") || !strings.Contains(out, "cleartext") {
				t.Errorf("remote bind started without a warning that names the exposure:\n%s", out)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon never reported it was listening; stderr:\n%s", stderr.String())
}
