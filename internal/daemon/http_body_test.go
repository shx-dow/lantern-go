package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every JSON request in this API is a small document of paths, aliases, and
// flags, so an oversized body is a mistake or an attack and must be refused
// rather than buffered into memory in full.
func TestJSONBodiesAreBounded(t *testing.T) {
	h := NewHandler(New(nil), "self", nil, true, 0, t.TempDir())
	mux := http.NewServeMux()
	h.Routes(mux)

	huge := strings.Repeat("A", maxJSONBody+1024)
	for _, tc := range []struct {
		name, path, body string
	}{
		{"shares", "/v1/shares", `{"path":"` + huge + `"}`},
		{"fetches", "/v1/fetches", `{"code":"` + huge + `"}`},
		{"pushes", "/v1/pushes", `{"to":"nas","path":"` + huge + `"}`},
		{"trust", "/v1/trust", `{"peer_id":"` + huge + `","alias":"x"}`},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusCreated || rec.Code == http.StatusOK {
			t.Errorf("%s accepted an oversized body (status %d)", tc.name, rec.Code)
		}
	}
}

// A body inside the limit must still parse, so the bound cannot quietly
// reject legitimate input such as a deep path.
func TestJSONBodyInsideLimitStillParses(t *testing.T) {
	h := NewHandler(New(nil), "self", nil, true, 0, t.TempDir())
	mux := http.NewServeMux()
	h.Routes(mux)

	deep := strings.Repeat("segment/", 400) + "file.txt"
	body := `{"path":"` + deep + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/shares", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "request body too large") {
		t.Fatalf("a 2.8KB path was rejected as too large: %s", rec.Body.String())
	}
	// It should have got past parsing and failed later on, for a real reason.
	if !strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("expected a later error, got: %s", rec.Body.String())
	}
}
