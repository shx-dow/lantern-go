package daemon

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// tokenFileName is the owner-only file holding the daemon bearer token.
const tokenFileName = ".lanternd-token"

// GenerateToken mints a 256-bit hex bearer token.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// LoadOrCreateToken returns the token in dir, creating and persisting one
// with owner-only permissions when absent. An explicit token (flag/env)
// takes precedence and is never written to disk by us.
func LoadOrCreateToken(dir, explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	if dir == "" {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, tokenFileName)
	if data, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok, nil
		}
	}
	tok, err := GenerateToken()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create token dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0600); err != nil {
		return "", fmt.Errorf("persist token: %w", err)
	}
	return tok, nil
}

// RequireAuth wraps next, rejecting requests without the bearer token.
// Comparison is constant-time. Empty token disables auth (tests only);
// production daemons always set one.
func RequireAuth(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(bearer(r.Header.Get("Authorization")))
		if len(got) == 0 || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="lanternd"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "missing or invalid daemon token (use --daemon-token or $LANTERN_DAEMON_TOKEN)",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearer(header string) string {
	if rest, ok := strings.CutPrefix(header, "Bearer "); ok {
		return strings.TrimSpace(rest)
	}
	return ""
}
