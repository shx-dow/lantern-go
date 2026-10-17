package main

// The transfer that `lantern send` and `lantern receive` exist for, end to end
// in process.
//
// This replaces a subprocess version that could not work. Two Lantern
// processes on one machine cannot form a sender/receiver pair at all: --data-dir
// holds both the identity key and the rendezvous directory, so sharing it to
// make discovery work also makes them the same peer, and the receiver dies with
// "dial to self attempted". Giving each process its own data dir puts discovery
// back on mDNS and the public DHT, which is too slow and too flaky to be a
// test. That is a limitation of the product rather than of the test, and it is
// the same question this project has been carrying since the start: should the
// rendezvous directory be shareable independently of identity?
//
// It also replaces an earlier in-process version that drove this through the
// capture helper. capture replaces os.Stdout, os.Stderr and the exit seam with
// process globals, and libp2p reads os.Stdout from its own goroutines while a
// connection is established. That was a data race on two of three platforms,
// and it could not be locked away: the other side of the race is a library that
// has no way to take the lock.
//
// Only the exit seam is swapped here. libp2p never reads it, so nothing races.
// The cost is that the command's own output lands in the test's real stdout.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shx-dow/lantern-go/internal/lanterntest"
)

// captureExit runs fn with only the exit seam replaced, and reports the code it
// asked to exit with. os.Stdout and os.Stderr are left alone on purpose.
func captureExit(fn func()) (code int) {
	real := exit
	exit = func(c int) { code = c }
	defer func() {
		exit = real
		if r := recover(); r != nil {
			if e, ok := r.(exitPanic); ok {
				code = e.code
				return
			}
			panic(r)
		}
	}()
	fn()
	return code
}

// `lantern send` must return once the receiver has taken the file.
//
// This is the property #49 was about. A paired receiver takes the read path,
// which never touched the code that reports a sender's progress, so the sender
// waited forever on an event that could not arrive -- after a transfer that had
// already succeeded.
var errNoCode = errors.New("the sender advertised no share code")

func TestSendReturnsOnceTheReceiverHasTakenTheFile(t *testing.T) {
	shared := t.TempDir()
	const name = "gift.txt"
	const content = "the bytes that must land"
	if err := os.WriteFile(filepath.Join(shared, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	p := lanterntest.NewPair(t, shared)
	out := t.TempDir()

	type outcome struct {
		code string
		err  error
	}
	result := make(chan outcome, 1)

	// The goroutine deliberately touches no *testing.T: Fatalf from a
	// non-test goroutine calls Goexit on the wrong one.
	go func() {
		// The code only exists once the sender has advertised, so it is polled
		// for in the sender's own rendezvous directory rather than read from
		// the command's output, which this test deliberately does not capture.
		code := awaitShareCode(p)
		if code == "" {
			result <- outcome{err: errNoCode}
			return
		}
		p.BridgeDiscovery(t, code)
		received, _, err := p.Receiver.Lantern.ReceiveSession(lanterntest.Ctx(t), code, out)
		if err != nil {
			result <- outcome{err: err}
			return
		}
		for range received.Events() {
		}
		received.Close()
		result <- outcome{code: code}
	}()

	var sendCode int
	done := make(chan struct{})
	go func() {
		defer close(done)
		sendCode = captureExit(func() {
			runSend(lanterntest.Ctx(t), p.Sender.Lantern, cliOptions{positional: []string{filepath.Join(shared, name)}})
		})
	}()

	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("send never returned after the receiver had taken the file")
	}

	res := <-result
	if res.err != nil {
		t.Fatalf("the receiver did not complete: %v", res.err)
	}
	if sendCode != 0 {
		t.Fatalf("send exited %d", sendCode)
	}

	got, err := os.ReadFile(filepath.Join(out, name))
	if err != nil {
		t.Fatalf("the receiver never got the file: %v", err)
	}
	if string(got) != content {
		t.Fatalf("the receiver got %q", got)
	}
}

// awaitShareCode finds the code runSend minted, from the rendezvous file the
// sender wrote. No *testing.T, because this runs off the test goroutine.
func awaitShareCode(p lanterntest.Pair) string {
	dir := p.Sender.DataDir
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				rest, ok := strings.CutPrefix(e.Name(), "lantern-")
				if !ok {
					continue
				}
				if code, ok := strings.CutSuffix(rest, ".peer"); ok && code != "" {
					return code
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return ""
}
