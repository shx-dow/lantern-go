package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSReflectsOriginAndPassesThrough(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Origin", "https://lantern.localhost")
	rec := httptest.NewRecorder()
	CORS(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected passthrough 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://lantern.localhost" {
		t.Fatalf("bad allow-origin: %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("expected allow-headers to be set")
	}
}

func TestCORSAnswersPreflightWithoutAuth(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusUnauthorized)
	})
	// Wrap like production: CORS outside RequireAuth.
	h := CORS(RequireAuth(next, "secret"))
	req := httptest.NewRequest(http.MethodOptions, "/v1/status", nil)
	req.Header.Set("Origin", "https://lantern.localhost")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called {
		t.Fatal("preflight must not reach the authed handler")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 preflight, got %d", rec.Code)
	}
}

func TestCORSLeavesSameOriginAlone(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/ui", nil)
	rec := httptest.NewRecorder()
	CORS(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected passthrough 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no allow-origin header, got %q", got)
	}
}
