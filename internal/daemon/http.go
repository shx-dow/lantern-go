package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Handler exposes the daemon over localhost HTTP for the CLI, GUI, and
// future MCP shim. All responses are JSON; bytes never flow through here.
type Handler struct {
	daemon     *Daemon
	peerID     string
	addrs      []string
	lanOnly    bool
	deviceName string
	defaultTTL time.Duration
	uploadDir  string
}

// NewHandler builds HTTP routes around d. peerID/addrs describe this node
// for GET /v1/status. defaultTTL applies to shares without ttl_seconds.
// uploadDir roots browser uploads (defaults to the OS temp dir).
func NewHandler(d *Daemon, peerID string, addrs []string, lanOnly bool, defaultTTL time.Duration, uploadDir string) *Handler {
	return &Handler{daemon: d, peerID: peerID, addrs: addrs, lanOnly: lanOnly, defaultTTL: defaultTTL, uploadDir: uploadDir}
}

// WithDeviceName sets the human alias reported by GET /v1/status.
func (h *Handler) WithDeviceName(name string) *Handler {
	h.deviceName = name
	return h
}

// Routes registers v1 endpoints on mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/shares", h.postShares)
	mux.HandleFunc("GET /v1/shares", h.getShares)
	mux.HandleFunc("DELETE /v1/shares/{id}", h.deleteTransfer)
	mux.HandleFunc("POST /v1/fetches", h.postFetches)
	mux.HandleFunc("GET /v1/transfers", h.getTransfers)
	mux.HandleFunc("GET /v1/transfers/{id}", h.getTransfer)
	mux.HandleFunc("DELETE /v1/transfers/{id}", h.deleteTransfer)
	mux.HandleFunc("GET /v1/history", h.getHistory)
	mux.HandleFunc("GET /v1/status", h.getStatus)
	mux.HandleFunc("GET /v1/peers", h.getPeers)
	mux.HandleFunc("POST /v1/uploads", h.postUploads)
	mux.HandleFunc("GET /v1/events", h.getEvents)
	mux.HandleFunc("GET /v1/trust", h.getTrust)
	mux.HandleFunc("POST /v1/trust", h.postTrust)
	mux.HandleFunc("DELETE /v1/trust/{id}", h.deleteTrust)
	mux.HandleFunc("GET /v1/files", h.getFiles)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

type shareRequest struct {
	Path       string `json:"path"`
	TTLSeconds int64  `json:"ttl_seconds,omitempty"`
}

func (h *Handler) postShares(w http.ResponseWriter, r *http.Request) {
	var req shareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if req.TTLSeconds < 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("ttl_seconds must not be negative"))
		return
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl == 0 {
		ttl = h.defaultTTL
	}
	rec, err := h.daemon.Share(req.Path, ttl)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	snap := mustGet(h, rec.ID)
	writeJSON(w, http.StatusCreated, snap)
}

func (h *Handler) getShares(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"shares": h.daemon.List(KindShare)})
}

type fetchRequest struct {
	Code   string `json:"code"`
	OutDir string `json:"out_dir"`
}

func (h *Handler) postFetches(w http.ResponseWriter, r *http.Request) {
	var req fetchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	rec, err := h.daemon.Fetch(req.Code, req.OutDir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	snap := mustGet(h, rec.ID)
	writeJSON(w, http.StatusCreated, snap)
}

func (h *Handler) getTransfers(w http.ResponseWriter, r *http.Request) {
	kind := Kind(strings.TrimSpace(r.URL.Query().Get("kind")))
	if kind != "" && kind != KindShare && kind != KindFetch {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid kind %q", kind))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transfers": h.daemon.List(kind)})
}

func (h *Handler) getTransfer(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.daemon.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("transfer not found"))
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (h *Handler) deleteTransfer(w http.ResponseWriter, r *http.Request) {
	if !h.daemon.Cancel(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, fmt.Errorf("transfer not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getHistory(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"history": h.daemon.History()})
}

func (h *Handler) getStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"peer_id":     h.peerID,
		"addrs":       h.addrs,
		"lan_only":    h.lanOnly,
		"device_name": h.deviceName,
	})
}

func (h *Handler) getPeers(w http.ResponseWriter, _ *http.Request) {
	peers := h.daemon.Peers()
	if peers == nil {
		peers = []PeerInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": peers})
}

// getEvents streams transfer events as SSE. Clients reconnect with
// Last-Event-ID ignored in v1; they reconcile via GET /v1/transfers.
func (h *Handler) getEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	events, unsub := h.daemon.Subscribe(64)
	defer unsub()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			data, _ := json.Marshal(e)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data)
			flusher.Flush()
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func mustGet(h *Handler, id string) Record {
	rec, _ := h.daemon.Get(id)
	return rec
}

type trustRequest struct {
	PeerID string `json:"peer_id"`
	Alias  string `json:"alias,omitempty"`
}

func (h *Handler) trustStore() (*TrustStore, error) {
	if h.daemon.Trust == nil {
		return nil, fmt.Errorf("pairing is not configured on this daemon")
	}
	return h.daemon.Trust, nil
}

func (h *Handler) getTrust(w http.ResponseWriter, _ *http.Request) {
	store, err := h.trustStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trusted": store.List()})
}

func (h *Handler) postTrust(w http.ResponseWriter, r *http.Request) {
	store, err := h.trustStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	var req trustRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	entry, err := store.Add(strings.TrimSpace(req.PeerID), strings.TrimSpace(req.Alias))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (h *Handler) deleteTrust(w http.ResponseWriter, r *http.Request) {
	store, err := h.trustStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	if !store.Remove(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, fmt.Errorf("peer not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getFiles(w http.ResponseWriter, r *http.Request) {
	entries, err := ListSharedFiles(h.daemon.SharedDirs, r.URL.Query().Get("dir"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if entries == nil {
		entries = []FileEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": entries})
}
