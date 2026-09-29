package daemon

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
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
	Device      string         `json:"device"`
	RemotePath  string         `json:"remote_path"`
	Bytes       int64          `json:"bytes"`
	SHA256      string         `json:"sha256"`
	LocalSHA256 string         `json:"local_sha256"`
	Entry       *lantern.Entry `json:"entry,omitempty"`
}

// postPush sends a local file to a paired device. Only paths and metadata
// cross this API; the bytes move p2p, so pushing a large file costs the same
// request as pushing a small one.
func (h *Handler) postPush(w http.ResponseWriter, r *http.Request) {
	var req pushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		remotePath = filepath.Base(req.Path)
	}
	entry := res.Entry
	writeJSON(w, http.StatusOK, pushResponse{
		Device:      ref,
		RemotePath:  remotePath,
		Bytes:       res.Bytes,
		SHA256:      res.SHA256,
		LocalSHA256: res.LocalSHA256,
		Entry:       &entry,
	})
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
type deviceInfo struct {
	PeerID    string `json:"peer_id"`
	Alias     string `json:"alias,omitempty"`
	AddedAt   string `json:"added_at,omitempty"`
	Online    bool   `json:"online"`
	Addresses int    `json:"known_addresses"`
}

// getDevices lists paired devices and whether each is currently connected,
// so an agent can pick a live target instead of guessing.
func (h *Handler) getDevices(w http.ResponseWriter, _ *http.Request) {
	if h.daemon.Trust == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("pairing is not configured on this daemon"))
		return
	}
	connected := map[string]bool{}
	if node := h.daemon.node(); node != nil && node.Host != nil {
		for _, p := range node.Host.Network().Peers() {
			connected[p.String()] = true
		}
	}
	node := h.daemon.node()
	out := make([]deviceInfo, 0)
	for _, e := range h.daemon.Trust.List() {
		addrs := 0
		if node != nil && node.Host != nil {
			if id, err := peer.Decode(e.PeerID); err == nil {
				addrs = len(node.Host.Peerstore().Addrs(id))
			}
		}
		out = append(out, deviceInfo{
			PeerID:    e.PeerID,
			Alias:     e.Alias,
			AddedAt:   e.AddedAt,
			Online:    connected[e.PeerID],
			Addresses: addrs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}
