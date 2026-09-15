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
	"strings"
	"time"
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

// Status mirrors GET /v1/status.
type Status struct {
	PeerID     string   `json:"peer_id"`
	DeviceName string   `json:"device_name"`
	Addrs      []string `json:"addrs"`
	LANOnly    bool     `json:"lan_only"`
}

// PeerInfo mirrors one connected peer.
type PeerInfo struct {
	ID        string   `json:"id"`
	Addrs     []string `json:"addrs"`
	Connected bool     `json:"connected"`
}

// TrustEntry is one paired device.
type TrustEntry struct {
	PeerID  string `json:"peer_id"`
	Alias   string `json:"alias"`
	AddedAt string `json:"added_at"`
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

// New builds a client for base with token.
func New(base, token string) *Client {
	return &Client{
		base:   strings.TrimSuffix(base, "/"),
		token:  token,
		api:    &http.Client{Timeout: 30 * time.Second},
		stream: &http.Client{Timeout: 0},
	}
}

// NewFromEnv builds a client from LANTERND_URL and LANTERN_DAEMON_TOKEN
// (or LANTERND_TOKEN), falling back to DefaultURL.
func NewFromEnv() *Client {
	base := strings.TrimSuffix(firstNonEmpty(os.Getenv("LANTERND_URL"), DefaultURL), "/")
	return New(base, firstNonEmpty(os.Getenv("LANTERN_DAEMON_TOKEN"), os.Getenv("LANTERND_TOKEN")))
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
	if err := json.Unmarshal(body, &m); err == nil && m["error"] != "" {
		return fmt.Errorf("daemon: %s (status %d)", m["error"], resp.StatusCode)
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	_ = path
	return fmt.Errorf("daemon: %s (status %d)", msg, resp.StatusCode)
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

// TrustAdd pairs peerID with an optional alias.
func (c *Client) TrustAdd(peerID, alias string) (TrustEntry, error) {
	var e TrustEntry
	return e, c.roundTrip(http.MethodPost, "/v1/trust", map[string]any{"peer_id": peerID, "alias": alias}, &e, http.StatusCreated)
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
