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
	p2pPort    int
}

// NewHandler builds HTTP routes around d. peerID/addrs describe this node
// for GET /v1/status. defaultTTL applies to shares without ttl_seconds.
// uploadDir roots browser uploads (defaults to the OS temp dir).
func NewHandler(d *Daemon, peerID string, addrs []string, lanOnly bool, defaultTTL time.Duration, uploadDir string) *Handler {
	return &Handler{daemon: d, peerID: peerID, addrs: addrs, lanOnly: lanOnly, defaultTTL: defaultTTL, uploadDir: uploadDir}
}

// WithP2PPort sets the libp2p listen port reported by GET /v1/status. Zero
// means the port was left random, which doctor surfaces as a warning because
// it invalidates any --peer address recorded on another device.
func (h *Handler) WithP2PPort(port int) *Handler {
	h.p2pPort = port
	return h
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
	mux.HandleFunc("PATCH /v1/trust/{id}", h.patchTrust)
	mux.HandleFunc("DELETE /v1/trust/{id}", h.deleteTrust)
	mux.HandleFunc("GET /v1/files", h.getFiles)
	mux.HandleFunc("GET /v1/devices", h.getDevices)
	mux.HandleFunc("GET /v1/peers/{id}/files", h.getRemoteFiles)
	mux.HandleFunc("GET /v1/peers/{id}/read", h.getRemoteRead)
	mux.HandleFunc("GET /v1/peers/{id}/stat", h.getRemoteStat)
	mux.HandleFunc("POST /v1/pushes", h.postPush)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// maxJSONBody bounds a JSON request body. Every request in this API is a small
// document of paths, aliases, and flags, so 64 KiB is generous; without a
// bound, a single oversized field would be buffered into memory in full.
const maxJSONBody = 64 * 1024

// readJSONBody reads one bounded JSON request body into dst.
func readJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	return json.NewDecoder(r.Body).Decode(dst)
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
	if err := readJSONBody(w, r, &req); err != nil {
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
	if err := readJSONBody(w, r, &req); err != nil {
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
		// p2p_port is 0 when the daemon was started without a fixed
		// --p2p-port, which means the listen port changes on every restart
		// and any --peer address recorded elsewhere goes stale. Reporting it
		// is what lets `lantern doctor` say so instead of guessing.
		"p2p_port": h.p2pPort,
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
	// Tier is the standing capability the pairing confers. Absent means
	// DefaultTier, which is read-only: pairing alone never grants a write.
	Tier AccessTier `json:"tier,omitempty"`
	// WritableRoots confines this device to a subset of this daemon's
	// writable dirs. It can only narrow them, never widen them.
	WritableRoots []string `json:"writable_roots,omitempty"`
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

// PATCH /v1/trust/{id} changes an existing pairing without re-adding it, so
// granting or revoking a tier is one call and cannot clobber the alias or the
// cached addresses.
func (h *Handler) patchTrust(w http.ResponseWriter, r *http.Request) {
	store, err := h.trustStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	peerID, err := h.resolveTrustRef(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req trustRequest
	if err := readJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}

	entry, found := store.Get(peerID)
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("peer not found"))
		return
	}
	// Only the fields present in the request change. An absent tier or root
	// list leaves the existing one alone, so a caller cannot revoke a tier by
	// forgetting to resend it.
	if req.Tier != "" {
		entry, err = store.SetTier(peerID, string(req.Tier))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	if req.WritableRoots != nil {
		entry, err = store.SetWritableRoots(peerID, req.WritableRoots, h.daemon.WritableRoots)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	if req.Alias != "" {
		entry.Alias = strings.TrimSpace(req.Alias)
	}
	writeJSON(w, http.StatusOK, entry)
}

func (h *Handler) postTrust(w http.ResponseWriter, r *http.Request) {
	store, err := h.trustStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	var req trustRequest
	if err := readJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	entry, err := store.Add(TrustSpec{
		PeerID:        strings.TrimSpace(req.PeerID),
		Alias:         strings.TrimSpace(req.Alias),
		Tier:          string(req.Tier),
		WritableRoots: req.WritableRoots,
		DeviceRoots:   h.daemon.WritableRoots,
	})
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
	peerID, err := h.resolveTrustRef(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !store.Remove(peerID) {
		writeError(w, http.StatusNotFound, fmt.Errorf("peer not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveTrustRef maps an alias or a peer ID to the stored peer ID. A ref that
// matches neither is returned unchanged so the store reports "not found" rather
// than this helper inventing an error about the wrong thing.
func (h *Handler) resolveTrustRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("device must not be empty")
	}
	if h.daemon.Trust == nil {
		return ref, nil
	}
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

func (h *Handler) getRemoteFiles(w http.ResponseWriter, r *http.Request) {
	ref, err := h.resolveRef(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	entries, err := h.daemon.RemoteFiles(r.Context(), ref, r.URL.Query().Get("dir"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if entries == nil {
		writeJSON(w, http.StatusOK, map[string]any{"files": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": entries})
}
