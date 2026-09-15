package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shx-dow/lantern-go/pkg/lantern"
)

// Kind distinguishes share and fetch transfers.
type Kind string

const (
	KindShare Kind = "share"
	KindFetch Kind = "fetch"
)

// State is the daemon-level lifecycle of a transfer.
type State string

const (
	StateRunning  State = "running"
	StateDone     State = "done"
	StateFailed   State = "failed"
	StateCanceled State = "canceled"
)

// Record is the daemon's view of one transfer. ID is the share code,
// matching Session.ID().
type Record struct {
	ID        string     `json:"id"`
	Kind      Kind       `json:"kind"`
	Code      string     `json:"code"`
	FileName  string     `json:"file_name"`
	FileSize  int64      `json:"file_size"`
	Bytes     int64      `json:"bytes"`
	Total     int64      `json:"total"`
	State     State      `json:"state"`
	Error     string     `json:"error,omitempty"`
	PeerID    string     `json:"peer_id,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	session  *lantern.Session
	cancel   context.CancelFunc
	ttlTimer *time.Timer
}

// snapshot returns a copy without internal handles.
func (r *Record) snapshot() Record {
	c := *r
	c.session = nil
	c.cancel = nil
	return c
}

// EventDTO is the SSE/JSON form of a transfer event. Bytes are never
// embedded; clients fetch file content out-of-band via receive.
type EventDTO struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	FileName string `json:"file_name,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	Total    int64  `json:"total,omitempty"`
	Error    string `json:"error,omitempty"`
}

const maxHistory = 50

// Daemon owns the Lantern node and all transfer records. It is the single
// source of truth that the CLI, GUI, and future MCP shim share.
type Daemon struct {
	ln *lantern.Lantern

	mu      sync.Mutex
	records map[string]*Record
	history []Record

	// Trust holds paired device peer IDs; nil means pairing disabled.
	Trust *TrustStore
	// SharedDirs roots local file discovery (GET /v1/files).
	SharedDirs []string

	subsMu  sync.Mutex
	subs    map[uint64]chan EventDTO
	nextSub uint64
}

// New wraps ln and starts serving records from it.
func New(ln *lantern.Lantern) *Daemon {
	return &Daemon{
		ln:      ln,
		records: make(map[string]*Record),
		subs:    make(map[uint64]chan EventDTO),
	}
}

// NormalizeCode trims spaces/dashes and lowercases share codes so pasted
// or dictated codes still work.
func NormalizeCode(code string) string {
	code = strings.TrimSpace(code)
	code = strings.ReplaceAll(code, "-", "")
	code = strings.ReplaceAll(code, " ", "")
	return strings.ToLower(code)
}

// Share advertises path and tracks the transfer. The share lives until
// Cancel is called, ttl elapses, or the daemon closes, independent of the
// HTTP request. A ttl of zero means no expiry.
func (d *Daemon) Share(path string, ttl time.Duration) (*Record, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path must not be empty")
	}
	ctx, cancel := context.WithCancel(context.Background())
	session, peer, err := d.ln.ShareSession(ctx, path)
	if err != nil {
		cancel()
		return nil, err
	}
	now := time.Now()
	rec := &Record{
		ID:        session.ID(),
		Kind:      KindShare,
		Code:      peer.Code,
		FileName:  peer.FileName,
		FileSize:  peer.FileSize,
		Total:     peer.FileSize,
		State:     StateRunning,
		PeerID:    peer.ID,
		StartedAt: now,
		UpdatedAt: now,
		session:   session,
		cancel:    cancel,
	}
	d.mu.Lock()
	d.records[rec.ID] = rec
	if ttl > 0 {
		exp := now.Add(ttl)
		rec.ExpiresAt = &exp
		rec.ttlTimer = time.AfterFunc(ttl, func() { d.Cancel(rec.ID) })
	}
	d.mu.Unlock()
	go d.watch(session, rec.ID)
	return rec, nil
}

// Fetch pulls code into outDir (".." defaults to ".") and tracks it.
func (d *Daemon) Fetch(code, outDir string) (*Record, error) {
	code = NormalizeCode(code)
	if code == "" {
		return nil, fmt.Errorf("code must not be empty")
	}
	if strings.TrimSpace(outDir) == "" {
		outDir = "."
	}
	ctx, cancel := context.WithCancel(context.Background())
	session, peer, err := d.ln.ReceiveSession(ctx, code, outDir)
	if err != nil {
		cancel()
		return nil, err
	}
	now := time.Now()
	rec := &Record{
		ID:        session.ID(),
		Kind:      KindFetch,
		Code:      peer.Code,
		PeerID:    peer.ID,
		State:     StateRunning,
		StartedAt: now,
		UpdatedAt: now,
		session:   session,
		cancel:    cancel,
	}
	d.mu.Lock()
	d.records[rec.ID] = rec
	d.mu.Unlock()
	go d.watch(session, rec.ID)
	return rec, nil
}

// Get returns a snapshot of one transfer.
func (d *Daemon) Get(id string) (Record, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rec, ok := d.records[id]
	if !ok {
		return Record{}, false
	}
	return rec.snapshot(), true
}

// List returns snapshots of all live transfers, optionally filtered by kind.
// Empty kind returns everything.
func (d *Daemon) List(kind Kind) []Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Record, 0, len(d.records))
	for _, rec := range d.records {
		if kind != "" && rec.Kind != kind {
			continue
		}
		out = append(out, rec.snapshot())
	}
	return out
}

// History returns the most recent terminal transfers, newest last.
func (d *Daemon) History() []Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Record, len(d.history))
	copy(out, d.history)
	return out
}

