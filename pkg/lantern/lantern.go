package lantern

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/shx-dow/lantern-go/internal/p2p"
)

type Config struct {
	Port      int
	DataDir   string
	Bootstrap []string
}

type EventType int

const (
	EventTransferProgress EventType = iota
	EventTransferDone
	EventPeerFound
	EventPeerConnected
	EventError
)

type TransferState int

const (
	TransferPending TransferState = iota
	TransferRunning
	TransferDone
	TransferFailed
	TransferCanceled
)

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

type Peer struct {
	ID       string
	Code     string
	FileName string
	FileSize int64
}

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

type subscription struct {
	channel  chan Event
	terminal func(Event)
}

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
	doneOnce    sync.Once
}

func (s *Session) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

func (s *Session) Events() <-chan Event { return s.events }

func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) State() TransferState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Session) Cancel() { s.Close() }

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
		s.doneOnce.Do(func() { close(s.done) })
		go s.unsubscribe()
	})
}

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
		events:      make(chan Event, 64),
		subscribers: make(map[uint64]subscription),
	}
	return l, nil
}

func (l *Lantern) Events() <-chan Event {
	return l.events
}

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
	s.events, s.unsubscribe = l.subscribe(128, func(e Event) {
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
	go func() {
		<-sessionCtx.Done()
		s.finish(TransferCanceled)
	}()
	return s
}

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

	progress := make(chan p2p.TransferProgress)
	go l.forwardProgress(progress, code)

	advCtx, advCancel := context.WithCancel(ctx)
	if err := l.node.RegisterShareHandler(code, path, progress, advCancel); err != nil {
		advCancel()
		return nil, err
	}

	if err := l.node.AdvertiseLocal(code); err != nil {
		advCancel()
		return nil, fmt.Errorf("local advertise: %w", err)
	}

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

	progress := make(chan p2p.TransferProgress)
	go l.forwardProgress(progress, code)

	l.node.RegisterReceive(ctx, pi, code, outputDir, progress)

	return &Peer{
		ID:   pi.ID.String(),
		Code: code,
	}, nil
}

func (l *Lantern) forwardProgress(progress <-chan p2p.TransferProgress, transferID string) {
	for p := range progress {
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
