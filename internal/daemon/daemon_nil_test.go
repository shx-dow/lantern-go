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
func TestNoNodeIsAnErrorNotAPanic(t *testing.T) {
	d := New(nil)

	if _, err := d.Share("/tmp/x", 0); err == nil {
		t.Error("Share with no node should error")
	}
	if _, err := d.Fetch("code", "/tmp"); err == nil {
		t.Error("Fetch with no node should error")
	}
	if _, err := d.ReadFile(nil, "peer", "/tmp/x", 0, 0); err == nil {
		t.Error("ReadFile with no node should error")
	}
	if _, err := d.StatFile(nil, "peer", "/tmp/x"); err == nil {
		t.Error("StatFile with no node should error")
	}
	if _, err := d.PushFile(nil, "peer", "/etc/hostname", "", false); err == nil {
		t.Error("PushFile with no node should error")
	}
}

// List and Peers are called by the status endpoints, so they must tolerate a
// node that has not been created yet.
func TestReadOnlyAccessorsTolerateNoNode(t *testing.T) {
	d := New(nil)
	_ = d.List("")
	_ = d.History()
	_ = d.Peers()
	_ = d.node()
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