// Cancel stops a transfer and marks it canceled unless already terminal.
// Revoking a share also clears its local advertisement via the session.
func (d *Daemon) Cancel(id string) bool {
	d.mu.Lock()
	rec, ok := d.records[id]
	if !ok {
		d.mu.Unlock()
		return false
	}
	terminal := rec.State != StateRunning
	if rec.ttlTimer != nil {
		rec.ttlTimer.Stop()
	}
	d.mu.Unlock()
	if terminal {
		return true
	}
	rec.session.Close()
	rec.cancel()
	d.mu.Lock()
	if rec.State == StateRunning {
		rec.State = StateCanceled
		rec.UpdatedAt = time.Now()
	}
	d.mu.Unlock()
	d.broadcast(EventDTO{Type: "cancelled", ID: id})
	return true
}

// Subscribe returns a buffered event channel; call the returned func to
// unsubscribe. Slow consumers drop progress but always see terminal events
// for transfers they watch via Get/polling.
func (d *Daemon) Subscribe(buffer int) (<-chan EventDTO, func()) {
	if buffer < 1 {
		buffer = 1
	}
	d.subsMu.Lock()
	defer d.subsMu.Unlock()
	d.nextSub++
	id := d.nextSub
	ch := make(chan EventDTO, buffer)
	d.subs[id] = ch
	return ch, func() {
		d.subsMu.Lock()
		defer d.subsMu.Unlock()
		if c, ok := d.subs[id]; ok {
			delete(d.subs, id)
			close(c)
		}
	}
}

func (d *Daemon) broadcast(e EventDTO) {
	d.subsMu.Lock()
	subs := make([]chan EventDTO, 0, len(d.subs))
	for _, ch := range d.subs {
		subs = append(subs, ch)
	}
	d.subsMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (d *Daemon) watch(session *lantern.Session, id string) {
	recorded := false
	for e := range session.Events() {
		d.mu.Lock()
		rec, ok := d.records[id]
		if !ok {
			d.mu.Unlock()
			continue
		}
		switch e.Type {
		case lantern.EventTransferProgress:
			rec.Bytes = e.Bytes
			rec.Total = e.Total
			if e.FileName != "" {
				rec.FileName = e.FileName
			}
			rec.UpdatedAt = time.Now()
			d.mu.Unlock()
			d.broadcast(EventDTO{Type: "progress", ID: id, FileName: rec.FileName, Bytes: e.Bytes, Total: e.Total})
		case lantern.EventTransferDone:
			if e.FileName != "" {
				rec.FileName = e.FileName
			}
			rec.Bytes = e.Bytes
			if e.Total > 0 {
				rec.Total = e.Total
			}
			rec.State = StateDone
			rec.UpdatedAt = time.Now()
			if rec.ttlTimer != nil {
				rec.ttlTimer.Stop()
			}
			done := rec.snapshot()
			d.pushHistoryLocked(done)
			recorded = true
			d.mu.Unlock()
			d.broadcast(EventDTO{Type: "done", ID: id, FileName: done.FileName, Bytes: done.Bytes, Total: done.Total})
			return
		case lantern.EventError:
			rec.State = StateFailed
			if e.Err != nil {
				rec.Error = e.Err.Error()
			} else {
				rec.Error = "transfer failed"
			}
			rec.UpdatedAt = time.Now()
			if rec.ttlTimer != nil {
				rec.ttlTimer.Stop()
			}
			failed := rec.snapshot()
			d.pushHistoryLocked(failed)
			recorded = true
			d.mu.Unlock()
			d.broadcast(EventDTO{Type: "error", ID: id, Error: failed.Error})
			return
		default:
			d.mu.Unlock()
		}
	}
	// Channel closed without a terminal event (e.g. Cancel, which marks
	// the record before the channel drains): record history unless a
	// terminal branch above already did.
	d.mu.Lock()
	rec, ok := d.records[id]
	if ok && !recorded {
		if rec.State == StateRunning {
			rec.State = StateCanceled
			rec.UpdatedAt = time.Now()
		}
		d.pushHistoryLocked(rec.snapshot())
	}
	d.mu.Unlock()
}

func (d *Daemon) pushHistoryLocked(rec Record) {
	d.history = append(d.history, rec)
	if len(d.history) > maxHistory {
		d.history = d.history[len(d.history)-maxHistory:]
	}
}

// PeerInfo describes one currently connected libp2p peer.
type PeerInfo struct {
	ID        string   `json:"id"`
	Addrs     []string `json:"addrs"`
	Connected bool     `json:"connected"`
}

// Peers lists currently connected peers (excluding self) with their known
// addresses. It reflects live connections, not DHT history.
func (d *Daemon) Peers() []PeerInfo {
	node := d.ln.Node()
	if node == nil || node.Host == nil {
		return nil
	}
	host := node.Host
	var out []PeerInfo
	for _, id := range host.Network().Peers() {
		addrs := make([]string, 0)
		for _, a := range host.Peerstore().Addrs(id) {
			addrs = append(addrs, a.String())
		}
		out = append(out, PeerInfo{ID: id.String(), Addrs: addrs, Connected: true})
	}
	return out
}

// RemoteFiles lists one level of dir on a connected peer. It delegates to
// the session layer; the remote side serves only its shared dirs and only
// to paired devices.
func (d *Daemon) RemoteFiles(ctx context.Context, peerID, dir string) ([]lantern.ListEntry, error) {
	if d.ln == nil {
		return nil, fmt.Errorf("node not ready")
	}
	return d.ln.RemoteFiles(ctx, peerID, dir)
}
