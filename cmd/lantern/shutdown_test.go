//go:build unix

package main

import (
	"bytes"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// syncBuffer collects a subprocess's stderr while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestSigtermShutsDaemonDownGracefully is the negative control for SIGTERM
// handling. Before the fix only os.Interrupt was registered, so SIGTERM kept
// its default action and killed the process: Wait returned "signal:
// terminated" rather than a clean exit, and the daemon never printed that it
// was shutting down. That is how systemd and Docker stop a service.
func TestSigtermShutsDaemonDownGracefully(t *testing.T) {
	stderr := new(syncBuffer)
	cmd := exec.Command(lanternBinary(t),
		"daemon",
		"--addr", "127.0.0.1:0",
		"--p2p-port", "0",
		"--data-dir", t.TempDir(),
		"--lan",
		"--token", "test-token",
	)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// The daemon is ready when it reports the address it actually bound.
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(stderr.String(), "listening on http://") {
		if time.Now().After(deadline) {
			t.Fatalf("daemon never reported it was listening; stderr:\n%s", stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SIGTERM did not shut the daemon down cleanly: %v; stderr:\n%s", err, stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("daemon still running 20s after SIGTERM; stderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "shutting down") {
		t.Errorf("daemon exited 0 without reporting a shutdown; stderr:\n%s", stderr.String())
	}
}
