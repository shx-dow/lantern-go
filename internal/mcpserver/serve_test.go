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
	"strings"
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
func TestServeStopsOnACancelledContext(t *testing.T) {
	// A reader that never returns data, so the loop is genuinely waiting.
	in := &neverEndingReader{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- Serve(ctx, in, &bytes.Buffer{}) }()

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serve returned nil for a cancelled context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop when its context was cancelled")
	}
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

// neverEndingReader satisfies io.Reader and never returns data or EOF, which
// is what a stdin looks like after the host stops writing to it.
type neverEndingReader struct{}

func (r *neverEndingReader) Read(p []byte) (int, error) {
	select {}
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
