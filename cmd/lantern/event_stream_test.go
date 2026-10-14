package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Streaming a transfer's progress is what an agent actually reads: `lantern
// send` is run, and the caller waits on the stream to learn the code and then
// whether the bytes moved. None of it was covered — runDaemonTransfer,
// streamDaemonEvents, emitDaemonProgress, emitDaemonRecord, isTerminal and
// progressPercent were all at zero, and they are reachable in process because
// the daemon client is just an HTTP client.

// eventDaemon serves GET /v1/events as server-sent events, and GET
// /v1/transfers/{id} as whatever the test says the transfer has become.
//
// events is what the stream sends. records is what a poll returns, in order,
// one per GET; the last is repeated once exhausted, so a test can describe a
// transfer that never moves.
type eventDaemon struct {
	*httptest.Server
	events   string
	records  []daemonRecord
	polls    int
	streamed int
}

// dial builds a client pointed at this daemon.
func (e *eventDaemon) dial(t *testing.T) *daemonClient {
	t.Helper()
	return newDaemonClient(e.URL, "")
}

func (e *eventDaemon) start(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/events", func(w http.ResponseWriter, r *http.Request) {
		e.streamed++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(e.events))
		if flusher != nil {
			flusher.Flush()
		}
	})
	mux.HandleFunc("/v1/transfers/", func(w http.ResponseWriter, r *http.Request) {
		idx := e.polls
		e.polls++
		if idx >= len(e.records) {
			idx = len(e.records) - 1
		}
		if idx < 0 {
			http.Error(w, `{"error":"no record"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(e.records[idx])
	})
	e.Server = httptest.NewServer(mux)
	t.Cleanup(e.Close)
}

// sse builds a server-sent events body from raw event lines. Each element is one
// "event: name" plus a "data: {json}" pair, separated by the blank line that
// terminates a frame.
func sse(frames ...string) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	return b.String()
}

// frame builds one server-sent event.
//
// The type goes in the payload as well as in the event name, because that is
// what the daemon does: EventDTO carries a `type` field, so the marshalled
// data already names the event. The CLI reads the type from the payload and
// ignores the event name entirely.
func frame(event string, payload map[string]any) string {
	payload["type"] = event
	data, _ := json.Marshal(payload)
	return "event: " + event + "\ndata: " + string(data)
}

// recordsOf decodes the JSON stream the CLI emits, so a test can assert on
// events rather than on substrings of them.
func recordsOf(t *testing.T, out string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("output line is not one JSON object: %v\n%s", err, line)
		}
		events = append(events, e)
	}
	return events
}

// bounded is a context with a deadline for calls that are meant to finish on
// their own.
//
// Without one, a regression that stops the CLI recognising a terminal frame
// does not fail an assertion — it sends the command into the polling fallback,
// where a daemon holding no record is an endless 404, and the test waits out
// the package timeout saying nothing. A failing test should name what broke.
func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// A transfer that finishes must say so, and the stream is the fast path a real
// caller is waiting on. If a "done" frame stops being honoured the command
// hangs until the context is cancelled and the caller learns nothing.
func TestStreamReportsDoneAndReportsItOnce(t *testing.T) {
	d := &eventDaemon{events: sse(
		frame("progress", map[string]any{"id": "t1", "file_name": "a.txt", "bytes": 5, "total": 10}),
		frame("done", map[string]any{"id": "t1", "file_name": "a.txt", "bytes": 10, "total": 10}),
	)}
	d.start(t)

	var out strings.Builder
	enc := json.NewEncoder(&out)
	failed := runDaemonTransfer(bounded(t), d.dial(t), enc, true, "t1", "sent", "")

	if failed {
		t.Error("a completed transfer was reported as failed")
	}
	events := recordsOf(t, out.String())
	if len(events) != 2 {
		t.Fatalf("expected progress then done, got %d events: %s", len(events), out.String())
	}
	if events[0]["type"] != "progress" || events[0]["bytes"].(float64) != 5 {
		t.Errorf("first event is not the progress report: %v", events[0])
	}
	if events[1]["type"] != "done" || events[1]["bytes"].(float64) != 10 {
		t.Errorf("last event is not the completion: %v", events[1])
	}
	// One read of the record, and it is the up-front check for a transfer that
	// is already terminal. The stream then reached a terminal frame, so there
	// was nothing to fall back to: a second read would mean the stream was
	// ignored and the answer came from the slower path instead.
	if d.polls != 1 {
		t.Errorf("the command read the transfer %d times; expected only the up-front check", d.polls)
	}
}

// A frame for a different transfer must not end this one. Two transfers can
// share a stream, and a "done" for the wrong id would end the wrong command.
func TestStreamIgnoresFramesForAnotherTransfer(t *testing.T) {
	d := &eventDaemon{events: sse(
		frame("progress", map[string]any{"id": "other", "bytes": 9, "total": 10}),
		frame("done", map[string]any{"id": "other", "file_name": "wrong.txt", "bytes": 10, "total": 10}),
		frame("done", map[string]any{"id": "mine", "file_name": "right.txt", "bytes": 3, "total": 3}),
	)}
	d.start(t)

	var out strings.Builder
	enc := json.NewEncoder(&out)
	runDaemonTransfer(bounded(t), d.dial(t), enc, true, "mine", "sent", "")

	events := recordsOf(t, out.String())
	last := events[len(events)-1]
	if last["type"] != "done" {
		t.Fatalf("the last event is not the completion: %v", last)
	}
	if last["file_name"] != "right.txt" {
		t.Errorf("another transfer's completion ended this one: %v", last)
	}
}

// A failed transfer has to be distinguishable from a completed one. Both exit
// non-zero at the process level, so the shape of the output is what a caller
// has to go on, and it carries the daemon's reason.
func TestStreamReportsFailureWithTheDaemonsReason(t *testing.T) {
	d := &eventDaemon{events: sse(
		frame("error", map[string]any{"id": "t1", "error": "peer went away"}),
	)}
	d.start(t)

	var out strings.Builder
	enc := json.NewEncoder(&out)
	failed := runDaemonTransfer(bounded(t), d.dial(t), enc, true, "t1", "sent", "")

	if !failed {
		t.Error("a failed transfer was reported as successful")
	}
	events := recordsOf(t, out.String())
	last := events[len(events)-1]
	if last["type"] != "error" {
		t.Fatalf("a failure did not emit an error event: %v", last)
	}
	if last["error"] != "peer went away" {
		t.Errorf("the daemon's reason was not carried through: %v", last)
	}
}

// A cancelled transfer is neither a success nor a failure, and the caller has to
// be able to tell the two apart to decide whether to retry.
func TestStreamReportsCancellationDistinctly(t *testing.T) {
	d := &eventDaemon{events: sse(
		frame("cancelled", map[string]any{"id": "t1"}),
	)}
	d.start(t)

	var out strings.Builder
	enc := json.NewEncoder(&out)
	failed := runDaemonTransfer(bounded(t), d.dial(t), enc, true, "t1", "sent", "")

	if !failed {
		t.Error("a cancelled transfer was reported as successful")
	}
	events := recordsOf(t, out.String())
	last := events[len(events)-1]
	if last["type"] != "cancelled" {
		t.Errorf("cancellation is not distinguishable from failure: %v", last)
	}
}

// A comment line is a keep-alive, not a frame. Treating it as one would either
// error out or end the stream on the first heartbeat.
func TestStreamIgnoresCommentLines(t *testing.T) {
	d := &eventDaemon{events: ": keep-alive\n\n" +
		sse(frame("done", map[string]any{"id": "t1", "file_name": "a.txt", "bytes": 1, "total": 1}))}
	d.start(t)

	var out strings.Builder
	enc := json.NewEncoder(&out)
	runDaemonTransfer(bounded(t), d.dial(t), enc, true, "t1", "sent", "")

	events := recordsOf(t, out.String())
	if len(events) != 1 || events[0]["type"] != "done" {
		t.Errorf("a keep-alive comment was treated as content: %s", out.String())
	}
}

// When the stream carries no terminal frame — it dropped, or the daemon died —
// the command has to fall back to polling rather than report nothing. This is
// the path that decides whether a transfer still running at the CLI's exit is
// reported honestly.
func TestStreamFallsBackToPollingWhenItDropsEarly(t *testing.T) {
	d := &eventDaemon{
		events: sse(frame("progress", map[string]any{"id": "t1", "bytes": 2, "total": 4})),
		records: []daemonRecord{
			{ID: "t1", State: "running", Bytes: 2, Total: 4},
			{ID: "t1", State: "done", FileName: "a.txt", Bytes: 4, Total: 4},
		},
	}
	d.start(t)

	var out strings.Builder
	enc := json.NewEncoder(&out)
	failed := runDaemonTransfer(bounded(t), d.dial(t), enc, true, "t1", "sent", "")

	if failed {
		t.Errorf("a transfer that completed while polling was reported as failed: %s", out.String())
	}
	if d.polls == 0 {
		t.Error("the stream ended without a terminal frame and nothing was polled")
	}
	events := recordsOf(t, out.String())
	if events[len(events)-1]["type"] != "done" {
		t.Errorf("the polled completion was not reported: %v", events)
	}
}

// Already-terminal-before-connecting is the fast path: a transfer that finished
// while nobody was listening must not wait for a stream that will never carry
// it.
func TestAlreadyTerminalTransferSkipsTheStream(t *testing.T) {
	d := &eventDaemon{
		events: sse(frame("progress", map[string]any{"id": "t1"})),
		records: []daemonRecord{
			{ID: "t1", State: "done", FileName: "a.txt", Bytes: 7, Total: 7},
		},
	}
	d.start(t)

	var out strings.Builder
	enc := json.NewEncoder(&out)
	failed := runDaemonTransfer(bounded(t), d.dial(t), enc, true, "t1", "sent", "")

	if failed {
		t.Error("an already-completed transfer was reported as failed")
	}
	if d.streamed != 0 {
		t.Errorf("a transfer that was already terminal opened the event stream anyway (%d times)", d.streamed)
	}
	events := recordsOf(t, out.String())
	if len(events) != 1 || events[0]["type"] != "done" {
		t.Errorf("expected exactly one completion event, got %s", out.String())
	}
}

// A cancelled context must stop the command. Without it a transfer whose daemon
// never reaches a terminal state runs until the process is killed, which is the
// hang a person hits when they press Ctrl-C.
func TestCancellationStopsAWaitingTransfer(t *testing.T) {
	// Never terminal, so the only way out is the context.
	d := &eventDaemon{
		events:  sse(frame("progress", map[string]any{"id": "t1", "bytes": 1, "total": 9})),
		records: []daemonRecord{{ID: "t1", State: "running", Bytes: 1, Total: 9}},
	}
	d.start(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	var out strings.Builder
	enc := json.NewEncoder(&out)
	done := make(chan bool, 1)
	go func() {
		done <- runDaemonTransfer(ctx, d.dial(t), enc, true, "t1", "sent", "")
	}()

	select {
	case failed := <-done:
		if !failed {
			t.Error("a cancelled transfer was reported as successful")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelling the context did not stop the transfer")
	}

	events := recordsOf(t, out.String())
	if len(events) == 0 || events[len(events)-1]["type"] != "cancelled" {
		t.Errorf("cancellation was not reported to the caller: %s", out.String())
	}
}

// The human-readable path prints to stderr and says the same things. It is the
// form a person sees, and it is a different code path from the JSON one, so
// neither covers the other.
func TestHumanStreamSaysWhatHappened(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events string
		want   string
		absent string
	}{
		{
			name:   "done",
			events: sse(frame("done", map[string]any{"id": "t1", "file_name": "report.txt", "bytes": 3, "total": 3})),
			want:   "sent report.txt",
		},
		{
			name:   "failed",
			events: sse(frame("error", map[string]any{"id": "t1", "error": "disk full"})),
			want:   "error: disk full",
		},
		{
			name:   "cancelled",
			events: sse(frame("cancelled", map[string]any{"id": "t1"})),
			want:   "cancelled",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &eventDaemon{events: tc.events}
			d.start(t)

			_, stderr, _ := capture(t, func() {
				var sink strings.Builder
				runDaemonTransfer(bounded(t), d.dial(t), json.NewEncoder(&sink), false, "t1", "sent", "")
			})

			if !strings.Contains(stderr, tc.want) {
				t.Errorf("output does not say %q:\n%s", tc.want, stderr)
			}
		})
	}
}

// Progress is written with a carriage return so it updates in place rather than
// scrolling. Losing that turns a transfer into a wall of lines.
func TestHumanProgressOverwritesRatherThanAppends(t *testing.T) {
	d := &eventDaemon{events: sse(
		frame("progress", map[string]any{"id": "t1", "file_name": "a.bin", "bytes": 1, "total": 4}),
		frame("progress", map[string]any{"id": "t1", "file_name": "a.bin", "bytes": 2, "total": 4}),
		frame("progress", map[string]any{"id": "t1", "file_name": "a.bin", "bytes": 4, "total": 4}),
		frame("done", map[string]any{"id": "t1", "file_name": "a.bin", "bytes": 4, "total": 4}),
	)}
	d.start(t)

	_, stderr, _ := capture(t, func() {
		var sink strings.Builder
		runDaemonTransfer(bounded(t), d.dial(t), json.NewEncoder(&sink), false, "t1", "sent", "")
	})

	// Split on the carriage return: what is left in each segment is what the
	// screen showed, in order. Asserting that some figure appears anywhere
	// would pass with the last update dropped, since the earlier ones are on
	// the way up and carry their own percentages.
	var shown []string
	for _, seg := range strings.Split(stderr, "\r") {
		if strings.Contains(seg, "progress:") {
			shown = append(shown, seg)
		}
	}
	if len(shown) < 3 {
		t.Fatalf("expected three progress updates, got %d: %q", len(shown), stderr)
	}
	// The final update is the full file, so it is the one left on screen.
	last := shown[len(shown)-1]
	if !strings.Contains(last, "100.0%") {
		t.Errorf("the last update shown is %q, want the completed 100.0%%", strings.TrimSpace(last))
	}
	// And the first is a quarter, so the sequence really is climbing rather
	// than repeating one figure.
	if !strings.Contains(shown[0], "25.0%") {
		t.Errorf("the first update is %q, want 25.0%%", strings.TrimSpace(shown[0]))
	}
}

// The verb is chosen by the caller — "sent" going out, "received:" coming in —
// so a human can tell the two apart. A completion that always said one of them
// would be a lie half the time.
func TestTheCompletionVerbComesFromTheCaller(t *testing.T) {
	for _, verb := range []string{"sent", "received:"} {
		t.Run(verb, func(t *testing.T) {
			d := &eventDaemon{events: sse(
				frame("done", map[string]any{"id": "t1", "file_name": "a.txt", "bytes": 1, "total": 1}),
			)}
			d.start(t)

			_, stderr, _ := capture(t, func() {
				var sink strings.Builder
				runDaemonTransfer(bounded(t), d.dial(t), json.NewEncoder(&sink), false, "t1", verb, "")
			})

			if !strings.Contains(stderr, verb+" a.txt") {
				t.Errorf("output does not use the caller's verb %q:\n%s", verb, stderr)
			}
		})
	}
}

// Every state the daemon can report has to map to exactly one outcome, or a
// caller cannot decide what to do next. isTerminal is what decides whether the
// command keeps waiting, so a state missing from it is a state that hangs.
func TestTerminalStatesAreExactlyTheThreeThatEndATransfer(t *testing.T) {
	terminal := []string{"done", "failed", "canceled"}
	for _, s := range terminal {
		if !isTerminal(s) {
			t.Errorf("%q does not end a transfer, so the command waits on it forever", s)
		}
	}
	for _, s := range []string{"running", "pending", "queued", "", "DONE", "done "} {
		if isTerminal(s) {
			t.Errorf("%q was treated as terminal", s)
		}
	}
}

// A terminal failure reported on the stream is terminal, not a dropped stream.
// It used to be treated as the latter: the command fell back to polling, and if
// the record was gone — a restarted daemon has no history, and history is
// capped — it polled until the context was cancelled, and reported the failure
// as a cancellation. The caller's reason was emitted and then overwritten.
func TestAFailedTransferIsNotPolledForAfterTheStreamSaysSo(t *testing.T) {
	d := &eventDaemon{
		events: sse(frame("error", map[string]any{"id": "t1", "error": "disk full"})),
		// The record has gone, as it would after a daemon restart.
		records: nil,
	}
	d.start(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var out strings.Builder
	enc := json.NewEncoder(&out)
	done := make(chan bool, 1)
	go func() { done <- runDaemonTransfer(ctx, d.dial(t), enc, true, "t1", "sent", "") }()

	var failed bool
	select {
	case failed = <-done:
	case <-time.After(4 * time.Second):
		t.Fatalf("the command kept polling after the stream reported a failure:\n%s", out.String())
	}

	if !failed {
		t.Error("a failed transfer was reported as successful")
	}
	if d.polls > 1 {
		t.Errorf("the stream reported a terminal failure, yet the command read the transfer %d times", d.polls)
	}

	events := recordsOf(t, out.String())
	last := events[len(events)-1]
	if last["type"] != "error" {
		t.Errorf("the failure was reported as %v; the reason the daemon gave is lost", last)
	}
}

// The same for a cancellation reported on the stream: it is already the answer,
// so falling back to polling only delays it.
func TestACancelledTransferIsNotPolledForAfterTheStreamSaysSo(t *testing.T) {
	d := &eventDaemon{events: sse(frame("cancelled", map[string]any{"id": "t1"}))}
	d.start(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var out strings.Builder
	enc := json.NewEncoder(&out)
	done := make(chan bool, 1)
	go func() { done <- runDaemonTransfer(ctx, d.dial(t), enc, true, "t1", "sent", "") }()

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatalf("the command kept polling after the stream reported a cancellation:\n%s", out.String())
	}
	if d.polls > 1 {
		t.Errorf("the stream reported a terminal cancellation, yet the record was read %d times", d.polls)
	}
	events := recordsOf(t, out.String())
	if last := events[len(events)-1]; last["type"] != "cancelled" {
		t.Errorf("the last event is %v, want a cancellation", last)
	}
}
func TestProgressPercentHandlesAnUnknownTotal(t *testing.T) {
	for _, tc := range []struct {
		bytes, total int64
		want         float64
	}{
		{5, 10, 50},
		{10, 10, 100},
		{0, 10, 0},
		{5, 0, 100},
		{5, -1, 100},
	} {
		if got := progressPercent(tc.bytes, tc.total); got != tc.want {
			t.Errorf("progressPercent(%d, %d) = %v, want %v", tc.bytes, tc.total, got, tc.want)
		}
	}
}

// A frame that is not valid JSON is a daemon bug, not a reason to end the
// transfer: the next frame may well be the completion.
func TestStreamSurvivesAMalformedFrame(t *testing.T) {
	d := &eventDaemon{events: sse(
		"event: progress\ndata: {not json",
		frame("done", map[string]any{"id": "t1", "file_name": "a.txt", "bytes": 1, "total": 1}),
	)}
	d.start(t)

	var out strings.Builder
	enc := json.NewEncoder(&out)
	runDaemonTransfer(bounded(t), d.dial(t), enc, true, "t1", "sent", "")

	events := recordsOf(t, out.String())
	if len(events) == 0 || events[len(events)-1]["type"] != "done" {
		t.Errorf("a malformed frame ended the stream early: %s", out.String())
	}
}

// An unknown event name is a daemon running a newer build than this CLI. It has
// to be ignored rather than treated as an error, or a downgrade silently breaks
// every transfer.
func TestStreamIgnoresAnEventItDoesNotKnow(t *testing.T) {
	d := &eventDaemon{events: sse(
		frame("quota-exceeded", map[string]any{"id": "t1"}),
		frame("done", map[string]any{"id": "t1", "file_name": "a.txt", "bytes": 1, "total": 1}),
	)}
	d.start(t)

	var out strings.Builder
	enc := json.NewEncoder(&out)
	runDaemonTransfer(bounded(t), d.dial(t), enc, true, "t1", "sent", "")

	events := recordsOf(t, out.String())
	if len(events) != 1 || events[0]["type"] != "done" {
		t.Errorf("an unknown event ended or polluted the stream: %s", out.String())
	}
}

// The event's type travels in the payload, not in the SSE event name. The
// daemon's EventDTO has a `type` field, so the marshalled data names the event
// and the `event:` line merely mirrors it. The CLI reads the payload.
//
// The two frames carry different file names and different byte counts, because
// the first one is *named* done and must be skipped. If the CLI read the event
// name instead it would stop on that first frame — and an assertion that only
// counted a single "done" event would still pass, since only one would be
// emitted either way. The outcome has to identify which frame was used.
func TestTheEventTypeComesFromThePayloadNotTheEventName(t *testing.T) {
	d := &eventDaemon{events: sse(
		// Named done, but the payload says nothing about it: skipped.
		"event: done\ndata: {\"id\":\"t1\",\"file_name\":\"skipped.txt\",\"bytes\":7,\"total\":7}",
		frame("done", map[string]any{"id": "t1", "file_name": "honoured.txt", "bytes": 3, "total": 3}),
	)}
	d.start(t)

	var out strings.Builder
	enc := json.NewEncoder(&out)
	runDaemonTransfer(bounded(t), d.dial(t), enc, true, "t1", "sent", "")

	events := recordsOf(t, out.String())
	if len(events) != 1 {
		t.Fatalf("expected exactly one event, got %d: %s", len(events), out.String())
	}
	got := events[0]
	if got["type"] != "done" {
		t.Errorf("the ending is not a completion: %v", got)
	}
	if got["file_name"] != "honoured.txt" {
		t.Errorf("ended on the frame named by its event name, not its payload: %v", got)
	}
	if got["bytes"].(float64) != 3 {
		t.Errorf("the wrong frame supplied the result: %v", got)
	}
}
func TestTheKnownFileNameOverridesTheStream(t *testing.T) {
	for _, tc := range []struct {
		name, known, streamed, want string
	}{
		{"caller knows the name", "notes.md", "wrong.txt", "notes.md"},
		{"caller does not", "", "wrong.txt", "wrong.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &eventDaemon{events: sse(
				frame("done", map[string]any{"id": "t1", "file_name": tc.streamed, "bytes": 1, "total": 1}),
			)}
			d.start(t)

			var out strings.Builder
			enc := json.NewEncoder(&out)
			runDaemonTransfer(bounded(t), d.dial(t), enc, true, "t1", "sent", tc.known)

			events := recordsOf(t, out.String())
			if got := events[len(events)-1]["file_name"]; got != tc.want {
				t.Errorf("completion named %v, want %q", got, tc.want)
			}
		})
	}
}
