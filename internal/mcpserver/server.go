// Package mcpserver is a thin MCP stdio shim over lanternd's localhost v1
// API (api/openapi.yaml). No transfer logic lives here: every tool proxies to
// the daemon, so the shim cannot drift from the CLI or SDKs.
//
// It runs as `lantern mcp`. Wire it into an MCP client with:
//
//	{"mcpServers": {"lantern": {"command": "lantern", "args": ["mcp"]}}}
//
// Env: LANTERND_URL (default http://127.0.0.1:43782),
// LANTERN_DAEMON_TOKEN (or LANTERND_TOKEN).
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shx-dow/lantern-go/internal/version"
	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

const (
	// mcpVersion is the Model Context Protocol revision this shim speaks.
	mcpVersion = "2024-11-05"
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
	arr := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	return []toolDef{
		{"share", "Advertise a local file and return its share code", obj(map[string]any{"path": str("Local file path to share"), "ttl_seconds": num("Auto-cancel after N seconds (0 = daemon default)")}, "path")},
		{"fetch", "Fetch a share code into out_dir", obj(map[string]any{"code": str("Share code"), "out_dir": str("Destination directory (default .)")}, "code")},
		{"status", "Daemon and node status (peer ID, device name, LAN mode)", obj(map[string]any{})},
		{"discover", "Self status plus connected peers", obj(map[string]any{})},
		{"transfers", "List live transfers", obj(map[string]any{"kind": str("Filter: share|fetch (omit for all)")})},
		{"transfer", "Get one transfer snapshot", obj(map[string]any{"id": str("Transfer ID (= share code)")}, "id")},
		{"history", "Recent terminal transfers", obj(map[string]any{})},
		{"cancel", "Cancel/revoke a transfer", obj(map[string]any{"id": str("Transfer ID (= share code)")}, "id")},
		{"trust_list", "List paired devices with the tier each one holds. The tier is what the pairing actually allows: none, read, or read-write.", obj(map[string]any{})},
		{"trust_add", "Pair a device. Defaults to tier read, which cannot write; ask for read-write only when the user wants that device to place files here. writable_roots may only narrow this device's writable dirs, never widen them.", obj(map[string]any{
			"peer_id":        str("Peer ID to pair"),
			"alias":          str("Human alias"),
			"tier":           str("none | read | read-write (default read)"),
			"writable_roots": arr("Restrict this device to these dirs (must be inside this device's writable dirs)"),
		}, "peer_id")},
		{"trust_set", "Change what an already-paired device may do, without re-adding it. Fields left unset keep their current value.", obj(map[string]any{
			"device":         str("Device alias or peer ID"),
			"tier":           str("none | read | read-write"),
			"writable_roots": arr("Restrict this device to these dirs; empty clears the restriction"),
		}, "device")},
		{"trust_remove", "Unpair a device", obj(map[string]any{"peer_id": str("Peer ID or alias to unpair")}, "peer_id")},
		{"files", "List local shared-dir files", obj(map[string]any{"dir": str("Subdirectory (omit for roots)")})},
		{"remote_files", "List files on a connected peer", obj(map[string]any{"peer_id": str("Connected peer ID"), "dir": str("Subdirectory (omit for roots)")}, "peer_id")},
		{"devices", "List paired devices with aliases and status. Start here when the user names a device. Probe=true dials each one to test real reachability; by default 'online' only means a connection is open right now, so an idle device looks offline.", obj(map[string]any{"probe": num("Dial each device and report reachability (default false)")})},
		{"read", "Read a file from a paired device. Device may be an alias or a peer ID. Text comes back as text, binary as base64.", obj(map[string]any{
			"device": str("Device alias (e.g. laptop) or peer ID"),
			"path":   str("Absolute path on that device"),
			"offset": num("Byte offset to start from (default 0)"),
			"length": num("Max bytes to return (default 256KB, max 8MB)"),
		}, "device", "path")},
		{"stat", "Get size and modification time for a path on a paired device", obj(map[string]any{
			"device": str("Device alias or peer ID"),
			"path":   str("Absolute path on that device"),
		}, "device", "path")},
		{"push", "Send a local file to a paired device. Fails if the target refuses writes or the file exists (see overwrite).", obj(map[string]any{
			"to":          str("Destination device alias (e.g. nas) or peer ID"),
			"path":        str("Local file to send"),
			"remote_path": str("Destination path on that device (default: the file's base name)"),
			"overwrite":   str("Replace the file if it already exists (default false)"),
		}, "to", "path")},
	}
}

