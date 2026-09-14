// Command lantern-mcp is a thin MCP stdio shim over lanternd's localhost
// v1 API (api/openapi.yaml). No transfer logic lives here: every tool
// proxies to the daemon, so the shim cannot drift from the CLI or SDKs.
//
// Wire it into an MCP client with:
//
//	{"mcpServers": {"lantern": {"command": "lantern-mcp"}}}
//
// Env: LANTERND_URL (default http://127.0.0.1:43782),
// LANTERN_DAEMON_TOKEN (or LANTERND_TOKEN).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultDaemonURL = "http://127.0.0.1:43782"
	mcpVersion       = "2024-11-05"
	serverVersion    = "0.1.0"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string  `json:"jsonrpc"`
	ID      any     `json:"id,omitempty"`
	Result  any     `json:"result,omitempty"`
	Error   *rpcErr `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

func tools() []toolDef {
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	num := func(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
	return []toolDef{
		{"share", "Advertise a local file and return its share code", obj(map[string]any{"path": str("Local file path to share"), "ttl_seconds": num("Auto-cancel after N seconds (0 = daemon default)")}, "path")},
		{"fetch", "Fetch a share code into out_dir", obj(map[string]any{"code": str("Share code"), "out_dir": str("Destination directory (default .)")}, "code")},
		{"status", "Daemon and node status (peer ID, device name, LAN mode)", obj(map[string]any{})},
		{"discover", "Self status plus connected peers", obj(map[string]any{})},
		{"transfers", "List live transfers", obj(map[string]any{"kind": str("Filter: share|fetch (omit for all)")})},
		{"transfer", "Get one transfer snapshot", obj(map[string]any{"id": str("Transfer ID (= share code)")}, "id")},
		{"history", "Recent terminal transfers", obj(map[string]any{})},
		{"cancel", "Cancel/revoke a transfer", obj(map[string]any{"id": str("Transfer ID (= share code)")}, "id")},
		{"trust_list", "List paired devices", obj(map[string]any{})},
		{"trust_add", "Pair a device", obj(map[string]any{"peer_id": str("Peer ID to pair"), "alias": str("Human alias")}, "peer_id")},
		{"trust_remove", "Unpair a device", obj(map[string]any{"peer_id": str("Peer ID to unpair")}, "peer_id")},
		{"files", "List local shared-dir files", obj(map[string]any{"dir": str("Subdirectory (omit for roots)")})},
		{"remote_files", "List files on a connected peer", obj(map[string]any{"peer_id": str("Connected peer ID"), "dir": str("Subdirectory (omit for roots)")}, "peer_id")},
	}
}

type daemonClient struct {
	base  string
	token string
	api   *http.Client
}

func newDaemonClient() *daemonClient {
	base := strings.TrimSuffix(firstNonEmpty(os.Getenv("LANTERND_URL"), defaultDaemonURL), "/")
	return &daemonClient{base: base, token: firstNonEmpty(os.Getenv("LANTERN_DAEMON_TOKEN"), os.Getenv("LANTERND_TOKEN")), api: &http.Client{Timeout: 30 * time.Second}}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func (c *daemonClient) setAuth(r *http.Request) {
	if c.token != "" {
		r.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func (c *daemonClient) doJSON(method, path string, body any) (any, error) {
	var rdr io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
		rdr = &buf
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.setAuth(req)
	resp, err := c.api.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w (is lanternd running at %s?)", method, path, err, c.base)
	}
	defer resp.Body.Close()
	var v any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil && resp.StatusCode < 300 {
		return nil, fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		msg, _ := json.Marshal(v)
		return nil, fmt.Errorf("daemon %s (status %d)", strings.TrimSpace(string(msg)), resp.StatusCode)
	}
	return v, nil
}

func (c *daemonClient) callTool(name string, args map[string]any) (any, error) {
	str := func(k string) string {
		v, _ := args[k].(string)
		return v
	}
	num := func(k string) int64 {
		switch v := args[k].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		case int:
			return int64(v)
		}
		return 0
	}
	switch name {
	case "share":
		if str("path") == "" {
			return nil, fmt.Errorf("path is required")
		}
		body := map[string]any{"path": str("path")}
		if v, ok := args["ttl_seconds"]; ok && v != nil {
			body["ttl_seconds"] = num("ttl_seconds")
		}
		return c.doJSON(http.MethodPost, "/v1/shares", body)
	case "fetch":
		if str("code") == "" {
			return nil, fmt.Errorf("code is required")
		}
		out := str("out_dir")
		if out == "" {
			out = "."
		}
		return c.doJSON(http.MethodPost, "/v1/fetches", map[string]any{"code": str("code"), "out_dir": out})
	case "status":
		return c.doJSON(http.MethodGet, "/v1/status", nil)
	case "discover":
		self, err := c.doJSON(http.MethodGet, "/v1/status", nil)
		if err != nil {
			return nil, err
		}
		peers, err := c.doJSON(http.MethodGet, "/v1/peers", nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"self": self, "peers": (func() any {
			if m, ok := peers.(map[string]any); ok {
				return m["peers"]
			}
			return peers
		})()}, nil
	case "transfers":
		path := "/v1/transfers"
		if k := str("kind"); k != "" {
			path += "?kind=" + k
		}
		return c.doJSON(http.MethodGet, path, nil)
	case "transfer":
		if str("id") == "" {
			return nil, fmt.Errorf("id is required")
		}
		return c.doJSON(http.MethodGet, "/v1/transfers/"+str("id"), nil)
	case "history":
		return c.doJSON(http.MethodGet, "/v1/history", nil)
	case "cancel":
		if str("id") == "" {
			return nil, fmt.Errorf("id is required")
		}
		_, err := c.doJSON(http.MethodDelete, "/v1/transfers/"+str("id"), nil)
		if err != nil {
			// DELETE returns 204 with empty body: treat EOF as success.
			if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "decode") {
				return map[string]any{"cancelled": str("id")}, nil
			}
			return nil, err
		}
		return map[string]any{"cancelled": str("id")}, nil
	case "trust_list":
		return c.doJSON(http.MethodGet, "/v1/trust", nil)
	case "trust_add":
		if str("peer_id") == "" {
			return nil, fmt.Errorf("peer_id is required")
		}
		return c.doJSON(http.MethodPost, "/v1/trust", map[string]any{"peer_id": str("peer_id"), "alias": str("alias")})
	case "trust_remove":
		if str("peer_id") == "" {
			return nil, fmt.Errorf("peer_id is required")
		}
		_, err := c.doJSON(http.MethodDelete, "/v1/trust/"+str("peer_id"), nil)
		if err != nil {
			if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "decode") {
				return map[string]any{"removed": str("peer_id")}, nil
			}
			return nil, err
		}
		return map[string]any{"removed": str("peer_id")}, nil
	case "files":
		path := "/v1/files"
		if d := str("dir"); d != "" {
			path += "?dir=" + d
		}
		return c.doJSON(http.MethodGet, path, nil)
	case "remote_files":
		if str("peer_id") == "" {
			return nil, fmt.Errorf("peer_id is required")
		}
		path := "/v1/peers/" + str("peer_id") + "/files"
		if d := str("dir"); d != "" {
			path += "?dir=" + d
		}
		return c.doJSON(http.MethodGet, path, nil)
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

type server struct {
	dc  *daemonClient
	out *bufio.Writer
}

func (s *server) respond(id any, result any) {
	enc := json.NewEncoder(s.out)
	_ = enc.Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
	_ = s.out.Flush()
}

func (s *server) respondErr(id any, code int, msg string) {
	enc := json.NewEncoder(s.out)
	_ = enc.Encode(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcErr{Code: code, Message: msg}})
	_ = s.out.Flush()
}

func (s *server) handle(req rpcRequest) {
	// Notifications carry no id and get no response.
	isNotification := req.ID == nil
	switch req.Method {
	case "initialize":
		if isNotification {
			return
		}
		s.respond(req.ID, map[string]any{
			"protocolVersion": mcpVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "lantern-mcp", "version": serverVersion},
		})
	case "notifications/initialized", "notifications/cancelled":
		return
	case "ping":
		if !isNotification {
			s.respond(req.ID, map[string]any{})
		}
	case "tools/list":
		if isNotification {
			return
		}
		s.respond(req.ID, map[string]any{"tools": tools()})
	case "tools/call":
		if isNotification {
			return
		}
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.respondErr(req.ID, -32602, "invalid params: "+err.Error())
			return
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		v, err := s.dc.callTool(p.Name, p.Arguments)
		if err != nil {
			text, _ := json.Marshal(map[string]any{"error": err.Error()})
			s.respond(req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}, "isError": true})
			return
		}
		text, _ := json.Marshal(v)
		s.respond(req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}})
	default:
		if !isNotification {
			s.respondErr(req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

func main() {
	s := &server{dc: newDaemonClient(), out: bufio.NewWriter(os.Stdout)}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		s.handle(req)
	}
}
