package lantern

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/shx-dow/lantern-go/internal/p2p"
)

// Config selects the listen port, the directory for local advertisements,
// and explicit bootstrap peers (defaults to the public libp2p bootstraps).
type Config struct {
	Port      int
	DataDir   string
	Bootstrap []string
}

// EventType classifies a Lantern event; progress events may be dropped
// under backpressure but terminal events (done/error) are always delivered.
type EventType int

const (
	EventTransferProgress EventType = iota
	EventTransferDone
	EventPeerFound
	EventPeerConnected
	EventError
)

// TransferState is the lifecycle of a Session; it moves forward only,
// ending in done, failed, or canceled.
type TransferState int

const (
	TransferPending TransferState = iota
	TransferRunning
	TransferDone
	TransferFailed
	TransferCanceled
)

// Event is a transfer lifecycle notification. TransferID correlates it
// with the Session that produced it; Err is set only on EventError.
type Event struct {
	TransferID string
	Type       EventType
	PeerID     string
	FileName   string
	Bytes      int64
	Total      int64
	Code       string
	Err        error
}

// Peer describes the other side of a transfer: its peer ID, the share
// code, and (for shares) the advertised file.
type Peer struct {
	ID       string
	Code     string
	FileName string
	FileSize int64
}

// Lantern is the high-level transfer API. Use ShareSession/ReceiveSession
// for transfers; the zero value is not usable, construct via New.
type Lantern struct {
	node           *p2p.Node
	events         chan Event
	mu             sync.Mutex
	subscribers    map[uint64]subscription
	nextSubscriber uint64
	closeOnce      sync.Once
	closeErr       error
	closed         bool
}

// Event buffer sizes: broadcasts tolerate slow consumers by dropping
// progress events, while per-session buffers are generous enough that a
// terminal event is never lost behind progress backlog.
const (
	broadcastBufferSize = 64
	sessionBufferSize   = 128
)

type subscription struct {
	channel  chan Event
	terminal func(Event)
}

// Session tracks one transfer: its events, terminal state, and
// cancellation. Close is idempotent; the first terminal event wins.
type Session struct {
	mu          sync.RWMutex
	id          string
	ctx         context.Context
	cancel      context.CancelFunc
	events      <-chan Event
	unsubscribe func()
	done        chan struct{}
	state       TransferState
	finishOnce  sync.Once
}

// ID returns the share code this session tracks. It is fixed at creation.
func (s *Session) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

// Events streams this session's events; the channel closes when the
// session finishes or is closed.
func (s *Session) Events() <-chan Event { return s.events }

// Done closes when the session reaches a terminal state.
func (s *Session) Done() <-chan struct{} { return s.done }

// State reports the session's current lifecycle state.
func (s *Session) State() TransferState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Cancel is shorthand for Close.
func (s *Session) Cancel() { s.Close() }

// Close cancels the transfer and releases the session. It is idempotent.
func (s *Session) Close() {
	s.finish(TransferCanceled)
}

func (s *Session) finish(state TransferState) {
	s.finishOnce.Do(func() {
		s.cancel()
		s.mu.Lock()
		if s.state == TransferPending || s.state == TransferRunning {
			s.state = state
		}
		s.mu.Unlock()
		close(s.done)
		go s.unsubscribe()
	})
}

// New starts the p2p node and returns a Lantern bound to it. An empty
// DataDir falls back to the OS temp dir.
func New(cfg Config) (*Lantern, error) {
	if cfg.DataDir == "" {
		cfg.DataDir = os.TempDir()
	}

	node, err := p2p.NewNode(cfg.Port, cfg.Bootstrap, cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("init p2p: %w", err)
	}

	l := &Lantern{
		node:        node,
		events:      make(chan Event, broadcastBufferSize),
		subscribers: make(map[uint64]subscription),
	}
	return l, nil
}

// Events exposes the global broadcast channel. Prefer Session.Events for
// per-transfer handling; progress events here may be dropped.
func (l *Lantern) Events() <-chan Event {
	return l.events
}

// Subscribe adds a broadcast listener with the given buffer size; the
// returned function unsubscribes and closes the channel.
func (l *Lantern) Subscribe(buffer int) (<-chan Event, func()) {
	return l.subscribe(buffer, nil)
}

func (l *Lantern) subscribe(buffer int, terminal func(Event)) (<-chan Event, func()) {
	if buffer < 1 {
		buffer = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nextSubscriber++
	id := l.nextSubscriber
	ch := make(chan Event, buffer)
	if l.closed {
		close(ch)
		return ch, func() {}
	}
	l.subscribers[id] = subscription{channel: ch, terminal: terminal}
	return ch, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if current, ok := l.subscribers[id]; ok {
			delete(l.subscribers, id)
			close(current.channel)
		}
	}
}

