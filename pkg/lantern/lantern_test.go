package lantern

import (
	"context"
	"testing"
)

func TestSubscribeReceivesEventsAndStopsOnCancel(t *testing.T) {
	l := &Lantern{
		events:      make(chan Event, 1),
		subscribers: make(map[uint64]subscription),
	}
	events, unsubscribe := l.Subscribe(1)
	l.emit(Event{TransferID: "transfer-1", Type: EventTransferDone})

	got := <-events
	if got.TransferID != "transfer-1" || got.Type != EventTransferDone {
		t.Fatalf("got %+v", got)
	}
	unsubscribe()
	if _, ok := <-events; ok {
		t.Fatal("subscription remained open after cancellation")
	}
}

func TestSubscribeAfterCloseIsClosed(t *testing.T) {
	l := &Lantern{
		events:      make(chan Event, 1),
		subscribers: make(map[uint64]subscription),
		closed:      true,
	}
	events, unsubscribe := l.Subscribe(1)
	defer unsubscribe()
	if _, ok := <-events; ok {
		t.Fatal("subscription was open after close")
	}
}

func TestSessionTracksOnlyItsTransfer(t *testing.T) {
	l := &Lantern{
		events:      make(chan Event, 1),
		subscribers: make(map[uint64]subscription),
	}
	s := l.newSession(context.Background(), "transfer-1")
	defer s.Close()

	l.emit(Event{TransferID: "transfer-2", Type: EventTransferDone})
	if s.State() != TransferRunning {
		t.Fatalf("unrelated event changed state to %v", s.State())
	}
	l.emit(Event{TransferID: "transfer-1", Type: EventTransferDone})
	if s.State() != TransferDone {
		t.Fatalf("session state is %v, want done", s.State())
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("session did not close Done")
	}
}
