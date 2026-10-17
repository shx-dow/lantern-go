package mcpserver

// Tests for Serve, the JSON-RPC loop.
//
// handle was covered -- tests called it directly and asserted on the response
// it produced. Serve itself was not, at 0%, and the two are different jobs.
// handle decides what to answer; Serve decides how requests are framed on the
// wire, what happens to input that is not a request, and when to stop.
//
// The framing is the part an agent actually depends on. A server that answered
// a blank line, or died on one malformed line, or held its last response in a
// buffer, would pass every handle test and fail every real client.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// idIs compares a JSON-RPC id against an int.
//
// The id field is `any` on both the request and the response, so a numeric id
// comes back from json.Unmarshal as float64. Comparing it to an int is false
// even when the two print identically, which is a confusing enough failure to
// be worth naming rather than working around quietly.
func idIs(got any, want int) bool {
	switch v := got.(type) {
	case float64:
		return v == float64(want)
	case int:
		return v == want
	case int64:
		return v == int64(want)
	case json.Number:
		return v.String() == fmt.Sprint(want)
	default:
		return false
	}
}

// rpcLines parses the responses Serve wrote.
func rpcLines(t *testing.T, out string) []rpcResponse {
	t.Helper()
	var resps []rpcResponse
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r rpcResponse
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("response line is not JSON: %v (%q)", err, line)
		}
		resps = append(resps, r)
	}
	return resps
}

// A request on stdin produces exactly one response on stdout, and it arrives
// before Serve returns.
func TestServeAnswersARequest(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	var out bytes.Buffer

	if err := Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	resps := rpcLines(t, out.String())
	if len(resps) != 1 {
		t.Fatalf("expected one response, got %d: %q", len(resps), out.String())
	}
	if resps[0].Error != nil {
		t.Fatalf("initialize errored: %+v", resps[0].Error)
	}
	if !idIs(resps[0].ID, 1) {
		t.Fatalf("response id is %v (%T), want 1", resps[0].ID, resps[0].ID)
	}
}

// Every request gets its own answer, in order.
//
// This is framing, not handle: two requests in one stream must not collapse
// into one response, which is what would happen if the id were dropped or the
// loop reused a buffer.
func TestServeAnswersEachRequestInOrder(t *testing.T) {
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"initialize"}` + "\n")

	var out bytes.Buffer
	if err := Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	resps := rpcLines(t, out.String())
	if len(resps) != 3 {
		t.Fatalf("expected three responses, got %d: %q", len(resps), out.String())
	}
	for i, want := range []int{1, 2, 3} {
		if !idIs(resps[i].ID, want) {
			t.Fatalf("response %d has id %v (%T), want %d", i, resps[i].ID, resps[i].ID, want)
		}
		if resps[i].Error != nil {
			t.Fatalf("request %d errored: %+v", want, resps[i].Error)
		}
	}
}

// Blank lines are not requests and must not be answered.
//
// An agent's transport pads. Answering a blank line would put a response in
// the stream with no request behind it, and an MCP client that reads
// request/response pairs would then be permanently one behind.
func TestServeIgnoresBlankLines(t *testing.T) {
	in := strings.NewReader("\n\n   \n" +
		`{"jsonrpc":"2.0","id":7,"method":"initialize"}` +
		"\n\n\n")

	var out bytes.Buffer
	if err := Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	resps := rpcLines(t, out.String())
	if len(resps) != 1 {
		t.Fatalf("blank lines produced %d responses, want 1: %q", len(resps), out.String())
	}
	if !idIs(resps[0].ID, 7) {
		t.Fatalf("response id is %v, want 7", resps[0].ID)
	}
}

// A malformed line is skipped, and the loop keeps serving.
//
// Silently continuing rather than erroring is the right call for a stdio
// transport: the alternative is that one bad line from a client permanently
// kills the session, with nothing on stdout to say why.
func TestServeSkipsMalformedInputAndKeepsGoing(t *testing.T) {
	in := strings.NewReader(
		"this is not json\n" +
			`{"jsonrpc":"2.0","id":1,` + "\n" + // truncated
			`{"jsonrpc":"2.0","id":2,"method":"initialize"}` + "\n" +
			"[1,2,3]\n")

	var out bytes.Buffer
	if err := Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	resps := rpcLines(t, out.String())
	if len(resps) != 1 {
		t.Fatalf("expected one response for the one valid request, got %d: %q", len(resps), out.String())
	}
	if !idIs(resps[0].ID, 2) {
		t.Fatalf("the surviving response is for id %v, want 2", resps[0].ID)
	}
}

// An unknown method is answered with an error rather than dropped.
//
// A dropped request is unrecoverable for a client: it has no response to
// correlate and no error to show. This is the difference between a tool call
// that fails and a session that hangs.
func TestServeAnswersUnknownMethodsWithAnError(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"no/such/method"}` + "\n")
	var out bytes.Buffer

	if err := Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	resps := rpcLines(t, out.String())
	if len(resps) != 1 {
		t.Fatalf("expected one response, got %d", len(resps))
	}
	if resps[0].Error == nil {
		t.Fatalf("an unknown method was answered without an error: %+v", resps[0])
	}
}

