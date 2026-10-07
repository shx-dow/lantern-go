package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/p2p"
	"github.com/shx-dow/lantern-go/internal/storage"
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

	// Trust holds paired devices and their tiers; nil means pairing disabled.
	Trust *TrustStore
	// SharedDirs roots local file discovery (GET /v1/files) and the reads a
	// paired device with read access may perform.
	SharedDirs []string
	// WritableRoots bounds where any paired device may write. It is the
	// operator's hard bound: a per-peer writable root may only narrow it.
	// Empty means no writes are possible regardless of tiers.
	WritableRoots []string
	// WritesEnabled mirrors --allow-writes. It is separate from
	// WritableRoots so a device can be configured with an inbox and still
	// refuse every write until the operator opts in.
	WritesEnabled bool
	// MaxWriteBytes caps one push. Zero means p2p.DefaultMaxWriteBytes.
	MaxWriteBytes int64

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
	if d.ln == nil {
		return nil, fmt.Errorf("node not ready")
	}
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
	if d.ln == nil {
		return nil, fmt.Errorf("node not ready")
	}
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

// RememberPeer notes where a paired device is, so a later restart can reach
// it without waiting for the network to announce it. Only paired devices are
// recorded. A failure to save is not worth interrupting a transfer for, so
// the error is returned for the caller to ignore.
func (d *Daemon) RememberPeer(peerID string) {
	if d.Trust == nil {
		return
	}
	node := d.node()
	if node == nil || node.Host == nil {
		return
	}
	id, err := peer.Decode(peerID)
	if err != nil {
		return
	}
	addrs := node.Host.Peerstore().Addrs(id)
	if len(addrs) == 0 {
		return
	}
	strs := make([]string, 0, len(addrs))
	for _, a := range addrs {
		strs = append(strs, a.String())
	}
	_ = d.Trust.UpdateAddrs(peerID, strs)
}

// PeerAddrs returns the cached addresses recorded for a peer, or nil.
func (d *Daemon) PeerAddrs(peerID string) []string {
	if d.Trust == nil {
		return nil
	}
	for _, e := range d.Trust.List() {
		if e.PeerID == peerID {
			return e.Addrs
		}
	}
	return nil
}

// peerIDOf normalises a device reference to a peer ID string, or returns
// the reference unchanged when it is not one.
func peerIDOf(ref string) string {
	if id, err := peer.Decode(strings.TrimSpace(ref)); err == nil {
		return id.String()
	}
	return ref
}

