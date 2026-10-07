package daemon

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

// resolveRef maps a device reference to a peer ID. A ref is either a full
// libp2p peer ID or the alias given at pairing time, so an agent can say
// "laptop" instead of pasting 52 characters of base58.
func (h *Handler) resolveRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("device must not be empty")
	}
	if h.daemon.Trust == nil {
		return ref, nil
	}
	// A peer ID contains no spaces and is long; try it verbatim first.
	for _, e := range h.daemon.Trust.List() {
		if e.PeerID == ref {
			return e.PeerID, nil
		}
	}
	for _, e := range h.daemon.Trust.List() {
		if e.Alias != "" && strings.EqualFold(e.Alias, ref) {
			return e.PeerID, nil
		}
	}
	return ref, nil
}

type readResponse struct {
	Device   string         `json:"device"`
	Path     string         `json:"path"`
	Offset   int64          `json:"offset"`
	Length   int64          `json:"length"`
	Total    int64          `json:"total"`
	EOF      bool           `json:"eof"`
	Encoding string         `json:"encoding"`
	Content  string         `json:"content"`
	Entry    *lantern.Entry `json:"entry,omitempty"`
	Warning  string         `json:"warning,omitempty"`
}

// encodeContent picks how file bytes are carried in a read response: text
// stays text so an agent can consume it directly, and anything that is not
// clean UTF-8 becomes base64. NUL bytes mark binary even when technically
// valid UTF-8. Callers get the encoding name alongside the payload so the
// agent never has to guess.
func encodeContent(data []byte) (encoding, content string) {
	if utf8.Valid(data) && !strings.ContainsRune(string(data), 0) {
		return "utf-8", string(data)
	}
	return "base64", base64.StdEncoding.EncodeToString(data)
}

// getRemoteRead pulls a byte range from a paired device. Text comes back
// as text and binary as base64, so an agent never has to guess how to
// interpret a payload or decode it into its context by hand.
func (h *Handler) getRemoteRead(w http.ResponseWriter, r *http.Request) {
	ref, err := h.resolveRef(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("path is required"))
		return
	}
	offset, err := queryInt(r, "offset", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	length, err := queryInt(r, "length", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if offset < 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("offset must not be negative"))
		return
	}

	res, err := h.daemon.ReadFile(r.Context(), ref, path, offset, length)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}

	out := readResponse{
		Device: ref,
		Path:   path,
		Offset: offset,
		Length: int64(len(res.Data)),
		Total:  res.Entry.Size,
		EOF:    res.EOF,
		Entry:  &res.Entry,
	}
	out.Encoding, out.Content = encodeContent(res.Data)
	if !res.EOF && res.Entry.Size > offset+int64(len(res.Data)) {
		out.Warning = fmt.Sprintf("partial read: %d of %d bytes; pass offset=%d to continue", len(res.Data), res.Entry.Size, offset+int64(len(res.Data)))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) getRemoteStat(w http.ResponseWriter, r *http.Request) {
	ref, err := h.resolveRef(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("path is required"))
		return
	}
	entry, err := h.daemon.StatFile(r.Context(), ref, path)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": ref, "entry": entry})
}

type pushRequest struct {
	// To is the destination alias or peer ID.
	To string `json:"to"`
	// Path is the local file to send.
	Path string `json:"path"`
	// RemotePath is the destination on the target device. Defaults to the
	// source base name.
	RemotePath string `json:"remote_path,omitempty"`
	// Overwrite allows replacing an existing file on the target.
	Overwrite bool `json:"overwrite,omitempty"`
}

type pushResponse struct {
	Device      string          `json:"device"`
	RemotePath  string          `json:"remote_path"`
	Bytes       int64           `json:"bytes"`
	SHA256      string          `json:"sha256"`
	LocalSHA256 string          `json:"local_sha256"`
	Entry       *lantern.Entry  `json:"entry,omitempty"`
	Entries     []lantern.Entry `json:"entries,omitempty"`
}

