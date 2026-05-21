package lantern

import (
	"context"
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
	Type      EventType
	PeerID    string
	FileName  string
	Bytes     int64
	Total     int64
	Code      string
	Err       error
}

type Peer struct {
	ID       string
	Code     string
	FileName string
	FileSize int64
}

type Lantern struct {
	cfg    Config
	events chan Event
}

func New(cfg Config) (*Lantern, error) {
	if cfg.Port == 0 {
		cfg.Port = 0
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "."
	}
	l := &Lantern{
		cfg:    cfg,
		events: make(chan Event, 64),
	}
	return l, nil
}

func (l *Lantern) Events() <-chan Event {
	return l.events
}

func (l *Lantern) Share(ctx context.Context, path string) (*Peer, error) {
	return nil, ErrNotImplemented
}

func (l *Lantern) Receive(ctx context.Context, code string, outputDir string) (*Peer, error) {
	return nil, ErrNotImplemented
}

func (l *Lantern) Close() error {
	return nil
}

var ErrNotImplemented = &Error{"not implemented yet", 0}

type Error struct {
	Msg string
	Code int
}

func (e *Error) Error() string {
	return e.Msg
}