type daemonClient = lanternclient.Client

func newDaemonClient() *daemonClient {
	return lanternclient.NewFromEnv()
}

func callTool(c *daemonClient, name string, args map[string]any) (any, error) {
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
		return c.Do(http.MethodPost, "/v1/shares", body)
	case "fetch":
		if str("code") == "" {
			return nil, fmt.Errorf("code is required")
		}
		out := str("out_dir")
		if out == "" {
			out = "."
		}
		return c.Do(http.MethodPost, "/v1/fetches", map[string]any{"code": str("code"), "out_dir": out})
	case "status":
		return c.Do(http.MethodGet, "/v1/status", nil)
	case "discover":
		self, err := c.Do(http.MethodGet, "/v1/status", nil)
		if err != nil {
			return nil, err
		}
		peers, err := c.Do(http.MethodGet, "/v1/peers", nil)
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
			path += "?kind=" + url.QueryEscape(k)
		}
		return c.Do(http.MethodGet, path, nil)
	case "transfer":
		if str("id") == "" {
			return nil, fmt.Errorf("id is required")
		}
		return c.Do(http.MethodGet, "/v1/transfers/"+str("id"), nil)
	case "history":
		return c.Do(http.MethodGet, "/v1/history", nil)
	case "cancel":
		if str("id") == "" {
			return nil, fmt.Errorf("id is required")
		}
		_, err := c.Do(http.MethodDelete, "/v1/transfers/"+str("id"), nil)
		if err != nil {
			// DELETE returns 204 with empty body: treat EOF as success.
			if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "decode") {
				return map[string]any{"cancelled": str("id")}, nil
			}
			return nil, err
		}
		return map[string]any{"cancelled": str("id")}, nil
	case "trust_list":
		return c.Do(http.MethodGet, "/v1/trust", nil)
	case "trust_add":
		if str("peer_id") == "" {
			return nil, fmt.Errorf("peer_id is required")
		}
		body := map[string]any{"peer_id": str("peer_id"), "alias": str("alias")}
		if t := str("tier"); t != "" {
			body["tier"] = t
		}
		if roots, ok := stringList(args["writable_roots"]); ok {
			body["writable_roots"] = roots
		}
		return c.Do(http.MethodPost, "/v1/trust", body)
	case "trust_set":
		dev := str("device")
		if dev == "" {
			return nil, fmt.Errorf("device is required (use trust_list to see aliases)")
		}
		body := map[string]any{}
		if t := str("tier"); t != "" {
			body["tier"] = t
		}
		if roots, ok := stringList(args["writable_roots"]); ok {
			body["writable_roots"] = roots
		}
		if len(body) == 0 {
			return nil, fmt.Errorf("nothing to change: pass tier and/or writable_roots")
		}
		return c.Do(http.MethodPatch, "/v1/trust/"+url.PathEscape(dev), body)
	case "trust_remove":
		if str("peer_id") == "" {
			return nil, fmt.Errorf("peer_id is required")
		}
		_, err := c.Do(http.MethodDelete, "/v1/trust/"+str("peer_id"), nil)
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
			path += "?dir=" + url.QueryEscape(d)
		}
		return c.Do(http.MethodGet, path, nil)
	case "remote_files":
		if str("peer_id") == "" {
			return nil, fmt.Errorf("peer_id is required")
		}
		path := "/v1/peers/" + url.PathEscape(str("peer_id")) + "/files"
		if d := str("dir"); d != "" {
			path += "?dir=" + url.QueryEscape(d)
		}
		return c.Do(http.MethodGet, path, nil)
	case "devices":
		path := "/v1/devices"
		if args["probe"] != nil && isTruthyArg(args["probe"]) {
			path += "?probe=1"
		}
		return c.Do(http.MethodGet, path, nil)
	case "read":
		dev, path := str("device"), str("path")
		if dev == "" {
			return nil, fmt.Errorf("device is required (use devices to list aliases)")
		}
		if path == "" {
			return nil, fmt.Errorf("path is required")
		}
		q := url.Values{}
		q.Set("path", path)
		if v := num("offset"); v > 0 {
			q.Set("offset", strconv.FormatInt(v, 10))
		}
		if v := num("length"); v > 0 {
			q.Set("length", strconv.FormatInt(v, 10))
		}
		return c.Do(http.MethodGet, "/v1/peers/"+url.PathEscape(dev)+"/read?"+q.Encode(), nil)
	case "stat":
		dev, path := str("device"), str("path")
		if dev == "" || path == "" {
			return nil, fmt.Errorf("device and path are required")
		}
		q := url.Values{}
		q.Set("path", path)
		return c.Do(http.MethodGet, "/v1/peers/"+url.PathEscape(dev)+"/stat?"+q.Encode(), nil)
	case "push":
		to, path := str("to"), str("path")
		if to == "" {
			return nil, fmt.Errorf("to is required (use devices to list aliases)")
		}
		if path == "" {
			return nil, fmt.Errorf("path is required")
		}
		body := map[string]any{"to": to, "path": path}
		if rp := str("remote_path"); rp != "" {
			body["remote_path"] = rp
		}
		overwrite := false
		switch v := args["overwrite"].(type) {
		case bool:
			overwrite = v
		case string:
			overwrite = v == "true" || v == "1" || v == "yes"
		}
		body["overwrite"] = overwrite
		return c.Do(http.MethodPost, "/v1/pushes", body)
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