// A cancelled context ends the loop, rather than blocking on a stdin that
// will never deliver again.
//
// The host going away is the normal way this process ends. Reporting it means
// the shim can exit non-zero and something upstream learns why.
//
// The reader is a pipe rather than a reader that blocks forever, because a
// reader blocked in Read cannot be released by closing a channel: the reader
// goroutine stays parked, holding its scanner buffer, for the life of the test
// binary. Closing the pipe is what a real stdin needs too, and it is the same
// mechanism Serve uses.
func TestServeStopsOnACancelledContext(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()

	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- Serve(ctx, reader, &out) }()

	_, _ = io.WriteString(writer, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n")
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serve returned nil for a cancelled context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop when its context was cancelled")
	}

	// The pipe read returns once Serve has closed it, which is what proves the
	// reader was not simply abandoned. A goroutine parked in Read cannot be
	// released by closing a channel; only closing the reader reaches it.
	//
	// The read is bounded, because without the close it does not return at all
	// and the failure would be the whole test binary timing out rather than
	// this one test saying what was wrong.
	released := make(chan error, 1)
	go func() {
		_, err := reader.Read(make([]byte, 1))
		released <- err
	}()
	select {
	case err := <-released:
		if err == nil {
			t.Fatal("the pipe is still open, so Serve left its reader blocked in Read")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve never closed its reader, so the reader goroutine is still parked in Read")
	}
}

// A request that is already queued when cancellation lands must not start.
//
// Reading ahead means the next request can be sitting in the channel before
// ctx is cancelled, and then both select cases are ready -- so the select may
// choose the request. That request can be a share or a fetch, so cancelling a
// session would still begin work the caller asked it not to.
//
// The window is held open deterministically by parking handle inside a daemon
// call: the first request is a tools/call against a server that blocks until
// the test releases it, and the second request is queued behind it. Cancelling
// in between is then a certainty rather than a race with the clock -- an
// earlier version of this test just slept, and the requests finished before
// the cancel arrived.
func TestServeDoesNotStartAQueuedRequestAfterCancellation(t *testing.T) {
	// Repeated, because with the check missing this is a coin flip rather than
	// a certainty: once cancellation and the queued request are both ready, Go
	// picks between the select arms at random. Blocking the daemon call holds
	// the window open, but it does not decide the choice. Measured at three
	// failures in ten rounds, so forty rounds miss it about once in a billion.
	//
	// It ran only once for a while, which made it a test that passed by luck
	// roughly half the time and looked green.
	const rounds = 40
	for round := range rounds {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			queuedRequestMustNotStart(t, round)
		})
	}
}

func queuedRequestMustNotStart(t *testing.T, round int) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"peer_id": "p1"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("LANTERND_URL", srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	defer writer.Close()

	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, reader, &out) }()

	// The first request parks inside handle; the second is read ahead.
	_, _ = io.WriteString(writer,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"status","arguments":{}}}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n")

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first request never reached the daemon")
	}

	// Cancelled while handle is still blocked, with request 2 queued.
	cancel()
	close(release)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serve returned nil for a cancelled context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop when its context was cancelled")
	}

	for _, r := range rpcLines(t, out.String()) {
		if idIs(r.ID, 2) {
			t.Fatal("a request queued before cancellation was started anyway")
		}
	}

	// The reader is released on this path too, not only when the select takes
	// the cancellation arm. Which arm it takes is arbitrary, so this has to
	// hold either way.
	releasedRead := make(chan error, 1)
	go func() {
		_, err := reader.Read(make([]byte, 1))
		releasedRead <- err
	}()
	select {
	case err := <-releasedRead:
		if err == nil {
			t.Fatal("the pipe is still open, so Serve left its reader blocked in Read")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve never closed its reader, so the reader goroutine is still parked in Read")
	}
	_ = round
}

// Empty input is not an error. A client that opens the stream and closes it
// cleanly should not see the server report a failure.
func TestServeAcceptsEmptyInput(t *testing.T) {
	var out bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(""), &out); err != nil {
		t.Fatalf("empty input reported %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("empty input produced output: %q", out.String())
	}
}

// stringList distinguishes an absent argument from an empty one, and the
// distinction is the whole point of the helper: clearing a restriction sends an
// empty list, leaving it alone sends nothing, and a caller that cannot tell
// those apart either refuses to clear or silently clears everything.
func TestStringListSeparatesAbsentFromEmpty(t *testing.T) {
	tests := []struct {
		name      string
		in        any
		wantOK    bool
		wantItems []string
	}{
		{name: "absent", in: nil, wantOK: false},
		{name: "wrong type", in: "not-a-list", wantOK: false},
		{name: "a number", in: 42, wantOK: false},
		{name: "empty list is present and empty", in: []any{}, wantOK: true, wantItems: []string{}},
		{name: "one item", in: []any{"a"}, wantOK: true, wantItems: []string{"a"}},
		{name: "several", in: []any{"a", "b"}, wantOK: true, wantItems: []string{"a", "b"}},
		// Blank entries are dropped rather than becoming empty strings: an
		// empty string is not a restriction, and passing one on to the daemon
		// would be a request to restrict nothing while claiming to restrict
		// something.
		{name: "blank entries dropped", in: []any{"a", "", "b"}, wantOK: true, wantItems: []string{"a", "b"}},
		{name: "non-string entries dropped", in: []any{"a", 1, true}, wantOK: true, wantItems: []string{"a"}},
		{name: "only blanks is present and empty", in: []any{"", ""}, wantOK: true, wantItems: []string{}},
	}
	for _, tc := range tests {
		got, ok := stringList(tc.in)
		if ok != tc.wantOK {
			t.Errorf("%s: present=%v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if len(got) != len(tc.wantItems) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.wantItems)
			continue
		}
		for i := range got {
			if got[i] != tc.wantItems[i] {
				t.Errorf("%s: got %v, want %v", tc.name, got, tc.wantItems)
				break
			}
		}
	}
}
