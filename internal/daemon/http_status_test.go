package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatusReportsDeviceName(t *testing.T) {
	h := NewHandler(New(nil), "peer-1", []string{"/ip4/127.0.0.1/tcp/1"}, true, 0, "").WithDeviceName("laptop")
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rec := httptest.NewRecorder()
	h.getStatus(rec, req)
	var got map[string]any
	if err := json.NewDecoder(rec.Result().Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["device_name"] != "laptop" || got["peer_id"] != "peer-1" {
		t.Fatalf("bad status: %v", got)
	}
}

// A daemon started on port 0 or with a custom --data-dir cannot be reached or
// inspected from those flags alone. Status carries the resolved listener
// address and the resolved state directory so a client does not have to guess.
// This is a regression net: the fields did not exist before the fix.
func TestStatusReportsResolvedHTTPAddrAndStateDir(t *testing.T) {
	h := NewHandler(New(nil), "peer-1", nil, true, 0, "").
		WithHTTPAddr("127.0.0.1:54321").
		WithStateDir("/tmp/lantern-state")
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rec := httptest.NewRecorder()
	h.getStatus(rec, req)
	var got map[string]any
	if err := json.NewDecoder(rec.Result().Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["http_addr"] != "127.0.0.1:54321" {
		t.Errorf("http_addr = %v, want the resolved listener address", got["http_addr"])
	}
	if got["state_dir"] != "/tmp/lantern-state" {
		t.Errorf("state_dir = %v, want the resolved data directory", got["state_dir"])
	}
}