// stringList reads an optional array-of-strings argument. The second result
// distinguishes "absent" from "present but empty", which matters: clearing a
// restriction sends an empty list, while leaving it alone sends nothing.
func stringList(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out, true
}

// isTruthyArg reads a boolean-ish tool argument. Tools are called by models,
// which send "true" as a string as often as a bool, so both are accepted.
func isTruthyArg(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "yes":
			return true
		}
	case float64:
		return t != 0
	}
	return false
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
			// The version reported to the host must be the same one
			// `lantern version` prints, or an agent and a human disagree
			// about which build they are talking to.
			"serverInfo": map[string]any{"name": "lantern", "version": version.Get().Version},
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
		v, err := callTool(s.dc, p.Name, p.Arguments)
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

// Serve runs the JSON-RPC loop over in and out until the input is exhausted
// or ctx is cancelled. It is exported and parameterised so the shim can be
// driven from a test or from another program rather than only as a process.
func Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s := &server{dc: newDaemonClient(), out: bufio.NewWriter(out)}
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		// A cancelled context means the host went away mid-call; stop reading
		// rather than blocking on a stdin that will never deliver more.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.handle(req)
	}
	return scan.Err()
}

// Usage is the agent shim's help text, also shown by `lantern help mcp`.
const Usage = `lantern mcp

Runs the MCP stdio server, the bridge between an agent and the daemon. It
holds no transfer logic: every tool proxies the daemon's local API, so it
cannot drift from the CLI or the SDKs.

Point it at a running daemon:

  LANTERND_URL           daemon base URL (default http://127.0.0.1:43782)
  LANTERN_DAEMON_TOKEN   daemon bearer token (or LANTERND_TOKEN)
  LANTERND_TOKEN         same

Register it with an MCP client as one command:

  {"mcpServers": {"lantern": {"command": "lantern", "args": ["mcp"]}}}

Tools:
  devices                      paired devices with aliases and status
                               (probe=true dials each for real reachability)
  read                         read a file from a paired device
  stat                         size and modification time for a path
  push                         send a local file to a paired device
  share, fetch, transfers, transfer, history, cancel
  trust_list, trust_add, trust_set, trust_remove
  files, remote_files, status, discover
`
