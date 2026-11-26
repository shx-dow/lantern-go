package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pushDaemon answers POST /v1/pushes with body and records the request. A test
// gets back the decoded body it sent, so it can assert what the CLI claimed
// rather than what the daemon happened to echo.
func pushDaemon(t *testing.T, status int, body string) (*httptest.Server, *map[string]any) {
	t.Helper()
	got := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/pushes" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func writeSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The negative control for this command: before it existed, `lantern push`
// was an unknown command and never reached the daemon at all.
func TestPushSendsTheLocalFileAndSaysWhereItLanded(t *testing.T) {
	srv, got := pushDaemon(t, http.StatusOK, `{
		"device":"12D3kooONE","remote_path":"backup/report.txt","bytes":5,
		"sha256":"aa","local_sha256":"aa",
		"entry":{"name":"report.txt","size":5,"is_dir":false}
	}`)
	local := writeSource(t)

	stdout, stderr, code := runCLI(t, "--daemon-url", srv.URL,
		"push", "nas:backup/report.txt", local)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if (*got)["to"] != "nas" {
		t.Errorf("to = %v, want nas", (*got)["to"])
	}
	if (*got)["path"] != local {
		t.Errorf("path = %v, want %q", (*got)["path"], local)
	}
	if (*got)["remote_path"] != "backup/report.txt" {
		t.Errorf("remote_path = %v, want backup/report.txt", (*got)["remote_path"])
	}
	if _, sent := (*got)["overwrite"]; sent {
		t.Errorf("overwrite was sent when it was not asked for: %v", *got)
	}
	for _, want := range []string{"pushed", local, "nas:backup/report.txt"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestPushJSONReportsTheVerifiedDigests(t *testing.T) {
	srv, _ := pushDaemon(t, http.StatusOK, `{
		"device":"12D3kooONE","remote_path":"backup/report.txt","bytes":5,
		"sha256":"aa","local_sha256":"aa",
		"entry":{"name":"report.txt","size":5,"is_dir":false}
	}`)
	local := writeSource(t)

	stdout, stderr, code := runCLI(t, "--daemon-url", srv.URL, "--json",
		"push", "nas:backup/report.txt", local)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &decoded); err != nil {
		t.Fatalf("--json push is not one JSON object: %v\n%s", err, stdout)
	}
	if decoded["type"] != "push" || decoded["device"] != "12D3kooONE" {
		t.Errorf("unexpected JSON: %v", decoded)
	}
	if decoded["sha256"] != "aa" || decoded["local_sha256"] != "aa" {
		t.Errorf("the digests are missing from the JSON: %v", decoded)
	}
	if decoded["bytes"] != float64(5) {
		t.Errorf("bytes = %v, want 5", decoded["bytes"])
	}
}

func TestPushOverwriteReachesTheDaemon(t *testing.T) {
	srv, got := pushDaemon(t, http.StatusOK, `{
		"device":"12D3kooONE","remote_path":"report.txt","bytes":5,
		"sha256":"aa","local_sha256":"aa",
		"entry":{"name":"report.txt","size":5,"is_dir":false}
	}`)
	local := writeSource(t)

	_, stderr, code := runCLI(t, "--daemon-url", srv.URL,
		"push", "nas:report.txt", local, "--overwrite")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if (*got)["overwrite"] != true {
		t.Errorf("overwrite did not reach the daemon: %v", *got)
	}
}

// A receiver that refuses the write explains why. The CLI has to show that
// reason, not a bare status code, or the user cannot tell "not paired for
// writes" from "disk full" from "device offline".
func TestPushShowsTheDaemonsRefusal(t *testing.T) {
	srv, _ := pushDaemon(t, http.StatusBadGateway, `{"error":"device is not paired for writes"}`)
	local := writeSource(t)

	_, stderr, code := runCLI(t, "--daemon-url", srv.URL,
		"push", "nas:report.txt", local)
	if code == 0 {
		t.Fatal("a refused push exited zero")
	}
	if !strings.Contains(stderr, "device is not paired for writes") {
		t.Fatalf("the refusal reason is missing:\n%s", stderr)
	}
}

// Getting the syntax wrong has to name the shape that works: the error is the
// only documentation a person reads at that moment.
func TestPushWithoutADeviceShowsTheForm(t *testing.T) {
	_, stderr, code := runCLI(t, "push", "report.txt")
	if code == 0 {
		t.Fatal("expected a usage error")
	}
	if !strings.Contains(stderr, "<device>:<path>") || !strings.Contains(stderr, "<local>") {
		t.Fatalf("error does not show the form:\n%s", stderr)
	}
}