// postPush sends a local file to a paired device. Only paths and metadata
// cross this API; the bytes move p2p, so pushing a large file costs the same
// request as pushing a small one.
func (h *Handler) postPush(w http.ResponseWriter, r *http.Request) {
	var req pushRequest
	if err := readJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("path is required"))
		return
	}
	ref, err := h.resolveRef(req.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	res, err := h.daemon.PushFile(r.Context(), ref, req.Path, req.RemotePath, req.Overwrite)
	if err != nil {
		// A problem with the caller's own input is a 400; anything the
		// remote refused or could not reach is a 502.
		var bad *RequestError
		if errors.As(err, &bad) {
			writeError(w, http.StatusBadRequest, bad)
			return
		}
		writeError(w, http.StatusBadGateway, err)
		return
	}
	remotePath := req.RemotePath
	if remotePath == "" {
		remotePath = filepath.Base(filepath.Clean(req.Path))
	}
	resp := pushResponse{
		Device:      ref,
		RemotePath:  remotePath,
		Bytes:       res.Bytes,
		SHA256:      res.SHA256,
		LocalSHA256: res.LocalSHA256,
		// A directory push reports the tree it created; a file push reports
		// the file it stored. Sending both would mean one of them is always
		// absent, and a caller cannot tell which shape it got.
		Entries: res.Entries,
	}
	if len(res.Entries) == 0 {
		entry := res.Entry
		resp.Entry = &entry
	}
	writeJSON(w, http.StatusOK, resp)
}

func queryInt(r *http.Request, key string, def int64) (int64, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return v, nil
}

// deviceInfo is one paired device with the details an agent needs to
// decide whether to reach for it.
//
// Reachable is only set when the caller asked to probe. `online` is a live
// connection, which says nothing about a peer that is merely idle, so
// ?probe=1 dials each device and reports whether a request would work now.
type deviceInfo struct {
	PeerID string `json:"peer_id"`
	Alias  string `json:"alias,omitempty"`
	// Tier is what this pairing actually confers. An agent needs it to
	// explain a refusal: "not paired" and "paired but read-only" need
	// different answers.
	Tier          string   `json:"tier,omitempty"`
	WritableRoots []string `json:"writable_roots,omitempty"`
	AddedAt       string   `json:"added_at,omitempty"`
	Online        bool     `json:"online"`
	Addresses     int      `json:"known_addresses"`
	Reachable     *bool    `json:"reachable,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// getDevices lists paired devices and whether each is currently connected,
// so an agent can pick a live target instead of guessing.
//
// Probing is opt-in via ?probe=1 because it opens connections: Lantern
// deliberately does not connect to a device merely to learn whether it is
// there. An operator debugging reachability asks for that cost once.
func (h *Handler) getDevices(w http.ResponseWriter, r *http.Request) {
	if h.daemon.Trust == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("pairing is not configured on this daemon"))
		return
	}
	probe := isTruthy(r.URL.Query().Get("probe"))
	entries := h.daemon.Trust.List()

	// Probe concurrently: each dial is bounded, but serialising them would
	// make N unreachable peers take N times the timeout.
	type probeResult struct {
		ok    bool
		error string
	}
	results := make([]probeResult, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(i int, ref string) {
			defer wg.Done()
			err := h.daemon.ProbeDevice(r.Context(), ref)
			ok := err == nil
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			results[i] = probeResult{ok: ok, error: msg}
		}(i, e.PeerID)
	}
	if probe {
		wg.Wait()
	}

	connected := map[string]bool{}
	if node := h.daemon.node(); node != nil && node.Host != nil {
		for _, p := range node.Host.Network().Peers() {
			connected[p.String()] = true
		}
	}
	node := h.daemon.node()
	out := make([]deviceInfo, 0, len(entries))
	for i, e := range entries {
		addrs := 0
		if node != nil && node.Host != nil {
			if id, err := peer.Decode(e.PeerID); err == nil {
				addrs = len(node.Host.Peerstore().Addrs(id))
			}
		}
		info := deviceInfo{
			PeerID:        e.PeerID,
			Alias:         e.Alias,
			Tier:          string(e.Tier),
			WritableRoots: e.WritableRoots,
			AddedAt:       e.AddedAt,
			Online:        connected[e.PeerID],
			Addresses:     addrs,
		}
		if probe {
			ok := results[i].ok
			info.Reachable = &ok
			info.Error = results[i].error
		}
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// isTruthy reads a query flag. Only an explicit affirmative counts, so a
// stray empty value cannot silently switch on an expensive code path.
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}
