package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

func TestParseGetTarget(t *testing.T) {
	cases := []struct {
		arg, device, path string
		wantErr           string
	}{
		{arg: "laptop:notes.md", device: "laptop", path: "notes.md"},
		{arg: "nas:/srv/share/report.pdf", device: "nas", path: "/srv/share/report.pdf"},
		// A Windows path keeps its own colon, so the split happens at the first.
		{arg: `laptop:C:\Users\me\notes.md`, device: "laptop", path: `C:\Users\me\notes.md`},
		{arg: "notes.md", wantErr: "no device"},
		{arg: ":notes.md", wantErr: "no device before the colon"},
		{arg: "laptop:", wantErr: "no path after the device name"},
	}
	for _, tc := range cases {
		device, path, err := parseGetTarget(tc.arg)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("parseGetTarget(%q) error = %v, want %q", tc.arg, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseGetTarget(%q): %v", tc.arg, err)
			continue
		}
		if device != tc.device || path != tc.path {
			t.Errorf("parseGetTarget(%q) = %q, %q; want %q, %q", tc.arg, device, path, tc.device, tc.path)
		}
	}
}

// readDaemon serves one file to the CLI. digest overrides the honest digest
// when a test needs the bytes and the claim to disagree.
func readDaemon(t *testing.T, content, digest string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/peers/laptop/read" {
			http.NotFound(w, r)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		body := []byte{}
		eof := false
		if offset == 0 {
			body = []byte(content)
			eof = true
		}
		sum := sha256.Sum256(body)
		want := digest
		if want == "" {
			want = hex.EncodeToString(sum[:])
		}
		_ = json.NewEncoder(w).Encode(lanternclient.ReadChunk{
			Device: "laptop", Path: "notes.md", Offset: int64(offset),
			Length: int64(len(body)), Total: int64(len(content)), EOF: eof,
			Encoding: "utf-8", Content: string(body), SHA256: want,
			Entry: &lanternclient.FileEntry{Name: "notes.md", Size: int64(len(content))},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetWritesTheFile(t *testing.T) {
	srv := readDaemon(t, "hello world", "")
	dest := filepath.Join(t.TempDir(), "copy.md")

	stdout, stderr, code := runCLI(t, "--daemon-url", srv.URL, "--json", "get", "laptop:notes.md", dest)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &decoded); err != nil {
		t.Fatalf("--json get is not one JSON object: %v\n%s", err, stdout)
	}
	if decoded["type"] != "file" || decoded["device"] != "laptop" || decoded["dest"] != dest {
		t.Fatalf("unexpected JSON: %v", decoded)
	}
	if decoded["bytes"] != float64(11) {
		t.Fatalf("bytes = %v", decoded["bytes"])
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Fatalf("file holds %q", got)
	}
}

func TestGetHumanOutputNamesTheFile(t *testing.T) {
	srv := readDaemon(t, "hi", "")
	dest := filepath.Join(t.TempDir(), "copy.md")

	stdout, stderr, code := runCLI(t, "--daemon-url", srv.URL, "get", "laptop:notes.md", dest)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "wrote") || !strings.Contains(stdout, dest) {
		t.Fatalf("output does not say what landed where:\n%s", stdout)
	}
}

// Getting the syntax wrong has to name the shape that works, because the error
// is the only documentation a person reads at that moment.
func TestGetWithoutADeviceShowsTheForm(t *testing.T) {
	_, stderr, code := runCLI(t, "get", "notes.md")
	if code == 0 {
		t.Fatal("expected a usage error")
	}
	if !strings.Contains(stderr, "<device>:<path>") {
		t.Fatalf("error does not show the form:\n%s", stderr)
	}
}

func TestGetRefusesAnOccupiedFile(t *testing.T) {
	srv := readDaemon(t, "new", "")
	dest := filepath.Join(t.TempDir(), "copy.md")
	if err := os.WriteFile(dest, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runCLI(t, "--daemon-url", srv.URL, "get", "laptop:notes.md", dest)
	if code == 0 {
		t.Fatal("expected an occupied destination to be refused")
	}
	if !strings.Contains(stderr, "already exists") {
		t.Fatalf("error does not name the obstacle:\n%s", stderr)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "keep me" {
		t.Fatalf("the existing file changed to %q", got)
	}
}

func TestGetRefusesADigestMismatch(t *testing.T) {
	srv := readDaemon(t, "new", strings.Repeat("0", 64))
	dest := filepath.Join(t.TempDir(), "copy.md")

	_, stderr, code := runCLI(t, "--daemon-url", srv.URL, "get", "laptop:notes.md", dest)
	if code == 0 {
		t.Fatal("expected a mismatched digest to be refused")
	}
	if !strings.Contains(stderr, "digest mismatch") {
		t.Fatalf("error does not say why:\n%s", stderr)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("a refused download left %s behind", dest)
	}
}
