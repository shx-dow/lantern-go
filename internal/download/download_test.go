package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// These tests drive the download through a real client against a daemon that
// answers read requests, because the behaviour that matters lives between two
// components: what the daemon claims, what arrives, and what ends up on disk.

// readServer is a fake lanternd that serves one file in pages. It can lie
// about the digest, omit it, overstate the size, or stop mid-file, which are
// the failures a download has to refuse rather than half-place.
type readServer struct {
	*httptest.Server
	mu       sync.Mutex
	content  []byte
	page     int
	digest   string // "" means the honest digest; "none" means send none
	total    int64  // -1 means len(content)
	stopAt   int    // >= 0: past this offset, return no bytes and no eof
	path     string // the remote path this server serves
	requests []string
}

func newReadServer(t *testing.T, content []byte, page int) *readServer {
	t.Helper()
	s := &readServer{content: content, page: page, total: -1, stopAt: -1, path: "notes.md"}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *readServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.URL.RawQuery)
	content, page, total, stopAt := s.content, s.page, s.total, s.stopAt
	digest, wantPath := s.digest, s.path
	s.mu.Unlock()

	if r.URL.Path != "/v1/peers/laptop/read" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("path") != wantPath {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unexpected path"}`))
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	var body []byte
	eof := false
	switch {
	case stopAt >= 0 && offset >= stopAt:
		// A server that stops answering without ever saying the file ended.
	case offset < len(content):
		end := offset + page
		if end > len(content) {
			end = len(content)
		}
		body = content[offset:end]
		eof = end == len(content)
	}
	if total < 0 {
		total = int64(len(content))
	}
	sum := sha256.Sum256(body)
	realDigest := hex.EncodeToString(sum[:])
	switch digest {
	case "":
		digest = realDigest
	case "none":
		digest = ""
	}

	encoding := "utf-8"
	text := string(body)
	if bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
		encoding = "base64"
		text = base64.StdEncoding.EncodeToString(body)
	}
	_ = json.NewEncoder(w).Encode(lanternclient.ReadChunk{
		Device:   "laptop",
		Path:     wantPath,
		Offset:   int64(offset),
		Length:   int64(len(body)),
		Total:    total,
		EOF:      eof,
		Encoding: encoding,
		Content:  text,
		SHA256:   digest,
		Entry:    &lanternclient.FileEntry{Name: "notes.md", Size: total},
	})
}

func (s *readServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *readServer) client() *lanternclient.Client {
	return lanternclient.New(s.URL, "")
}