// node returns the underlying p2p node, or nil before it is set up.
func (d *Daemon) node() *p2p.Node {
	if d == nil || d.ln == nil {
		return nil
	}
	return d.ln.Node()
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
	node := d.node()
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

// PushResult describes a completed push and the digests that prove it.
type PushResult struct {
	// Entry describes the stored file. It is the zero value when Entries is
	// set, because a directory push reports a tree rather than a file.
	Entry       lantern.Entry
	Entries     []lantern.Entry
	Bytes       int64
	SHA256      string
	LocalSHA256 string
}

// ReadFile pulls a byte range from a paired device. It is the read path an
// agent uses to "get me xyz from the laptop" without a share code.
func (d *Daemon) ReadFile(ctx context.Context, ref, path string, offset, length int64) (lantern.ReadResult, error) {
	if d.ln == nil {
		return lantern.ReadResult{}, fmt.Errorf("node not ready")
	}
	res, err := d.ln.ReadRemote(ctx, ref, path, offset, length)
	if err == nil {
		// The read proved the peer was reachable, so whatever addresses it
		// came in on are worth keeping.
		d.RememberPeer(peerIDOf(ref))
	}
	return res, err
}

// ProbeDevice reports whether a paired device is reachable right now, without
// moving bytes. It is the reachability half of GET /v1/devices: `online` is a
// live connection that may be long idle, while a probe dials on demand and so
// answers the question an operator is actually asking.
func (d *Daemon) ProbeDevice(ctx context.Context, ref string) error {
	if d.ln == nil {
		return fmt.Errorf("node not ready")
	}
	return d.ln.ProbePeer(ctx, peerIDOf(ref))
}

// StatFile returns metadata for one path on a paired device.
func (d *Daemon) StatFile(ctx context.Context, ref, path string) (lantern.Entry, error) {
	if d.ln == nil {
		return lantern.Entry{}, fmt.Errorf("node not ready")
	}
	return d.ln.StatRemote(ctx, ref, path)
}

// RequestError marks a problem with the caller's own input — a bad path, a
// directory, an oversize file — as opposed to a failure reaching or being
// refused by the remote. The HTTP layer maps it to 400 rather than 502.
type RequestError struct{ Err error }

func (e *RequestError) Error() string { return e.Err.Error() }
func (e *RequestError) Unwrap() error { return e.Err }

func badRequest(format string, args ...any) error {
	return &RequestError{Err: fmt.Errorf(format, args...)}
}

// PushFile copies a local file or directory onto a paired device. Bytes move
// p2p, never through the daemon's HTTP surface, and the copy is verified
// against the remote's reported digest before the transfer is called a
// success.
//
// The source is validated before any transport work, so a caller error is
// reported as such rather than as a failure to reach the remote.
//
// remotePath defaults to the source file's base name.
func (d *Daemon) PushFile(ctx context.Context, ref, path, remotePath string, overwrite bool) (PushResult, error) {
	if strings.TrimSpace(path) == "" {
		return PushResult{}, badRequest("path must not be empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return PushResult{}, badRequest("read source: %w", err)
	}
	// A file's size is known before anything is read, so an oversize source is
	// refused as the caller's mistake rather than after a transfer is under way.
	// A directory's size is only known once it is archived, so that check
	// belongs with the archiving.
	if !info.IsDir() && info.Size() > p2p.DefaultMaxWriteBytes {
		return PushResult{}, badRequest("file is %d bytes, over the %d byte push limit", info.Size(), p2p.DefaultMaxWriteBytes)
	}
	if d.ln == nil {
		return PushResult{}, fmt.Errorf("node not ready")
	}
	if strings.TrimSpace(remotePath) == "" {
		remotePath = filepath.Base(filepath.Clean(path))
	}
	if info.IsDir() {
		return d.pushDir(ctx, ref, path, remotePath, overwrite)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return PushResult{}, err
	}
	// Digest what we are about to send so a corrupted copy cannot be
	// reported as delivered.
	local := sha256.Sum256(content)

	localDigest := hex.EncodeToString(local[:])
	res, err := d.ln.PushRemote(ctx, ref, remotePath, content, overwrite)
	if err != nil {
		return PushResult{}, err
	}
	if err := verifyRemoteDigest(localDigest, res.SHA256); err != nil {
		return PushResult{}, err
	}
	return PushResult{
		Entry:       res.Entry,
		Bytes:       res.Bytes,
		SHA256:      res.SHA256,
		LocalSHA256: localDigest,
	}, nil
}

// pushDir archives a local directory and sends it as one write, which the
// receiving device verifies and then expands into a directory.
//
// The archive is built in a temporary directory and removed afterwards; nothing
// is left in the source tree, and a failure part-way through leaves the source
// untouched. The digest that is verified is the archive's, which is the thing
// that crossed the wire — the receiver reports the digest of what it received
// and stored, not of the tree it expanded from it.
func (d *Daemon) pushDir(ctx context.Context, ref, path, remotePath string, overwrite bool) (PushResult, error) {
	zipPath, cleanup, err := storage.ZipDirToTemp(path)
	if err != nil {
		return PushResult{}, badRequest("archive directory: %w", err)
	}
	defer cleanup()

	fi, err := os.Stat(zipPath)
	if err != nil {
		return PushResult{}, err
	}
	if fi.Size() > p2p.DefaultMaxWriteBytes {
		return PushResult{}, badRequest("the directory archives to %d bytes, over the %d byte push limit", fi.Size(), p2p.DefaultMaxWriteBytes)
	}
	archive, err := os.ReadFile(zipPath)
	if err != nil {
		return PushResult{}, err
	}
	local := sha256.Sum256(archive)
	localDigest := hex.EncodeToString(local[:])

	res, err := d.ln.PushDirRemote(ctx, ref, remotePath, archive, overwrite)
	if err != nil {
		return PushResult{}, err
	}
	if err := verifyRemoteDigest(localDigest, res.SHA256); err != nil {
		return PushResult{}, err
	}
	return PushResult{
		Entries:     res.Entries,
		Bytes:       res.Bytes,
		SHA256:      res.SHA256,
		LocalSHA256: localDigest,
	}, nil
}

// verifyRemoteDigest compares the digest of what we sent against the digest the
// receiving device says it stored.
//
// It fails closed. The receiver both writes the bytes and computes the digest,
// so it is the party being trusted here: a receiver that reports no digest must
// not pass as "verified", or it could report any content as delivered while the
// push response carries an empty sha256. An absent digest means the copy cannot
// be checked, and that is a failure rather than a pass.
//
// The comparison is exact. It does not fold case or trim whitespace, because a
// digest that differs in any byte is a different digest.
func verifyRemoteDigest(local, remote string) error {
	if strings.TrimSpace(remote) == "" {
		return fmt.Errorf("the receiving device reported no digest, so the copy cannot be verified (sent %s)", local)
	}
	if remote != local {
		return fmt.Errorf("digest mismatch: sent %s, remote reported %s", local, remote)
	}
	return nil
}
