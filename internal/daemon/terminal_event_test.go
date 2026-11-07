package daemon

import (
	"testing"
	"time"

	"github.com/shx-dow/lantern-go/pkg/lantern"
)

// TestBroadcastKeepsTerminalEventForSlowConsumer pins that a full subscriber
// buffer cannot swallow the event that ends a transfer.
//
// Progress may be dropped when a client falls behind; the ending may not.
// Before this, the terminal event was dropped like any other, and the client
// kept waiting on a stream that was still open, so the polling fallback never
// ran.
func TestBroadcastKeepsTerminalEventForSlowConsumer(t *testing.T) {
	d := New(nil)
	ch, unsub := d.Subscribe(1)
	defer unsub()

	d.broadcast(EventDTO{Type: "progress", ID: "x"})
	d.broadcast(EventDTO{Type: "done", ID: "x"})

	select {
	case e := <-ch:
		if e.Type != "done" {
			t.Fatalf("first event = %q, want the terminal done", e.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal event was dropped")
	}
}

// TestWatchEmitsTerminalWhenSessionCloses pins that a session closing without
// a terminal event still ends the transfer for a subscriber.
//
// watch marked the record canceled and wrote history, but broadcast nothing:
// an SSE client held an open stream and waited forever. The client only polls
// when the stream closes, and this stream never did.
func TestWatchEmitsTerminalWhenSessionCloses(t *testing.T) {
	const id = "t1"
	now := time.Now()
	d := New(nil)
	d.records[id] = &Record{ID: id, State: StateRunning, StartedAt: now, UpdatedAt: now}

	events, unsub := d.Subscribe(8)
	defer unsub()

	stream := make(chan lantern.Event)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.watch(stream, id)
	}()
	close(stream)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watch did not return after the session closed")
	}

	select {
	case e := <-events:
		if e.Type != "cancelled" || e.ID != id {
			t.Fatalf("event = %+v, want cancelled for %s", e, id)
		}
	case <-time.After(time.Second):
		t.Fatal("no terminal event was broadcast when the session closed")
	}
}
