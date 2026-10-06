// Package lanternclient is the single Go client for lanternd's localhost
// v1 API (api/openapi.yaml). The CLI daemon path and the MCP shim both sit
// on it; no transfer logic lives here, so it cannot drift from the daemon.
package lanternclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shx-dow/lantern-go/internal/paths"
)

// DefaultURL is the default lanternd listen address.
const DefaultURL = "http://127.0.0.1:43782"

// Record mirrors the daemon transfer record. ID always equals the share code.
type Record struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Code     string `json:"code"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	Bytes    int64  `json:"bytes"`
	Total    int64  `json:"total"`
	State    string `json:"state"`
	Error    string `json:"error"`
	PeerID   string `json:"peer_id"`
}

// Status mirrors GET /v1/status. P2PPort is 0 when the daemon was started
// without a fixed --p2p-port, meaning its listen port changes every restart.
type Status struct {
	PeerID     string   `json:"peer_id"`
	DeviceName string   `json:"device_name"`
	Addrs      []string `json:"addrs"`
	LANOnly    bool     `json:"lan_only"`
	P2PPort    int      `json:"p2p_port"`
}

// PeerInfo mirrors one connected peer.
type PeerInfo struct {
	ID        string   `json:"id"`
	Addrs     []string `json:"addrs"`
	Connected bool     `json:"connected"`
}

// TrustEntry is one paired device and what it is allowed to do. Tier is the
// standing capability the pairing confers; WritableRoots, when set, confines
// this device to a subset of the daemon's writable dirs.
type TrustEntry struct {
	PeerID        string   `json:"peer_id"`
	Alias         string   `json:"alias"`
	AddedAt       string   `json:"added_at"`
	Tier          string   `json:"tier"`
	WritableRoots []string `json:"writable_roots"`
}

// Device is one paired device with the details an agent needs to decide
// whether to reach for it. Online is point-in-time: a live connection,
// not a promise that the next dial succeeds.
//
// Reachable is only present when the listing was probed (?probe=1). Probing
// dials each peer, which is deliberately opt-in: Lantern does not connect to
// devices just to announce itself.
type Device struct {
	PeerID         string   `json:"peer_id"`
	Alias          string   `json:"alias"`
	Tier           string   `json:"tier"`
	WritableRoots  []string `json:"writable_roots"`
	AddedAt        string   `json:"added_at"`
	Online         bool     `json:"online"`
	KnownAddresses int      `json:"known_addresses"`
	Reachable      *bool    `json:"reachable,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// APIError is a non-2xx response from the daemon. It carries the status code
// so callers can tell "wrong token" (401) from "refused or unreachable"
// (502), which are different problems with different fixes.
type APIError struct {
	Code    int
	Path    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("daemon: %s (status %d)", e.Message, e.Code)
}

// FileEntry is one local or remote file listing entry.
type FileEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
	IsDir   bool   `json:"is_dir"`
}

// Client talks to one lanternd over localhost HTTP.
type Client struct {
	base   string
	token  string
	api    *http.Client
	stream *http.Client
}

// DefaultTimeout bounds a normal API call. A peer probe can outlast it, so
// probes use ProbeTimeout instead.
const (
	DefaultTimeout = 30 * time.Second
	ProbeTimeout   = 12 * time.Second
)

// New builds a client for base with token.
func New(base, token string) *Client {
	return &Client{
		base:   strings.TrimSuffix(base, "/"),
		token:  token,
		api:    &http.Client{Timeout: DefaultTimeout},
		stream: &http.Client{Timeout: 0},
	}
}

// NewFromEnv builds a client from LANTERND_URL and LANTERN_DAEMON_TOKEN
// (or LANTERND_TOKEN), falling back to DefaultURL.
func NewFromEnv() *Client {
	base := strings.TrimSuffix(firstNonEmpty(os.Getenv("LANTERND_URL"), DefaultURL), "/")
	return New(base, firstNonEmpty(os.Getenv("LANTERN_DAEMON_TOKEN"), os.Getenv("LANTERND_TOKEN")))
}

// BaseURL reports the daemon address this client talks to.
func (c *Client) BaseURL() string { return c.base }

// TokenFile is the name the daemon persists its bearer token under.
const TokenFile = ".lanternd-token"

