package daemonapp

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
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
