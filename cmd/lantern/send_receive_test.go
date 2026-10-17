package main

// Tests for `lantern send` and `lantern receive`.
//
// These two commands were at zero coverage, and the reason is worth recording.
// A driver needs the process-level stdout seam, while the property that
// mattered -- that send returns once the receiver has the file -- lives a layer
// down, in the serving device's session. So neither level could reach the
// other's test, and the commands were unreachable from either.
//
// Two things had to change for them to be reachable at all:
//
//   - runSend and runReceive reported failure through os.Exit directly while
//     every other command went through the exit seam, so even their usage paths
//     took the whole test binary down with them.
//   - runTransfer took a *lantern.Session but only ever read session.Events().
//     A session is a lifecycle with states and a Done channel, none of which
//     this loop uses, and sessions can only be built inside pkg/lantern. It now
//     takes the channel, which is what makes the branches below drivable.
//
// Both changes are the same call in production: exit is os.Exit, and the two
// call sites passed session.Events() before.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shx-dow/lantern-go/internal/lanterntest"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

func cliCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func evProgress(bytes, total int64) lantern.Event {
	return lantern.Event{Type: lantern.EventTransferProgress, FileName: "gift.txt", Bytes: bytes, Total: total}
}

func evDone(bytes, total int64) lantern.Event {
	return lantern.Event{Type: lantern.EventTransferDone, FileName: "gift.txt", Bytes: bytes, Total: total}
}

var errCLI = errors.New("transfer went wrong")

func evError(err error) lantern.Event {
	return lantern.Event{Type: lantern.EventError, Err: err}
}

// stringWriter collects what a JSON encoder writes, so the tests can assert on
// the stream rather than on the process's stdout. Encoding to the real stdout
// would interleave with anything else a test prints.
type stringWriter struct{ b *string }

func (w *stringWriter) Write(p []byte) (int, error) {
	*w.b += string(p)
	return len(p), nil
}

// fatal writes to stderr in human mode and to stdout in JSON mode, so the usage
// message follows whichever stream the caller asked for. Asserting the wrong one
// is how this first failed.
func TestSendWithNoPathIsAUsageError(t *testing.T) {
	p := lanterntest.NewPair(t)
	_, stderr, exitCode := capture(t, func() {
		runSend(cliCtx(t), p.Sender.Lantern, cliOptions{})
	})
	if exitCode != 1 {
		t.Fatalf("send with no path exited %d, want 1", exitCode)
	}
	if !strings.Contains(stderr, "usage: lantern send") {
		t.Fatalf("no usage message on stderr: %q", stderr)
	}

	// And in JSON mode, as an object on stdout, because an agent driving this
	// command cannot parse a bare sentence off stderr.
	stdout, _, jsonExit := capture(t, func() {
		runSend(cliCtx(t), p.Sender.Lantern, cliOptions{jsonOut: true})
	})
	if jsonExit != 1 {
		t.Fatalf("json send with no path exited %d, want 1", jsonExit)
	}
	if !strings.Contains(stdout, `"type":"error"`) || !strings.Contains(stdout, "usage: lantern send") {
		t.Fatalf("json mode did not report the usage error on stdout: %q", stdout)
	}
}

func TestReceiveWithNoCodeIsAUsageError(t *testing.T) {
	p := lanterntest.NewPair(t)
	_, stderr, exitCode := capture(t, func() {
		runReceive(cliCtx(t), p.Receiver.Lantern, cliOptions{})
	})
	if exitCode != 1 {
		t.Fatalf("receive with no code exited %d, want 1", exitCode)
	}
	if !strings.Contains(stderr, "usage: lantern receive") {
		t.Fatalf("no usage message on stderr: %q", stderr)
	}
}

// JSON mode is the machine-readable contract: one object per event, on stdout,
// with nothing else mixed in. A consumer parsing stdout depends on both halves.
func TestRunTransferRendersEveryEventAsJSONOnStdout(t *testing.T) {
	events := make(chan lantern.Event, 4)
	events <- evProgress(10, 100)
	events <- evDone(100, 100)
	close(events)

	failed, out, _ := captureRunTransferTo(t, events, true, "sent", "gift.txt")
	if failed {
		t.Error("a completed transfer reported failure")
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected one object per event, got %d: %q", len(lines), out)
	}

	var progress, done map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &progress); err != nil {
		t.Fatalf("progress line is not JSON: %v (%q)", err, lines[0])
	}
	if progress["type"] != "progress" || progress["bytes"].(float64) != 10 || progress["total"].(float64) != 100 {
		t.Fatalf("progress: %v", progress)
	}
	if err := json.Unmarshal([]byte(lines[1]), &done); err != nil {
		t.Fatalf("done line is not JSON: %v (%q)", err, lines[1])
	}
	if done["type"] != "done" || done["file_name"] != "gift.txt" || done["bytes"].(float64) != 100 {
		t.Fatalf("done: %v", done)
	}
}

