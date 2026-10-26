package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const refTestPeerID = "12D3KooWTestPeerID00000000000000000000000000000000"

// TestUnknownDeviceIsNotFoundAndNamed is the negative control for device
// reference resolution. An unmatched ref used to be passed through to the p2p
// layer, which answered 502 "failed to parse peer ID" or "node not ready":
// neither names the device the caller typed, and neither says the device is
// simply not paired.
func TestUnknownDeviceIsNotFoundAndNamed(t *testing.T) {
	h := newTrustHandler(t, TrustEntry{PeerID: refTestPeerID, Alias: "laptop"})

	req := httptest.NewRequest(http.MethodGet, "/v1/peers/desktop/files", nil)
	req.SetPathValue("id", "desktop")
	rec := httptest.NewRecorder()
	h.getRemoteFiles(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Error, `no paired device named "desktop"`) {
		t.Errorf("error does not name the device: %q", body.Error)
	}
	if !strings.Contains(body.Error, "lantern trust list") {
		t.Errorf("error does not say how to see paired devices: %q", body.Error)
	}
}

// Every path that takes a device reference must answer the same way: a 404
// that names the ref, not a downstream parse or connection error. This is
// coverage for the ordering fix, not a pre-fix control.
func TestEveryDeviceRefPathNamesUnknownDevice(t *testing.T) {
	h := newTrustHandler(t, TrustEntry{PeerID: refTestPeerID, Alias: "laptop"})

	withPath := func(method, target string) *http.Request {
		req := httptest.NewRequest(method, target, nil)
		req.SetPathValue("id", "desktop")
		return req
	}
	cases := []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
		req  *http.Request
	}{
		{"remote files", h.getRemoteFiles, withPath(http.MethodGet, "/v1/peers/desktop/files")},
		{"read", h.getRemoteRead, withPath(http.MethodGet, "/v1/peers/desktop/read?path=/x")},
		{"stat", h.getRemoteStat, withPath(http.MethodGet, "/v1/peers/desktop/stat?path=/x")},
		{"trust patch", h.patchTrust, withPath(http.MethodPatch, "/v1/trust/desktop")},
		{"trust delete", h.deleteTrust, withPath(http.MethodDelete, "/v1/trust/desktop")},
		{"push", h.postPush, httptest.NewRequest(http.MethodPost, "/v1/pushes", strings.NewReader(`{"to":"desktop","path":"/x"}`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, tc.req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("got %d, want 404; body: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `no paired device named \"desktop\"`) {
				t.Errorf("error does not name the device: %s", rec.Body.String())
			}
		})
	}
}
