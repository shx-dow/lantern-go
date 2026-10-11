package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every entry point must report a missing node rather than dereferencing it.
// A panic here takes the whole daemon down, and the daemon is the only thing
// serving the API, so it is an availability fault and not a cosmetic one.
//
// The nil contexts below are the point of the test, not an oversight: a nil
// context is exactly the kind of zero value a caller can hand in, and these
// entry points have to reject it on the missing node before anything reaches a
// library that would dereference it.
func TestNoNodeIsAnErrorNotAPanic(t *testing.T) {
	d := New(nil)

	if _, err := d.Share("/tmp/x", 0); err == nil {
		t.Error("Share with no node should error")
	}
	if _, err := d.Fetch("code", "/tmp"); err == nil {
		t.Error("Fetch with no node should error")
	}
	//lint:ignore SA1012 A nil context is the input under test.
	if _, err := d.ReadFile(nil, "peer", "/tmp/x", 0, 0); err == nil {
		t.Error("ReadFile with no node should error")
	}
	//lint:ignore SA1012 A nil context is the input under test.
	if _, err := d.StatFile(nil, "peer", "/tmp/x"); err == nil {
		t.Error("StatFile with no node should error")
	}
	//lint:ignore SA1012 A nil context is the input under test.
	if _, err := d.PushFile(nil, "peer", "/etc/hostname", "", false); err == nil {
		t.Error("PushFile with no node should error")
	}
}

// List and Peers are called by the status endpoints, so they must tolerate a
// node that has not been created yet.
func TestReadOnlyAccessorsTolerateNoNode(t *testing.T) {
	d := New(nil)
	if got := d.List(""); len(got) != 0 {
		t.Errorf("List with no node returned %d records, want none", len(got))
	}
	if got := d.History(); len(got) != 0 {
		t.Errorf("History with no node returned %d records, want none", len(got))
	}
	if got := d.Peers(); len(got) != 0 {
		t.Errorf("Peers with no node returned %d peers, want none", len(got))
	}
	if got := d.node(); got != nil {
		t.Errorf("node with no underlying Lantern = %v, want nil", got)
	}
}

// The HTTP surface must answer with an error status rather than crashing the
// process when the node is absent.
func TestHTTPReturnsErrorStatusWithNoNode(t *testing.T) {
	h := NewHandler(New(nil), "self", nil, true, 0, t.TempDir())
	mux := http.NewServeMux()
	h.Routes(mux)

	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"share", "POST", "/v1/shares", `{"path":"/tmp/x"}`},
		{"fetch", "POST", "/v1/fetches", `{"code":"abc"}`},
		{"push", "POST", "/v1/pushes", `{"to":"nas","path":"/tmp/x"}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < 400 {
			t.Errorf("%s returned %d, want an error status", tc.name, rec.Code)
		}
	}
}

func TestPeersEndpointReturnsEmptyArrayWithNoNode(t *testing.T) {
	h := NewHandler(New(nil), "self", nil, true, 0, t.TempDir())
	mux := http.NewServeMux()
	h.Routes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/peers", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/peers returned %d, want %d", rec.Code, http.StatusOK)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"peers":[]}` {
		t.Fatalf("GET /v1/peers body = %s, want {\"peers\":[]}", body)
	}
}
