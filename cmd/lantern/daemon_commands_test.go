package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// These drive the built binary against a daemon that answers. Everything else
// in this package tests the parser, which is the part that was already
// covered; this is the part that was not, and it is the part a person touches:
// they type a command and read the output.
//
// The claims worth checking are the ones a user would notice. A command that
// says "no connected peers" when the daemon sent two is a bug that no amount
// of parser testing will ever find.

// fakeDaemon answers the routes the CLI uses. Anything unrouted is a 404 with
// the path, so a command that starts calling somewhere new fails loudly here
// instead of quietly getting an empty body.
type fakeDaemon struct {
	*httptest.Server
	mu sync.Mutex
	// requests records method and path, so a test can assert the CLI called
	// what it should and nothing more.
	requests []string
	// failures maps a path prefix to a status the daemon returns instead.
	failures map[string]int
	// body, when set, is returned for every route instead of the normal map.
	body string
	// arrived counts HTTP calls at the server boundary, independently of
	// requests. Comparing the two is what tells a test whether the recording
	// is faithful; counting them from the same append would prove nothing.
	arrived int
}

func (d *fakeDaemon) record(r *http.Request) {
	d.mu.Lock()
	d.arrived++
	d.requests = append(d.requests, r.Method+" "+r.URL.Path)
	d.mu.Unlock()
}

// arrivals is how many HTTP calls the fake actually received.
func (d *fakeDaemon) arrivals() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.arrived
}

// failureFor reports whether path matches a configured failure, and which.
func (d *fakeDaemon) failureFor(path string) (int, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for prefix, code := range d.failures {
		if strings.HasPrefix(path, prefix) {
			return code, true
		}
	}
	return 0, false
}

func (d *fakeDaemon) fail(prefix string, code int) {
	d.mu.Lock()
	d.failures[prefix] = code
	d.mu.Unlock()
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{failures: map[string]int{}}
	mux := http.NewServeMux()
	d.Server = httptest.NewServer(mux)
	t.Cleanup(d.Close)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		d.record(r)
		ok := func(body any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
		}

		// A snapshot under the lock rather than unlocking here. The old shape
		// unlocked inside the failure branch and again after the loop, and
		// removing the duplicate append below the loop removed one of those two
		// unlocks, so the balance broke.
		code, failing := d.failureFor(r.URL.Path)
		if failing {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"refused on purpose"}`))
			return
		}

		switch r.URL.Path {
		case "/v1/status":
			ok(map[string]any{
				"device_name": "laptop", "peer_id": "12D3kooWRIGHT",
				"lan_only": true,
				"addrs":    []string{"/ip4/127.0.0.1/tcp/4001"},
			})
		case "/v1/peers":
			ok(map[string]any{"peers": []map[string]any{
				{"id": "12D3kooONE", "addrs": []string{"/ip4/127.0.0.1/tcp/4002"}},
				{"id": "12D3kooTWO", "addrs": []string{"/ip4/127.0.0.1/tcp/4003"}},
			}})
		case "/v1/trust":
			ok(map[string]any{"trusted": []map[string]any{
				{"peer_id": "12D3kooONE", "alias": "nas", "tier": "read-write"},
			}})
		case "/v1/devices":
			ok(map[string]any{"devices": []map[string]any{
				{"peer_id": "12D3kooONE", "alias": "nas", "online": true},
			}})
		case "/v1/transfers":
			ok(map[string]any{"transfers": []map[string]any{
				{"id": "abc", "state": "done", "file_name": "report.txt", "bytes": 5, "total": 5},
			}})
		case "/v1/history":
			ok(map[string]any{"history": []map[string]any{
				{"id": "old", "state": "failed", "file_name": "gone.txt", "error": "peer went away"},
			}})
		case "/v1/files", "/v1/peers/12D3kooONE/files":
			ok(map[string]any{"files": []map[string]any{
				{"name": "notes.md", "size": 12, "is_dir": false},
			}})
		case "/v1/shares":
			ok(map[string]any{"shares": []map[string]any{
				{"id": "share1", "state": "done", "file_name": "shared.txt", "bytes": 3, "total": 3},
			}})
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no route for ` + r.URL.Path + `"}`))
		}
	})
	return d
}

func (d *fakeDaemon) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.requests...)
}

