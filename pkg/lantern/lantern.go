package lantern

import (
	"context"
	"fmt"
	"os"

	"github.com/shx-dow/lantern-go/internal/p2p"
)

type Config struct {
	Port      int
	DataDir   string
	Bootstrap []string
}

type EventType int

const (
	EventPeerFound    EventType = iota
	EventPeerConnected
	EventTransferStarted
	EventTransferProgress
	EventTransferDone
	EventError
)

type Event struct {
	Type     EventType
	PeerID   string
	FileName string
	Bytes    int64
	Total    int64
	Code     string
	Err      error
}

type Peer struct {
	ID       string
	Code     string
	FileName string
	FileSize int64
}

type Lantern struct {
	node   *p2p.Node
	events chan Event
}

func New(cfg Config) (*Lantern, error) {
	if cfg.DataDir == "" {
		cfg.DataDir = "."
	}

	node, err := p2p.NewNode(cfg.Port, cfg.Bootstrap)
	if err != nil {
		return nil, fmt.Errorf("init p2p: %w", err)
	}

	l := &Lantern{
		node:   node,
		events: make(chan Event, 64),
	}
	return l, nil
}

func (l *Lantern) Events() <-chan Event {
	return l.events
}

func (l *Lantern) Share(ctx context.Context, path string) (*Peer, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat path: %w", err)
	}

	code, err := p2p.GenerateCode()
	if err != nil {
		return nil, fmt.Errorf("generate code: %w", err)
	}

	l.emit(Event{Type: EventPeerFound, Code: code})

	advCtx, advCancel := context.WithCancel(ctx)
	defer advCancel()
	go func() {
		if err := l.node.Advertise(advCtx, code); err != nil && ctx.Err() == nil {
			l.emit(Event{Type: EventError, Err: fmt.Errorf("advertise: %w", err)})
		}
	}()

	progress := make(chan p2p.TransferProgress)
	go l.handleProgress(progress)

	if err := l.node.SendFile(ctx, code, path, progress); err != nil {
		return nil, err
	}

	return &Peer{
		ID:       l.node.Host.ID().String(),
		Code:     code,
		FileName: info.Name(),
		FileSize: info.Size(),
	}, nil
}

func (l *Lantern) Receive(ctx context.Context, code string, outputDir string) (*Peer, error) {
	pi, err := l.node.Discover(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}

	l.emit(Event{Type: EventPeerConnected, PeerID: pi.ID.String()})
	l.emit(Event{Type: EventTransferStarted})

	progress := make(chan p2p.TransferProgress)
	go l.handleProgress(progress)

	if err := l.node.ReceiveFile(ctx, pi, code, outputDir, progress); err != nil {
		return nil, err
	}

	return &Peer{
		ID:   pi.ID.String(),
		Code: code,
	}, nil
}

func (l *Lantern) handleProgress(progress <-chan p2p.TransferProgress) {
	for p := range progress {
		if p.Err != nil {
			l.emit(Event{Type: EventError, Err: p.Err})
			continue
		}
		if p.Done {
			l.emit(Event{Type: EventTransferDone, FileName: p.FileName})
		} else {
			if p.Bytes == 0 && p.Total == 0 {
				continue
			}
			l.emit(Event{Type: EventTransferProgress, FileName: p.FileName, Bytes: p.Bytes, Total: p.Total})
		}
	}
}

func (l *Lantern) Close() error {
	l.node.Close()
	return nil
}

func (l *Lantern) emit(e Event) {
	select {
	case l.events <- e:
	default:
	}
}
