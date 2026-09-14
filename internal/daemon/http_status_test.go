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
