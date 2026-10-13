package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Driving a command in process needs three things the binary-level tests
// cannot have: the output captured as a string, the exit code, and the daemon
// answering. This file provides those, so the command surface is reachable at
// all.
//
// Without them every command is tested only by running the built binary, where
// coverage instrumentation cannot see it. That is why cmd/lantern sat at 15%
// with a parser suite and no test of a single command.

// exitPanic is what the exit seam throws instead of ending the process.
type exitPanic struct{ code int }

// capture runs fn with stdout and stderr redirected, and returns what it
// printed and the code it asked to exit with.
func capture(t *testing.T, fn func()) (stdout, stderr string, code int) {
	t.Helper()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	realOut, realErr, realExit := os.Stdout, os.Stderr, exit
	os.Stdout, os.Stderr = outW, errW
	exit = func(c int) { panic(exitPanic{code: c}) }

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errCh <- string(b) }()

	func() {
		defer func() {
			os.Stdout, os.Stderr, exit = realOut, realErr, realExit
			_ = outW.Close()
			_ = errW.Close()
			if r := recover(); r != nil {
				if e, ok := r.(exitPanic); ok {
					code = e.code
					return
				}
				panic(r)
			}
		}()
		fn()
	}()

	return <-outCh, <-errCh, code
}

// drive parses args, runs the command, and reports what a person would see.
func drive(t *testing.T, d *fakeDaemon, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	opts, err := parseArgs(append([]string{"--daemon-url", d.URL}, args...))
	if err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return capture(t, func() { runDaemonCommand(t.Context(), opts) })
}

// A status that renders correctly is the whole of what `lantern status` is for.
func TestStatusRendersTheDeviceTheDaemonDescribes(t *testing.T) {
	d := newFakeDaemon(t)
	stdout, stderr, code := drive(t, d, "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"laptop", "12D3kooWRIGHT", "lan_only: true", "/ip4/127.0.0.1/tcp/4001"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output is missing %q:\n%s", want, stdout)
		}
	}
}

// Every daemon command, in process. This is the table the binary-level test
// also walks, and it exists here because this one can be measured.
func TestDaemonCommandsRenderWhatTheDaemonSent(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		wants []string
	}{
		{"status", []string{"status"}, []string{"laptop", "12D3kooWRIGHT"}},
		{"discover", []string{"discover"}, []string{"self: laptop", "12D3kooONE", "12D3kooTWO"}},
		{"peers", []string{"peers"}, []string{"12D3kooONE", "12D3kooTWO"}},
		{"list transfers", []string{"list"}, []string{"report.txt"}},
		{"list history", []string{"list", "history"}, []string{"gone.txt"}},
		{"list shares", []string{"list", "shares"}, []string{"shared.txt"}},
		{"trust list", []string{"trust", "list"}, []string{"nas", "read-write"}},
		{"files", []string{"files"}, []string{"notes.md"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newFakeDaemon(t)
			stdout, stderr, code := drive(t, d, c.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			for _, want := range c.wants {
				if !strings.Contains(stdout, want) {
					t.Errorf("output is missing %q:\n%s", want, stdout)
				}
			}
		})
	}
}

// The empty result is the first thing a new user sees, and it is the one a
// table of happy paths never writes.
func TestEmptyResultsSaySoRatherThanPrintingNothing(t *testing.T) {
	cases := []struct {
		name string
		body string
		args []string
		want string
	}{
		{"peers", `{"peers":[]}`, []string{"peers"}, "no connected peers"},
		{"trust", `{"trusted":[]}`, []string{"trust", "list"}, "no"},
		{"transfers", `{"transfers":[]}`, []string{"list"}, "no"},
		{"files", `{"files":[]}`, []string{"files"}, "no"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newEmptyDaemon(t, c.body)
			stdout, stderr, code := drive(t, d, c.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if strings.TrimSpace(stdout) == "" {
				t.Fatalf("printed nothing at all:\n%s", stderr)
			}
			if !strings.Contains(strings.ToLower(stdout), c.want) {
				t.Errorf("output does not say %q:\n%s", c.want, stdout)
			}
		})
	}
}

// A refusal is a different situation from an empty result and must not look
// like one. This is the failure a person hits when the daemon is stopped.
func TestARefusalIsNotAnEmptyResult(t *testing.T) {
	d := newFakeDaemon(t)
	d.fail("/v1/status", 503)

	stdout, stderr, code := drive(t, d, "status")
	if code != 1 {
		t.Errorf("a refusal should exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "refused on purpose") {
		t.Errorf("the daemon's own message was lost: %q", stderr)
	}
	if strings.Contains(stdout, "lan_only") {
		t.Errorf("a refusal rendered as a status: %q", stdout)
	}
}

// And under --json it stays JSON, because that is the form an agent reads and
// the one it has to be able to parse on the failure path too.
func TestARefusalIsJSONUnderJSON(t *testing.T) {
	d := newFakeDaemon(t)
	d.fail("/v1/status", 500)

	stdout, _, code := drive(t, d, "--json", "status")
	if code != 1 {
		t.Errorf("a refusal should exit 1, got %d", code)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &decoded); err != nil {
		t.Fatalf("--json failure is not one JSON object: %v\n%s", err, stdout)
	}
	if decoded["error"] == nil {
		t.Errorf("a JSON failure carries no error field: %v", decoded)
	}
	if decoded["type"] != "error" {
		t.Errorf("a JSON failure is not typed as an error: %v", decoded)
	}
}

// --json has to be JSON and have no prose mixed in, which is the property that
// makes it usable by anything other than a person.
func TestJSONOutputIsJSONAndNothingElse(t *testing.T) {
	d := newFakeDaemon(t)
	stdout, _, code := drive(t, d, "--json", "status")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var first map[string]any
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("--json status is not JSON: %v\n%s", err, stdout)
	}
	if err := dec.Decode(&first); err != io.EOF {
		t.Errorf("more than one JSON value on stdout, or trailing prose: %v\n%s", err, stdout)
	}
}

// Human output must not contain JSON, or a person reads a wall of braces and
// an agent that forgot --json parses something nobody meant to send.
func TestHumanOutputIsNotJSON(t *testing.T) {
	d := newFakeDaemon(t)
	stdout, _, _ := drive(t, d, "status")
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("human output is JSON without --json:\n%s", stdout)
	}
}

// newEmptyDaemon answers one route with an empty result and nothing else, for
// the first-run case.
func newEmptyDaemon(t *testing.T, body string) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{failures: map[string]int{}, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.record(r)
		for prefix, code := range d.failures {
			if strings.HasPrefix(r.URL.Path, prefix) {
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"error":"refused on purpose"}`))
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	d.Server = srv
	t.Cleanup(srv.Close)
	return d
}