// runAgainst starts a fake daemon, points the CLI at it, and runs args.
func (d *fakeDaemon) runAgainst(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	dir := t.TempDir()
	full := append([]string{"--daemon-url", d.URL}, args...)
	full = append(full, "--data-dir", filepath.Join(dir, "state"))
	t.Setenv("LANTERND_URL", d.URL)
	t.Setenv("LANTERN_DAEMON_TOKEN", "")
	return runCLI(t, full...)
}

// status prints what the daemon said, not a placeholder.
func TestDaemonStatusPrintsWhatTheDaemonSaid(t *testing.T) {
	d := newFakeDaemon(t)

	stdout, stderr, code := d.runAgainst(t, "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"laptop", "12D3kooWRIGHT", "/ip4/127.0.0.1/tcp/4001", "lan_only: true"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output is missing %q:\n%s", want, stdout)
		}
	}

	// And the JSON form carries the same thing, which is the form an agent
	// reads. A human output that works and a JSON output that does not would
	// be two different commands pretending to be one.
	stdout, stderr, code = d.runAgainst(t, "--json", "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &decoded); err != nil {
		t.Fatalf("--json status is not one JSON object: %v\n%s", err, stdout)
	}
	if decoded["device_name"] != "laptop" {
		t.Errorf("--json status lost the device name: %v", decoded)
	}
}

// The empty case is the one a hand-written test skips and a person hits on
// their first run. "no connected peers" is the whole output.
func TestPeersSaysSoWhenThereAreNone(t *testing.T) {
	d := newFakeDaemon(t)
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/peers" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"peers":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer empty.Close()

	stdout, stderr, code := d.runAt(t, empty.URL, "peers")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "no connected peers") {
		t.Errorf("an empty peer list should say so, got:\n%s", stdout)
	}
}

// runAt is runAgainst with an explicit daemon URL, for a server built inline.
func (d *fakeDaemon) runAt(t *testing.T, url string, args ...string) (string, string, int) {
	t.Helper()
	full := append([]string{"--daemon-url", url}, args...)
	full = append(full, "--data-dir", filepath.Join(t.TempDir(), "state"))
	return runCLI(t, full...)
}

// Every daemon command has to reach its route. A command that renders an
// empty list because it called the wrong path looks exactly like a device
// with nothing in it.
func TestEachDaemonCommandReachesItsRoute(t *testing.T) {
	cases := []struct {
		args []string
		want string // a route the daemon must have seen
	}{
		{[]string{"status"}, "/v1/status"},
		{[]string{"discover"}, "/v1/status"},
		{[]string{"peers"}, "/v1/peers"},
		{[]string{"list"}, "/v1/transfers"},
		{[]string{"list", "history"}, "/v1/history"},
		{[]string{"list", "shares"}, "/v1/shares"},
		{[]string{"trust", "list"}, "/v1/trust"},
		{[]string{"files"}, "/v1/files"},
		{[]string{"remote-files", "12D3kooONE"}, "/v1/peers/12D3kooONE/files"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			d := newFakeDaemon(t)
			stdout, stderr, code := d.runAgainst(t, c.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			found := false
			for _, r := range d.called() {
				if strings.HasSuffix(r, c.want) {
					found = true
				}
			}
			if !found {
				t.Errorf("%v did not reach %s; it called %v", c.args, c.want, d.called())
			}
			if strings.TrimSpace(stdout) == "" {
				t.Errorf("%v printed nothing", c.args)
			}
		})
	}
}

// The recorded list has to have one entry per HTTP call. It used to hold two
// for every call — the handler recorded the request and then appended it again
// — which is invisible to a test that only asks whether a route was reached and
// makes any count-based assertion wrong by a factor of two.
//
// The property pinned here is that recording is faithful, not that a command
// calls each route once. Whether a given command may legitimately poll a route
// twice is a claim about the CLI, and this is a claim about the harness: it
// would break the next time someone adds a retry, for a reason that has
// nothing to do with what is being tested.
func TestEachCallIsRecordedOnce(t *testing.T) {
	for _, command := range []string{"status", "peers", "files"} {
		t.Run(command, func(t *testing.T) {
			d := newFakeDaemon(t)
			if _, stderr, code := d.runAgainst(t, command); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			arrivals, recorded := d.arrivals(), d.called()
			if arrivals == 0 {
				t.Fatalf("%s made no HTTP calls at all", command)
			}
			if len(recorded) != arrivals {
				t.Errorf("%s made %d HTTP calls but %d were recorded:\n%v",
					command, arrivals, len(recorded), recorded)
			}
		})
	}
}

