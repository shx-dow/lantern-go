package lanternclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func fakeDaemon() *httptest.Server {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /v1/shares", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		write(w, Record{ID: "c1", Kind: "share", Code: "c1", FileName: "a.bin", FileSize: 3, Total: 3, State: "running"})
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		write(w, Status{PeerID: "p1", DeviceName: "laptop", LANOnly: true})
	})
	mux.HandleFunc("GET /v1/peers", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"peers": []PeerInfo{{ID: "p9", Connected: true}}})
	})
	mux.HandleFunc("GET /v1/trust", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"trusted": []TrustEntry{{PeerID: "p9", Alias: "desk"}}})
	})
	mux.HandleFunc("GET /v1/files", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"files": []FileEntry{{Name: "a.txt", Size: 1}}})
	})
	mux.HandleFunc("DELETE /v1/transfers/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		write(w, map[string]string{"error": "transfer not found"})
	})
	mux.HandleFunc("DELETE /v1/transfers/c1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return httptest.NewServer(mux)
}

func TestClientRoundTrip(t *testing.T) {
	srv := fakeDaemon()
	defer srv.Close()
	c := New(srv.URL, "")

	rec, err := c.Share("/tmp/a.bin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != "c1" {
		t.Fatalf("bad record: %+v", rec)
	}
	st, peers, err := c.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if st.DeviceName != "laptop" || len(peers) != 1 || peers[0].ID != "p9" {
		t.Fatalf("bad discover: %+v %+v", st, peers)
	}
	trusted, err := c.TrustList()
	if err != nil || len(trusted) != 1 || trusted[0].Alias != "desk" {
		t.Fatalf("bad trust: %+v %v", trusted, err)
	}
	files, err := c.Files("")
	if err != nil || len(files) != 1 || files[0].Name != "a.txt" {
		t.Fatalf("bad files: %+v %v", files, err)
	}
	if err := c.Cancel("c1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Cancel("missing"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestNewFromEnv(t *testing.T) {
	t.Setenv("LANTERND_URL", "http://example:1")
	c := NewFromEnv()
	if c.base != "http://example:1" {
		t.Fatalf("bad base: %q", c.base)
	}
}

// isolateTokenEnv points every platform's per-user data directory at a temp
// root and clears the token sources, so a token file written by a test is the
// only one a client can find.
func isolateTokenEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("USERPROFILE", filepath.Join(root, "home"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "localappdata"))
	t.Setenv("LANTERN_DAEMON_TOKEN", "")
	t.Setenv("LANTERND_TOKEN", "")
	t.Setenv("LANTERN_DAEMON_TOKEN_FILE", "")
	return TokenPath()
}

func writeToken(t *testing.T, path, token string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The token file is a convenience for the default local setup, not a secret to
// spray at whatever URL happens to be configured. Reading it is gated on a
// loopback URL; an explicit token always wins.
func TestResolveTokenReadsFileOnlyForLoopback(t *testing.T) {
	path := isolateTokenEnv(t)
	writeToken(t, path, "file-token")

	cases := []struct {
		name     string
		base     string
		explicit string
		file     string
		want     string
		wantErr  bool
	}{
		{name: "default file on ipv4 loopback", base: "http://127.0.0.1:43782", want: "file-token"},
		{name: "default file on localhost", base: "http://localhost:43782", want: "file-token"},
		{name: "default file on ipv6 loopback", base: "http://[::1]:43782", want: "file-token"},
		{name: "explicit token beats the file", base: "http://127.0.0.1:43782", explicit: "explicit", want: "explicit"},
		// The control for the leak this gate exists for: a non-loopback URL
		// must not pick up the local token, even though the file exists.
		{name: "remote url ignores default file", base: "http://192.0.2.10:43782", want: ""},
		// Naming a local file for a remote daemon is refused rather than
		// quietly sending that file's contents across the network.
		{name: "remote url refuses named file", base: "http://192.0.2.10:43782", file: path, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveToken(tc.base, tc.explicit, tc.file)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveToken(%q, %q, %q) = %q, want an error", tc.base, tc.explicit, tc.file, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveToken(%q, %q, %q): %v", tc.base, tc.explicit, tc.file, err)
			}
			if got != tc.want {
				t.Errorf("ResolveToken(%q, %q, %q) = %q, want %q", tc.base, tc.explicit, tc.file, got, tc.want)
			}
		})
	}
}

// A missing default file is not an error: the client simply has no token, and
// the daemon's 401 is the place to explain that. A file the caller named is
// different -- silently ignoring it would hide a typo.
func TestResolveTokenMissingFiles(t *testing.T) {
	path := isolateTokenEnv(t)

	got, err := ResolveToken("http://127.0.0.1:43782", "", "")
	if err != nil || got != "" {
		t.Fatalf("missing default file: token = %q, err = %v; want empty and no error", got, err)
	}
	if _, err := ResolveToken("http://127.0.0.1:43782", "", path); err == nil {
		t.Fatal("a named token file that does not exist should be reported")
	}
}

// NewFromEnv is the MCP shim's entry point, so the same precedence and the
// same loopback gate have to apply there.
func TestNewFromEnvReadsLoopbackTokenFile(t *testing.T) {
	path := isolateTokenEnv(t)
	writeToken(t, path, "file-token")

	t.Setenv("LANTERND_URL", "http://127.0.0.1:43782")
	if c := NewFromEnv(); c.token != "file-token" {
		t.Errorf("loopback URL: token = %q, want the token file's value", c.token)
	}

	t.Setenv("LANTERND_URL", "http://192.0.2.10:43782")
	if c := NewFromEnv(); c.token != "" {
		t.Errorf("non-loopback URL: token = %q, want no local token", c.token)
	}

	t.Setenv("LANTERND_URL", "http://127.0.0.1:43782")
	t.Setenv("LANTERN_DAEMON_TOKEN", "env-token")
	if c := NewFromEnv(); c.token != "env-token" {
		t.Errorf("env token must beat the file: token = %q", c.token)
	}
}

func TestNewFromEnvHonorsTokenFileOverride(t *testing.T) {
	isolateTokenEnv(t)
	custom := filepath.Join(t.TempDir(), "other-token")
	writeToken(t, custom, "custom-token")

	t.Setenv("LANTERN_DAEMON_TOKEN_FILE", custom)
	t.Setenv("LANTERND_URL", "http://127.0.0.1:43782")
	if c := NewFromEnv(); c.token != "custom-token" {
		t.Errorf("token = %q, want the file named by LANTERN_DAEMON_TOKEN_FILE", c.token)
	}
}
