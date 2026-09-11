package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := RequireAuth(ok, "secret")

	unauthorized := []string{"", "Bearer wrong", "Basic secret"}
	for _, header := range unauthorized {
		req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("header %q: got %d, want 401", header, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: got %d, want 200", rec.Code)
	}
}

func TestBearerParsing(t *testing.T) {
	if got := bearer("Bearer abc123"); got != "abc123" {
		t.Fatalf("got %q", got)
	}
	if got := bearer("Basic abc"); got != "" {
		t.Fatalf("non-bearer accepted: %q", got)
	}
}

func TestGenerateTokenEntropy(t *testing.T) {
	a, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 64 || a == b {
		t.Fatalf("weak tokens: %q %q", a, b)
	}
}

func TestLoadOrCreateTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreateToken(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateToken(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("token not stable across loads")
	}
	if got, _ := LoadOrCreateToken(dir, "explicit"); got != "explicit" {
		t.Fatalf("explicit token ignored: %q", got)
	}
}