func outputDir(t *testing.T) string {
	t.Helper()
	// Not t.TempDir directly: the permission test needs a directory whose mode
	// it sets, and a test that shares its root with other assertions would
	// change what they see.
	dir, err := os.MkdirTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFileCopiesText(t *testing.T) {
	s := newReadServer(t, []byte("hello world"), 64)
	dir := outputDir(t)
	dest := filepath.Join(dir, "copy.md")

	res, err := File(context.Background(), s.client(), "laptop", "notes.md", dest)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != dest || res.Bytes != 11 {
		t.Fatalf("result = %+v", res)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Fatalf("file holds %q", got)
	}
	if len(s.seen()) != 1 {
		t.Fatalf("a short file took %d reads: %v", len(s.seen()), s.seen())
	}
}

// A file larger than one response has to be fetched in order and reassembled;
// a download that only ever reads offset 0 would still pass the single-chunk
// case.
func TestFilePagesThroughTheWholeFile(t *testing.T) {
	s := newReadServer(t, []byte("0123456789"), 3)
	dir := outputDir(t)

	dest := filepath.Join(dir, "copy.md")
	res, err := File(context.Background(), s.client(), "laptop", "notes.md", dest)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "0123456789" {
		t.Fatalf("file holds %q", got)
	}
	if res.Bytes != 10 {
		t.Fatalf("result says %d bytes", res.Bytes)
	}
	seen := s.seen()
	if len(seen) != 4 {
		t.Fatalf("expected four reads (0,3,6,9), got %d: %v", len(seen), seen)
	}
	for i, want := range []string{"offset=0", "offset=3", "offset=6", "offset=9"} {
		if !strings.Contains(seen[i], want) {
			t.Errorf("read %d was %q, want %s", i, seen[i], want)
		}
	}
}

// Binary content arrives base64-encoded, and a NUL byte is the point at which
// "text" stops being text. A decode bug here corrupts every image and archive.
func TestFileDecodesBinary(t *testing.T) {
	content := []byte{0x00, 0x01, 0xff, 0xfe, 'a', 'b', 'c', 0x00}
	s := newReadServer(t, content, 64)
	dir := outputDir(t)

	dest := filepath.Join(dir, "copy.bin")
	if _, err := File(context.Background(), s.client(), "laptop", "notes.md", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("file holds %x, want %x", got, content)
	}
}

// A directory destination places the remote file inside it under its own name,
// including a name written by a device with a different separator.
func TestFileLandsInsideADirectoryDestination(t *testing.T) {
	s := newReadServer(t, []byte("x"), 64)
	s.path = `C:\Users\me\notes.md`
	dir := outputDir(t)

	res, err := File(context.Background(), s.client(), "laptop", s.path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != filepath.Join(dir, "notes.md") {
		t.Fatalf("landed at %s", res.Path)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatal(err)
	}
}

// With no destination the file lands in the current directory, which is what
// `lantern get laptop:notes.md` has to mean.
func TestFileDefaultsToTheCurrentDirectory(t *testing.T) {
	s := newReadServer(t, []byte("x"), 64)
	dir := outputDir(t)
	t.Chdir(dir)

	res, err := File(context.Background(), s.client(), "laptop", "notes.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "notes.md" {
		t.Fatalf("landed at %s", res.Path)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.md")); err != nil {
		t.Fatal(err)
	}
}

func TestFileRefusesAnOccupiedDestination(t *testing.T) {
	s := newReadServer(t, []byte("new content"), 64)
	dir := outputDir(t)
	dest := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(dest, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := File(context.Background(), s.client(), "laptop", "notes.md", dest)
	if err == nil {
		t.Fatal("expected an occupied destination to be refused")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error should name the obstacle: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "keep me" {
		t.Fatalf("the existing file changed to %q", got)
	}
	if len(s.seen()) != 0 {
		t.Fatalf("the download fetched %v before refusing", s.seen())
	}
	assertNoStagingFiles(t, dir)
}

func TestFileRefusesMissingDigest(t *testing.T) {
	s := newReadServer(t, []byte("bytes"), 64)
	s.digest = "none"
	dir := outputDir(t)
	dest := filepath.Join(dir, "notes.md")

	_, err := File(context.Background(), s.client(), "laptop", "notes.md", dest)
	if err == nil {
		t.Fatal("expected content with no digest to be refused")
	}
	if !strings.Contains(err.Error(), "cannot be verified") {
		t.Fatalf("error should say why it was refused: %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("a refused download left %s behind", dest)
	}
	assertNoStagingFiles(t, dir)
}

func TestFileRefusesMismatchedDigest(t *testing.T) {
	s := newReadServer(t, []byte("bytes"), 64)
	s.digest = strings.Repeat("0", 64)
	dir := outputDir(t)
	dest := filepath.Join(dir, "notes.md")

	_, err := File(context.Background(), s.client(), "laptop", "notes.md", dest)
	if err == nil {
		t.Fatal("expected a digest mismatch to be refused")
	}
	if !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("error should name the mismatch: %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("a refused download left %s behind", dest)
	}
	assertNoStagingFiles(t, dir)
}

// A server that ends the file early, or that stops answering without saying it
// ended, must not leave a short file in place.
func TestFileRefusesATruncatedFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*readServer)
	}{
		{"eof before the reported size", func(s *readServer) { s.total = 12 }},
		{"no bytes and no eof", func(s *readServer) { s.stopAt = 4 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newReadServer(t, []byte("0123456789"), 4)
			tc.set(s)
			dir := outputDir(t)
			dest := filepath.Join(dir, "notes.md")

			_, err := File(context.Background(), s.client(), "laptop", "notes.md", dest)
			if err == nil {
				t.Fatal("expected a truncated download to be refused")
			}
			if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
				t.Fatalf("a truncated download left %s behind", dest)
			}
			assertNoStagingFiles(t, dir)
		})
	}
}

func TestFileRefusesAMissingDirectory(t *testing.T) {
	s := newReadServer(t, []byte("x"), 64)
	missing := filepath.Join(t.TempDir(), "nope")
	dest := filepath.Join(missing, "notes.md")

	_, err := File(context.Background(), s.client(), "laptop", "notes.md", dest)
	if err == nil {
		t.Fatal("expected a missing destination directory to be refused")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("error should name the missing directory: %v", err)
	}
}

// The file gets the mode the landing directory implies, not a constant.
func TestFileInheritsDestinationPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}
	s := newReadServer(t, []byte("x"), 64)
	dir := outputDir(t)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "notes.md")
	if _, err := File(context.Background(), s.client(), "laptop", "notes.md", dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode %o in a 0700 directory, want 0600", got)
	}
}

// A refused download must not leave staging files, which is what makes a
// retry after a network failure work without cleanup.
func assertNoStagingFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".lantern-get-") {
			t.Errorf("staging file %s was left behind", e.Name())
		}
	}
}

func TestRemoteBase(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"notes.md", "notes.md"},
		{"/home/me/notes.md", "notes.md"},
		{`C:\Users\me\notes.md`, "notes.md"},
		{"/", ""},
		{".", ""},
		{"..", ""},
		{"", ""},
	} {
		if got := remoteBase(tc.in); got != tc.want {
			t.Errorf("remoteBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestInheritedFilePerm(t *testing.T) {
	for _, tc := range []struct {
		dir  os.FileMode
		want os.FileMode
	}{
		{0o755, 0o644},
		{0o700, 0o600},
		{0o750, 0o640},
	} {
		if got := inheritedFilePerm(tc.dir); got != tc.want {
			t.Errorf("inheritedFilePerm(%o) = %o, want %o", tc.dir, got, tc.want)
		}
	}
}

// The daemon's error is passed through: a missing remote file must read as the
// daemon's refusal, not as a local filesystem problem.
func TestFileReportsTheDaemonsRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"remote: no such file"}`))
	}))
	defer srv.Close()

	dir := outputDir(t)
	_, err := File(context.Background(), lanternclient.New(srv.URL, ""), "laptop", "notes.md", filepath.Join(dir, "notes.md"))
	if err == nil {
		t.Fatal("expected the daemon's refusal")
	}
	if !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("error lost the daemon's reason: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "notes.md")); !os.IsNotExist(statErr) {
		t.Fatal("a refused download left a file behind")
	}
}

func TestFileRefusesEmptyRemotePath(t *testing.T) {
	s := newReadServer(t, []byte("x"), 64)
	_, err := File(context.Background(), s.client(), "laptop", "", filepath.Join(outputDir(t), "x"))
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("expected an empty path to be refused, got %v", err)
	}
}
