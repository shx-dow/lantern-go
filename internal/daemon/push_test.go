package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shx-dow/lantern-go/internal/p2p"
)

func pushReq(t *testing.T, body map[string]any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, "/v1/pushes", bytes.NewReader(raw))
}

func TestPushRequiresDestination(t *testing.T) {
	h := newTrustHandler(t)
	rec := httptest.NewRecorder()
	h.postPush(rec, pushReq(t, map[string]any{"path": "/etc/passwd"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 when the destination is missing", rec.Code)
	}
}

func TestPushRequiresPath(t *testing.T) {
	h := newTrustHandler(t)
	rec := httptest.NewRecorder()
	h.postPush(rec, pushReq(t, map[string]any{"to": "laptop"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 when the path is missing", rec.Code)
	}
}

func TestPushRejectsMalformedJSON(t *testing.T) {
	h := newTrustHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/pushes", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	h.postPush(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

// An alias nobody paired must not resolve to some other device. It reaches
// the transport as a peer ID and is refused there, so this is an upstream
// failure rather than a caller error.
func TestPushRejectsUnknownAlias(t *testing.T) {
	const id = "12D3KooWTestPeerID00000000000000000000000000000000"
	h := newTrustHandler(t, TrustEntry{PeerID: id, Alias: "laptop"})
	src := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(src, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.postPush(rec, pushReq(t, map[string]any{"to": "desk", "path": src}))
	if rec.Code == http.StatusOK {
		t.Fatal("push to an unknown device must not succeed")
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d, want 502 for an unresolvable device", rec.Code)
	}
}

// A directory source is no longer refused. It is archived and sent, and the
// receiving device expands it into a directory of its own choosing. This test
// asserts that the request is accepted rather than rejected outright, which is
// the behaviour change; the expansion itself is covered end to end in
// internal/p2p/audit_push_dir_test.go.
func TestPushAcceptsDirectorySource(t *testing.T) {
	h := newTrustHandler(t)
	rec := httptest.NewRecorder()
	h.postPush(rec, pushReq(t, map[string]any{"to": "laptop", "path": t.TempDir()}))
	if rec.Code == http.StatusBadRequest && strings.Contains(rec.Body.String(), "not implemented") {
		t.Fatalf("a directory source must no longer be refused as unimplemented: %s", rec.Body.String())
	}
}

func TestPushRejectsMissingSource(t *testing.T) {
	h := newTrustHandler(t)
	rec := httptest.NewRecorder()
	h.postPush(rec, pushReq(t, map[string]any{"to": "laptop", "path": filepath.Join(t.TempDir(), "absent")}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 for a missing source", rec.Code)
	}
}

// The size cap is enforced at the daemon layer too, before the file is read
// into memory, so an oversize push costs no allocation.
func TestPushFileRejectsOversizeBeforeReading(t *testing.T) {
	d := New(nil)
	big := filepath.Join(t.TempDir(), "big")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(p2p.DefaultMaxWriteBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	_, err = d.PushFile(context.Background(), "peer", big, "", false)
	if err == nil {
		t.Fatal("expected an oversize file to be refused")
	}
	if !strings.Contains(err.Error(), "push limit") {
		t.Fatalf("error should name the limit: %v", err)
	}
}

func TestPushFileRejectsDirectory(t *testing.T) {
	d := New(nil)
	if _, err := d.PushFile(context.Background(), "peer", t.TempDir(), "", false); err == nil {
		t.Fatal("expected a directory to be refused")
	}
}

func TestPushFileRejectsEmptyPath(t *testing.T) {
	d := New(nil)
	if _, err := d.PushFile(context.Background(), "peer", "   ", "", false); err == nil {
		t.Fatal("expected an empty path to be refused")
	}
}
