package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T, dc *daemonClient) (*server, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return &server{dc: dc, out: bufio.NewWriter(&buf)}, &buf
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
	s, buf := testServer(t, newDaemonClient())
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
	dc := &daemonClient{base: srv.URL, api: srv.Client()}
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
	s, buf := testServer(t, newDaemonClient())
	s.handle(rpcRequest{JSONRPC: "2.0", ID: 4, Method: "nope/method"})
	resp := lastResponse(t, buf)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("expected -32601, got %+v", resp)
	}
}
