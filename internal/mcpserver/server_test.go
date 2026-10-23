package mcpserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

func testServer(t *testing.T, dc *daemonClient) (*server, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return &server{dc: dc, out: bufio.NewWriter(&buf)}, &buf
}

func envClient(t *testing.T) *daemonClient {
	t.Helper()
	dc, err := newDaemonClient()
	if err != nil {
		t.Fatal(err)
	}
	return dc
}

func lastResponse(t *testing.T, buf *bytes.Buffer) rpcResponse {
	t.Helper()
	lines := strings.TrimSpace(buf.String())
	if lines == "" {
		t.Fatal("no response written")
	}
	parts := strings.Split(lines, "\n")
	var resp rpcResponse
	if err := json.Unmarshal([]byte(parts[len(parts)-1]), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestInitializeAndToolsList(t *testing.T) {
	s, buf := testServer(t, envClient(t))
	s.handle(rpcRequest{JSONRPC: "2.0", ID: 1, Method: "initialize"})
	resp := lastResponse(t, buf)
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	s.handle(rpcRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list"})
	resp = lastResponse(t, buf)
	m, _ := json.Marshal(resp.Result)
	var got struct {
		Tools []toolDef `json:"tools"`
	}
	if err := json.Unmarshal(m, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) < 7 {
		t.Fatalf("expected >=7 tools, got %d", len(got.Tools))
	}
}

func TestToolsCallStatusProxiesDaemon(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"peer_id": "p1", "device_name": "laptop", "lan_only": true})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dc := lanternclient.New(srv.URL, "")
	s, buf := testServer(t, dc)
	params, _ := json.Marshal(map[string]any{"name": "status", "arguments": map[string]any{}})
	s.handle(rpcRequest{JSONRPC: "2.0", ID: 3, Method: "tools/call", Params: params})
	resp := lastResponse(t, buf)
	if resp.Error != nil {
		t.Fatalf("tools/call error: %+v", resp.Error)
	}
	m, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(m), "laptop") {
		t.Fatalf("expected daemon payload proxied, got %s", m)
	}
}

func TestUnknownMethodErrors(t *testing.T) {
	s, buf := testServer(t, envClient(t))
	s.handle(rpcRequest{JSONRPC: "2.0", ID: 4, Method: "nope/method"})
	resp := lastResponse(t, buf)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("expected -32601, got %+v", resp)
	}
}

// isolateMCPTokenEnv points every platform's per-user data dir at a temp root
// and clears the token sources, so a test controls exactly what the shim can
// find.
func isolateMCPTokenEnv(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("USERPROFILE", filepath.Join(root, "home"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "localappdata"))
	t.Setenv("LANTERN_DAEMON_TOKEN", "")
	t.Setenv("LANTERND_TOKEN", "")
	t.Setenv("LANTERN_DAEMON_TOKEN_FILE", "")
}

// The MCP shim gets its client from NewFromEnvChecked, so it must inherit the
// CLI's token-file fallback. Without it, an agent wired up with just
// LANTERND_URL gets 401s on every tool while the CLI works, which is a
// confusing split.
//
// Control: before the fallback, newDaemonClient sends no bearer token and this
// call fails with the daemon's 401.
func TestMCPClientFindsLoopbackTokenFile(t *testing.T) {
	isolateMCPTokenEnv(t)

	tokenPath := lanternclient.TokenPath()
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("mcp-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mcp-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing or invalid daemon token"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"peer_id": "p1", "device_name": "laptop"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("LANTERND_URL", srv.URL)

	s, buf := testServer(t, envClient(t))
	params, _ := json.Marshal(map[string]any{"name": "status", "arguments": map[string]any{}})
	s.handle(rpcRequest{JSONRPC: "2.0", ID: 5, Method: "tools/call", Params: params})
	resp := lastResponse(t, buf)
	m, _ := json.Marshal(resp.Result)
	if strings.Contains(string(m), "isError") || strings.Contains(string(m), "401") {
		t.Fatalf("status call failed, so the MCP client found no token file: %s", m)
	}
	if !strings.Contains(string(m), "laptop") {
		t.Fatalf("expected the daemon payload, got %s", m)
	}
}

// A token file named through the environment is an instruction, not a hint.
// Refusing to start on an unreadable one names the actual problem; starting
// tokenless turns every later tool call into a misleading 401.
func TestMCPReportsNamedTokenFileError(t *testing.T) {
	isolateMCPTokenEnv(t)
	t.Setenv("LANTERND_URL", "http://127.0.0.1:43782")
	t.Setenv("LANTERN_DAEMON_TOKEN_FILE", filepath.Join(t.TempDir(), "missing-token"))

	if _, err := newDaemonClient(); err == nil {
		t.Fatal("a named token file that cannot be read should be reported at startup")
	}
}
