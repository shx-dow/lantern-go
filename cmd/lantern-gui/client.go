// Command lantern-gui is the desktop shell for Lantern.
//
// The GUI is a thin client over lanternd's localhost v1 API
// (api/openapi.yaml): all transfer state lives in the daemon, and every
// method below proxies to it. There are no GUI-only transfer paths.
//
// Default build (pure Go, no desktop window): attaches to a running lanternd
// or starts an embedded daemon, then waits like lanternd console mode:
//
//	go run ./cmd/lantern-gui
//
// Desktop window build (Fyne, needs OS graphics deps, see README.md in
// this directory):
//
//	go run -tags fyne ./cmd/lantern-gui
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultDaemonURL matches lanternd's default listen address.
const defaultDaemonURL = "http://127.0.0.1:43782"

// Transfer mirrors the daemon Record JSON
// (api/openapi.yaml #/components/schemas/Record).
type Transfer struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Code      string     `json:"code"`
	FileName  string     `json:"file_name"`
	FileSize  int64      `json:"file_size"`
	Bytes     int64      `json:"bytes"`
	Total     int64      `json:"total"`
	State     string     `json:"state"`
	Error     string     `json:"error,omitempty"`
	PeerID    string     `json:"peer_id,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Status mirrors #/components/schemas/Status.
type Status struct {
	PeerID  string   `json:"peer_id"`
	Addrs   []string `json:"addrs"`
	LANOnly bool     `json:"lan_only"`
}

// AppInfo is static GUI metadata for the frontend header.
type AppInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Upload mirrors the daemon upload JSON
// (api/openapi.yaml #/components/schemas/Upload).
type Upload struct {
	Path     string `json:"path"`
	FileName string `json:"file_name"`
	Size     int64  `json:"size"`
}

// Peer mirrors #/components/schemas/PeerInfo.
type Peer struct {
	ID        string   `json:"id"`
	Addrs     []string `json:"addrs"`
	Connected bool     `json:"connected"`
}

// Client talks to a running or embedded lanternd over HTTP.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient normalizes baseURL so-flag, env, and test values behave alike.
func NewClient(baseURL, token string) *Client {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = defaultDaemonURL
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	return &Client{
		base:  strings.TrimSuffix(base, "/"),
		token: strings.TrimSpace(token),
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

// APIError surfaces daemon failures with status plus a short excerpt.
type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("daemon %s %s: status %d (%s)", e.Method, e.Path, e.Status, e.Body)
}

func (c *Client) authed(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func (c *Client) doJSON(method, path string, body any, out any, want int) error {
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
	c.authed(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("daemon %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
	if resp.StatusCode != want {
		msg := strings.TrimSpace(string(raw))
		var m map[string]string
		if json.Unmarshal(raw, &m) == nil && m["error"] != "" {
			msg = m["error"]
		}
		if msg == "" {
			msg = resp.Status
		}
		return &APIError{Method: method, Path: path, Status: resp.StatusCode, Body: msg}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

// ShareFile advertises path via POST /v1/shares.
func (c *Client) ShareFile(path string) (Transfer, error) {
	return c.ShareFileWithTTL(path, 0)
}

// ShareFileWithTTL advertises path with an expiry in seconds (0 = daemon default).
func (c *Client) ShareFileWithTTL(path string, ttlSeconds int64) (Transfer, error) {
	var t Transfer
	err := c.doJSON(http.MethodPost, "/v1/shares",
		map[string]any{"path": path, "ttl_seconds": ttlSeconds}, &t, http.StatusCreated)
	return t, err
}

// FetchCode pulls code into outDir via POST /v1/fetches.
func (c *Client) FetchCode(code, outDir string) (Transfer, error) {
	var t Transfer
	err := c.doJSON(http.MethodPost, "/v1/fetches",
		map[string]string{"code": code, "out_dir": outDir}, &t, http.StatusCreated)
	return t, err
}

// ListTransfers returns live transfers, optionally filtered by kind
// ("share", "fetch", or "" for everything).
func (c *Client) ListTransfers(kind string) ([]Transfer, error) {
	path := "/v1/transfers"
	if k := strings.TrimSpace(kind); k != "" {
		path += "?kind=" + url.QueryEscape(k)
	}
	var out struct {
		Transfers []Transfer `json:"transfers"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	if out.Transfers == nil {
		out.Transfers = []Transfer{}
	}
	return out.Transfers, nil
}

// GetTransfer returns one transfer snapshot.
func (c *Client) GetTransfer(id string) (Transfer, error) {
	var t Transfer
	err := c.doJSON(http.MethodGet, "/v1/transfers/"+url.PathEscape(id), nil, &t, http.StatusOK)
	return t, err
}

// CancelTransfer cancels or revokes a transfer via DELETE /v1/transfers/{id}.
func (c *Client) CancelTransfer(id string) error {
	return c.doJSON(http.MethodDelete, "/v1/transfers/"+url.PathEscape(id), nil, nil, http.StatusNoContent)
}

// UploadFile stores data daemon-side via POST /v1/uploads and returns the
// daemon path to pass to ShareFile. It powers the desktop drop zone, which
// holds file bytes but no daemon-local path.
func (c *Client) UploadFile(fileName string, data []byte) (Upload, error) {
	var up Upload
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", fileName)
	if err != nil {
		return up, err
	}
	if _, err := fw.Write(data); err != nil {
		return up, err
	}
	if err := w.Close(); err != nil {
		return up, err
	}
	req, err := http.NewRequest(http.MethodPost, c.base+"/v1/uploads", &buf)
	if err != nil {
		return up, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	c.authed(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return up, fmt.Errorf("daemon POST /v1/uploads: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
	if resp.StatusCode != http.StatusCreated {
		msg := strings.TrimSpace(string(raw))
		var m map[string]string
		if json.Unmarshal(raw, &m) == nil && m["error"] != "" {
			msg = m["error"]
		}
		if msg == "" {
			msg = resp.Status
		}
		return up, &APIError{Method: http.MethodPost, Path: "/v1/uploads", Status: resp.StatusCode, Body: msg}
	}
	if err := json.Unmarshal(raw, &up); err != nil {
		return up, fmt.Errorf("decode POST /v1/uploads: %w", err)
	}
	return up, nil
}

// ListHistory returns recent terminal transfers; never nil.
func (c *Client) ListHistory() ([]Transfer, error) {
	var out struct {
		History []Transfer `json:"history"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/history", nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	if out.History == nil {
		out.History = []Transfer{}
	}
	return out.History, nil
}

// GetStatus returns daemon and node status.
func (c *Client) GetStatus() (Status, error) {
	var st Status
	err := c.doJSON(http.MethodGet, "/v1/status", nil, &st, http.StatusOK)
	return st, err
}

// ListPeers returns currently connected peers; never nil.
func (c *Client) ListPeers() ([]Peer, error) {
	var out struct {
		Peers []Peer `json:"peers"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/peers", nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	if out.Peers == nil {
		out.Peers = []Peer{}
	}
	return out.Peers, nil
}