// TokenPath is where the daemon persists its bearer token in the default
// per-user data dir, so a tool can name the exact file an operator should
// read rather than describing it. A daemon started with --data-dir keeps its
// token there instead.
func TokenPath() string {
	return filepath.Join(paths.Data(), TokenFile)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func (c *Client) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func apiError(path string, resp *http.Response) error {
	var m map[string]string
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
	msg := resp.Status
	if err := json.Unmarshal(body, &m); err == nil && m["error"] != "" {
		msg = m["error"]
	} else if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		msg = trimmed
	}
	return &APIError{Code: resp.StatusCode, Path: path, Message: msg}
}

func (c *Client) roundTrip(method, path string, body any, out any, ok ...int) error {
	var rdr io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
		rdr = &buf
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.setAuth(req)
	resp, err := c.api.Do(req)
	if err != nil {
		return fmt.Errorf("daemon %s: %w (is lanternd running at %s?)", path, err, c.base)
	}
	defer resp.Body.Close()
	good := map[int]bool{http.StatusOK: true, http.StatusCreated: true, http.StatusNoContent: true}
	if len(ok) > 0 {
		good = map[int]bool{}
		for _, s := range ok {
			good[s] = true
		}
	}
	if !good[resp.StatusCode] {
		return apiError(path, resp)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Do performs a raw JSON call and returns the decoded payload. It exists
// for pass-through shims (MCP); prefer the typed methods.
func (c *Client) Do(method, path string, body any) (any, error) {
	var v any
	if err := c.roundTrip(method, path, body, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// OpenEventsStream opens GET /v1/events (SSE) for ctx. The caller owns the
// body and must close it.
func (c *Client) OpenEventsStream(ctx context.Context) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	c.setAuth(req)
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, fmt.Errorf("daemon /v1/events: %w (is lanternd running at %s?)", err, c.base)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, apiError("/v1/events", resp)
	}
	return resp, nil
}

// Share advertises path with an optional TTL in seconds (0 = daemon default).
func (c *Client) Share(path string, ttlSeconds int64) (Record, error) {
	var rec Record
	body := map[string]any{"path": path}
	if ttlSeconds != 0 {
		body["ttl_seconds"] = ttlSeconds
	}
	return rec, c.roundTrip(http.MethodPost, "/v1/shares", body, &rec, http.StatusCreated)
}

// Fetch starts pulling code into outDir.
func (c *Client) Fetch(code, outDir string) (Record, error) {
	var rec Record
	return rec, c.roundTrip(http.MethodPost, "/v1/fetches", map[string]any{"code": code, "out_dir": outDir}, &rec, http.StatusCreated)
}

// Get returns one transfer snapshot.
func (c *Client) Get(id string) (Record, error) {
	var rec Record
	return rec, c.roundTrip(http.MethodGet, "/v1/transfers/"+id, nil, &rec)
}

// Cancel revokes a share or cancels a transfer (no-op when terminal).
func (c *Client) Cancel(id string) error {
	return c.roundTrip(http.MethodDelete, "/v1/transfers/"+id, nil, nil, http.StatusNoContent, http.StatusOK)
}

// Transfers lists live transfers, optionally filtered by kind.
func (c *Client) Transfers(kind string) ([]Record, error) {
	path := "/v1/transfers"
	if kind != "" {
		path += "?kind=" + kind
	}
	var out struct {
		Transfers []Record `json:"transfers"`
	}
	return out.Transfers, c.roundTrip(http.MethodGet, path, nil, &out)
}

// Shares lists live shares.
func (c *Client) Shares() ([]Record, error) {
	var out struct {
		Shares []Record `json:"shares"`
	}
	return out.Shares, c.roundTrip(http.MethodGet, "/v1/shares", nil, &out)
}

// History returns recent terminal transfers.
func (c *Client) History() ([]Record, error) {
	var out struct {
		History []Record `json:"history"`
	}
	return out.History, c.roundTrip(http.MethodGet, "/v1/history", nil, &out)
}

// Status returns daemon and node status.
func (c *Client) Status() (Status, error) {
	var st Status
	return st, c.roundTrip(http.MethodGet, "/v1/status", nil, &st)
}

// Peers lists connected peers.
func (c *Client) Peers() ([]PeerInfo, error) {
	var out struct {
		Peers []PeerInfo `json:"peers"`
	}
	if out.Peers == nil {
		out.Peers = []PeerInfo{}
	}
	err := c.roundTrip(http.MethodGet, "/v1/peers", nil, &out)
	if out.Peers == nil {
		out.Peers = []PeerInfo{}
	}
	return out.Peers, err
}

// Discover returns self status plus connected peers.
func (c *Client) Discover() (Status, []PeerInfo, error) {
	st, err := c.Status()
	if err != nil {
		return st, nil, err
	}
	peers, err := c.Peers()
	return st, peers, err
}

// TrustList returns paired devices.
func (c *Client) TrustList() ([]TrustEntry, error) {
	var out struct {
		Trusted []TrustEntry `json:"trusted"`
	}
	return out.Trusted, c.roundTrip(http.MethodGet, "/v1/trust", nil, &out)
}

// TrustAdd pairs peerID with an optional alias, at the default tier.
func (c *Client) TrustAdd(peerID, alias string) (TrustEntry, error) {
	var e TrustEntry
	return e, c.roundTrip(http.MethodPost, "/v1/trust", map[string]any{"peer_id": peerID, "alias": alias}, &e, http.StatusCreated)
}

// TrustAddSpec pairs a device with the full policy: a tier, and optionally the
// subset of the daemon's writable dirs it may use. Empty tier and roots mean
// the daemon's defaults, which are read-only and this device's whole writable
// set respectively.
func (c *Client) TrustAddSpec(body map[string]any) (TrustEntry, error) {
	var e TrustEntry
	return e, c.roundTrip(http.MethodPost, "/v1/trust", body, &e, http.StatusCreated)
}

// TrustSetTier changes one paired device's tier without re-adding it, so the
// alias and cached addresses survive.
func (c *Client) TrustSetTier(peerID, tier string) (TrustEntry, error) {
	var e TrustEntry
	return e, c.roundTrip(http.MethodPatch, "/v1/trust/"+url.PathEscape(peerID),
		map[string]any{"tier": tier}, &e)
}

// TrustSetRoots confines one paired device to a subset of the daemon's
// writable dirs. An empty list clears the restriction.
func (c *Client) TrustSetRoots(peerID string, roots []string) (TrustEntry, error) {
	var e TrustEntry
	if roots == nil {
		roots = []string{}
	}
	return e, c.roundTrip(http.MethodPatch, "/v1/trust/"+url.PathEscape(peerID),
		map[string]any{"writable_roots": roots}, &e)
}

// TrustRemove unpairs peerID.
func (c *Client) TrustRemove(peerID string) error {
	return c.roundTrip(http.MethodDelete, "/v1/trust/"+peerID, nil, nil, http.StatusNoContent, http.StatusOK)
}

// Files lists local shared-dir files.
func (c *Client) Files(dir string) ([]FileEntry, error) {
	path := "/v1/files"
	if dir != "" {
		path += "?dir=" + url.QueryEscape(dir)
	}
	var out struct {
		Files []FileEntry `json:"files"`
	}
	return out.Files, c.roundTrip(http.MethodGet, path, nil, &out)
}

// Devices lists paired devices. probe asks the daemon to dial each one and
// report reachability, which is slower and opens connections that Lantern
// otherwise defers, so it is opt-in.
func (c *Client) Devices(probe bool) ([]Device, error) {
	path := "/v1/devices"
	if probe {
		path += "?probe=1"
	}
	var out struct {
		Devices []Device `json:"devices"`
	}
	err := c.roundTrip(http.MethodGet, path, nil, &out)
	if out.Devices == nil {
		out.Devices = []Device{}
	}
	return out.Devices, err
}

// RemoteFiles lists files on a connected peer.
func (c *Client) RemoteFiles(peerID, dir string) ([]FileEntry, error) {
	path := "/v1/peers/" + peerID + "/files"
	if dir != "" {
		path += "?dir=" + url.QueryEscape(dir)
	}
	var out struct {
		Files []FileEntry `json:"files"`
	}
	return out.Files, c.roundTrip(http.MethodGet, path, nil, &out)
}
