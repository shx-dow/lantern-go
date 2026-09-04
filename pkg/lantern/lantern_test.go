package lantern

import (
	"context"
	"sync"
	"testing"
	"time"
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

func TestEmitDoesNotBlockOnSlowSubscriber(t *testing.T) {
	l := &Lantern{
		events:      make(chan Event, 64),
		subscribers: make(map[uint64]subscription),
	}
	slow, unsubSlow := l.Subscribe(1)
	defer unsubSlow()
	fast, unsubFast := l.Subscribe(64)
	defer unsubFast()

	// Fill the slow subscriber so its buffer is full and unread.
	l.emit(Event{TransferID: "x", Type: EventTransferProgress, Bytes: 1, Total: 10})
	<-slow // consume one to leave buffer state deterministic

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			l.emit(Event{TransferID: "x", Type: EventTransferProgress, Bytes: int64(i), Total: 20})
		}
		l.emit(Event{TransferID: "x", Type: EventTransferDone})
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("emit blocked on slow subscriber")
	}

	select {
	case e := <-fast:
		_ = e
	case <-time.After(5 * time.Second):
		t.Fatal("fast subscriber received nothing")
	}
}