// An error must be reported as an error, and must not also read as success.
// runSend exits on the return value, so reporting success here would exit 0 on
// a transfer that failed.
func TestRunTransferReportsAnErrorAsAFailure(t *testing.T) {
	events := make(chan lantern.Event, 4)
	events <- evError(errCLI)
	close(events)

	failed, out, _ := captureRunTransferTo(t, events, true, "sent", "gift.txt")
	if !failed {
		t.Error("an errored transfer reported success")
	}
	if !strings.Contains(out, `"type":"error"`) {
		t.Fatalf("the error was not reported on stdout: %q", out)
	}
	if strings.Contains(out, `"type":"done"`) {
		t.Fatalf("a failed transfer also reported done: %q", out)
	}
}

// A session whose channel closes without a terminal event is not a success.
// Returning false there would exit 0 having said nothing at all.
// A session whose channel closes without a terminal event is a failure.
//
// This was written the other way round first, asserting the behaviour rather
// than the contract: the test said returning false was right, which is success,
// so `lantern send` exited 0 having printed nothing. Found by running the
// suite repeatedly, where an aborted send intermittently took the closed-channel
// branch before the cancellation branch.
//
// A Done event is always emitted before the channel closes, so a close with no
// terminal event means there was no completion.
func TestRunTransferTreatsAClosedChannelAsAFailure(t *testing.T) {
	events := make(chan lantern.Event)
	close(events)

	failed, rendered, _ := captureRunTransferTo(t, events, true, "sent", "gift.txt")
	if !failed {
		t.Fatal("a transfer that ended without completing reported success")
	}
	if !strings.Contains(rendered, `"type":"error"`) {
		t.Fatalf("no error reported for an unterminated transfer: %q", rendered)
	}

	// And it says so in human mode too, rather than exiting quietly.
	stdout, stderr := captureHumanTransfer(t, events, "sent", "gift.txt")
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("human mode wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("no error on stderr: %q", stderr)
	}
}

// receive passes no name of its own, so the done event's file name is what gets
// reported. Getting this wrong prints an empty name to the user.
func TestRunTransferFallsBackToTheEventsFileName(t *testing.T) {
	events := make(chan lantern.Event, 2)
	events <- evDone(7, 7)
	close(events)

	_, stdout, _ := captureRunTransferTo(t, events, true, "received:", "")
	if !strings.Contains(stdout, `"file_name":"gift.txt"`) {
		t.Fatalf("the event's file name was not used: %q", stdout)
	}
}

// In human mode stdout carries the result and stderr carries the chatter.
// Mixing them makes `lantern send > out.txt` write a progress line into the
// file, which is the whole reason the two streams are split.
func TestRunTransferKeepsHumanProgressOffStdout(t *testing.T) {
	events := make(chan lantern.Event, 4)
	events <- evProgress(50, 100)
	events <- evDone(100, 100)
	close(events)

	stdout, stderr := captureHumanTransfer(t, events, "sent", "gift.txt")
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("human mode wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "progress:") {
		t.Errorf("no progress on stderr: %q", stderr)
	}
	if !strings.Contains(stderr, "sent gift.txt") {
		t.Errorf("no completion on stderr: %q", stderr)
	}
}

// captureRunTransferTo drives runTransfer with a scripted event stream and
// returns whether it reported failure, the JSON it rendered, and stderr.
//
// The encoder is pointed at a sink rather than the process's stdout so that a
// failure partway through a JSON document cannot interleave with the test's own
// output, and so the rendered stream can be parsed as a whole.
func captureRunTransferTo(t *testing.T, events <-chan lantern.Event, jsonOut bool, doneVerb, doneName string) (failed bool, rendered, stderr string) {
	t.Helper()
	var sink string
	_, stderr, _ = capture(t, func() {
		enc := json.NewEncoder(&stringWriter{&sink})
		failed = runTransfer(cliCtx(t), events, enc, jsonOut, doneVerb, doneName)
	})
	return failed, sink, stderr
}

func captureHumanTransfer(t *testing.T, events <-chan lantern.Event, doneVerb, doneName string) (stdout, stderr string) {
	t.Helper()
	stdout, stderr, _ = capture(t, func() {
		enc := json.NewEncoder(os.Stdout)
		runTransfer(cliCtx(t), events, enc, false, doneVerb, doneName)
	})
	return stdout, stderr
}