func (l *Lantern) newSession(ctx context.Context, id string) *Session {
	sessionCtx, cancel := context.WithCancel(ctx)
	s := &Session{
		id:     id,
		ctx:    sessionCtx,
		cancel: cancel,
		state:  TransferRunning,
		done:   make(chan struct{}),
	}
	s.events, s.unsubscribe = l.subscribe(sessionBufferSize, func(e Event) {
		if e.TransferID != s.ID() {
			return
		}
		if e.Type == EventTransferDone {
			s.finish(TransferDone)
		} else {
			s.finish(TransferFailed)
		}
	})
	s.mu.Lock()
	s.state = TransferRunning
	s.mu.Unlock()
	context.AfterFunc(sessionCtx, func() { s.finish(TransferCanceled) })
	return s
}

// ShareSession advertises path under a fresh code and returns a session
// tracking the transfer. The session's context drives advertisement and
// the transfer; closing it stops both.
func (l *Lantern) ShareSession(ctx context.Context, path string) (*Session, *Peer, error) {
	code, err := p2p.GenerateCode()
	if err != nil {
		return nil, nil, fmt.Errorf("generate code: %w", err)
	}
	session := l.newSession(ctx, code)
	peer, err := l.shareWithCode(session.ctx, path, code)
	if err != nil {
		session.Close()
		return nil, nil, err
	}
	return session, peer, nil
}

func (l *Lantern) shareWithCode(ctx context.Context, path string, code string) (*Peer, error) {
	if code == "" {
		return nil, fmt.Errorf("share code must not be empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat path: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory: %s", path)
	}

	progress := make(chan p2p.TransferProgress, broadcastBufferSize)

	advCtx, advCancel := context.WithCancel(ctx)
	if err := l.node.RegisterShareHandler(code, path, progress, advCancel); err != nil {
		advCancel()
		return nil, err
	}

	if err := l.node.AdvertiseLocal(code); err != nil {
		advCancel()
		return nil, fmt.Errorf("local advertise: %w", err)
	}

	go l.forwardProgress(advCtx, progress, code)

	go func() {
		if err := l.node.Advertise(advCtx, code); err != nil && ctx.Err() == nil {
			l.emit(Event{TransferID: code, Type: EventError, Err: fmt.Errorf("advertise: %w", err)})
		}
		advCancel()
	}()

	l.emit(Event{TransferID: code, Type: EventPeerFound, Code: code})

	return &Peer{
		ID:       l.node.Host.ID().String(),
		Code:     code,
		FileName: info.Name(),
		FileSize: info.Size(),
	}, nil
}

// ReceiveSession discovers the peer behind code and pulls the file into
// outputDir, resuming any partial download. See ShareSession for the
// session contract.
func (l *Lantern) ReceiveSession(ctx context.Context, code string, outputDir string) (*Session, *Peer, error) {
	session := l.newSession(ctx, code)
	peer, err := l.receive(session.ctx, code, outputDir)
	if err != nil {
		session.Close()
		return nil, nil, err
	}
	return session, peer, nil
}

func (l *Lantern) receive(ctx context.Context, code string, outputDir string) (*Peer, error) {
	pi, err := l.node.Discover(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}

	l.emit(Event{TransferID: code, Type: EventPeerConnected, PeerID: pi.ID.String()})

	progress := make(chan p2p.TransferProgress, broadcastBufferSize)
	go l.forwardProgress(ctx, progress, code)

	l.node.RegisterReceive(ctx, pi, code, outputDir, progress)

	return &Peer{
		ID:   pi.ID.String(),
		Code: code,
	}, nil
}

func (l *Lantern) forwardProgress(ctx context.Context, progress <-chan p2p.TransferProgress, transferID string) {
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-progress:
			if !ok {
				return
			}
			if p.Err != nil {
				l.emit(Event{TransferID: transferID, Type: EventError, Err: p.Err})
				continue
			}
			if p.Done {
				l.emit(Event{TransferID: transferID, Type: EventTransferDone, FileName: p.FileName})
			} else {
				l.emit(Event{TransferID: transferID, Type: EventTransferProgress, FileName: p.FileName, Bytes: p.Bytes, Total: p.Total})
			}
		}
	}
}

// Close shuts down the node and all subscriptions. It is idempotent and
// reports the first shutdown error.
func (l *Lantern) Close() error {
	l.closeOnce.Do(func() {
		l.closeErr = l.node.Close()
		l.mu.Lock()
		l.closed = true
		for id, sub := range l.subscribers {
			close(sub.channel)
			delete(l.subscribers, id)
		}
		l.mu.Unlock()
	})
	return l.closeErr
}

func (l *Lantern) emit(e Event) {
	l.mu.Lock()
	subs := make([]subscription, 0, len(l.subscribers))
	for _, sub := range l.subscribers {
		subs = append(subs, sub)
	}
	events := l.events
	l.mu.Unlock()

	select {
	case events <- e:
	default:
	}
	terminal := e.Type == EventTransferDone || e.Type == EventError
	for _, sub := range subs {
		if terminal {
			select {
			case sub.channel <- e:
			default:
				select {
				case <-sub.channel:
				default:
				}
				select {
				case sub.channel <- e:
				default:
				}
			}
		} else {
			select {
			case sub.channel <- e:
			default:
			}
		}
		if terminal && sub.terminal != nil {
			sub.terminal(e)
		}
	}
}