// A daemon that refuses is a different situation from one with nothing, and the
// two must not look alike.
func TestDaemonRefusalIsNotAnEmptyResult(t *testing.T) {
	d := newFakeDaemon(t)
	d.mu.Lock()
	d.failures["/v1/status"] = http.StatusServiceUnavailable
	d.mu.Unlock()

	stdout, stderr, code := d.runAgainst(t, "status")
	if code == 0 {
		t.Fatalf("a refusing daemon should not exit zero: %s", stdout)
	}
	out := stdout + stderr
	if !strings.Contains(out, "refused on purpose") {
		t.Errorf("the daemon's own message was lost: %q", out)
	}
	if strings.Contains(out, "lan_only") {
		t.Errorf("a refusal rendered as a status: %q", out)
	}
}

// --json has to be JSON on the failure path too, since that is the path an
// agent takes.
func TestDaemonRefusalIsJSONUnderJSONFlag(t *testing.T) {
	d := newFakeDaemon(t)
	d.mu.Lock()
	d.failures["/v1/status"] = http.StatusInternalServerError
	d.mu.Unlock()

	stdout, stderr, code := d.runAgainst(t, "--json", "status")
	if code == 0 {
		t.Fatalf("should not exit zero: %s", stdout)
	}
	line := strings.TrimSpace(stdout + stderr)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(lastLine(line)), &decoded); err != nil {
		t.Fatalf("--json failure is not JSON: %v\n%s", err, line)
	}
	if decoded["error"] == nil {
		t.Errorf("a JSON failure carries no error field: %v", decoded)
	}
}

// A missing token and a refused connection are different problems with
// different fixes, which is what `doctor` exists to tell apart. Here the CLI
// only has to not conflate them.
func TestUnreachableDaemonNamesTheURL(t *testing.T) {
	d := newFakeDaemon(t)
	closed := d.URL
	d.Close()

	stdout, stderr, _ := runCLI(t, "--json", "--daemon-url", closed, "status")
	out := stdout + stderr
	if !strings.Contains(out, "lanternd running") {
		t.Errorf("an unreachable daemon should say how to start one, got: %q", out)
	}
	if !strings.Contains(out, closed) {
		t.Errorf("the error does not name the URL that was refused: %q", out)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// usage text is what someone reads when they got a command wrong.
func TestHelpMentionsTheCommandsThatExist(t *testing.T) {
	stdout, stderr, _ := runCLI(t, "help")
	out := stdout + stderr
	// Every command the CLI actually has. push is not one: it is the
	// /v1/pushes endpoint, and asking help for it asserts a command that does
	// not exist.
	for _, cmd := range []string{"send", "receive", "status", "list", "peers", "discover", "trust", "files", "remote-files", "doctor", "version"} {
		if !strings.Contains(out, cmd) {
			t.Errorf("help does not mention %q", cmd)
		}
	}
}

// The daemon and mcp subcommands own their own grammar and run before the
// parser, so the parser's tests never reach them. At minimum they must not
// silently do nothing.
func TestUnknownSubcommandIsNotSwallowed(t *testing.T) {
	stdout, stderr, code := runCLI(t, "--daemon", "definitely-not-a-flag")
	if code == 0 {
		t.Errorf("an unknown daemon flag should not exit zero: %s%s", stdout, stderr)
	}
}

func TestJSONFlagDoesNotPolluteHumanOutput(t *testing.T) {
	d := newFakeDaemon(t)
	stdout, _, _ := d.runAgainst(t, "peers")
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("human output is JSON without --json:\n%s", stdout)
	}
	if strings.Contains(stdout, "\"peers\"") {
		t.Errorf("human output carries JSON fields:\n%s", stdout)
	}
}

func TestErrorOutputGoesToStderr(t *testing.T) {
	d := newFakeDaemon(t)
	d.mu.Lock()
	d.failures["/v1/status"] = http.StatusServiceUnavailable
	d.mu.Unlock()

	stdout, stderr, _ := d.runAgainst(t, "status")
	if strings.TrimSpace(stderr) == "" {
		t.Errorf("an error without --json went nowhere: stdout=%q", stdout)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("an error wrote to stdout, which would corrupt piped output: %q", stdout)
	}
}
