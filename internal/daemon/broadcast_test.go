package daemon

import (
	"sync"
	"testing"
	"time"
)

// TestBroadcastRacesWithUnsubscribe pins that a subscriber going away can never
// turn into a send on a closed channel.
//
// broadcast used to snapshot the subscriber list under subsMu, release the lock,
// and only then send. An SSE client disconnecting in that window ran unsub,
// which deleted and closed the channel under the same lock, and the in-flight
// send panicked. That panic runs on the watch goroutine, which net/http does not
// recover, so the whole daemon died on any client disconnect. Holding subsMu
// across the sends closes the window; the sends are non-blocking, so the lock is
// never held for long.
//
// Run under -race: without the race detector this is a timing test that may pass
// by luck, which is how the bug survived.
func TestBroadcastRacesWithUnsubscribe(t *testing.T) {
	d := New(nil)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, unsub := d.Subscribe(1)
				d.broadcast(EventDTO{Type: "progress", ID: "x"})
				unsub()
			}
		}()
	}

	// Long enough for the churn to overlap repeatedly.
	time.Sleep(250 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestUnsubscribeIsIdempotent pins that calling the returned func twice, or
// after the subscriber is already gone, is safe. It is the same lock discipline
// as TestBroadcastRacesWithUnsubscribe but on the caller's side.
func TestUnsubscribeIsIdempotent(t *testing.T) {
	d := New(nil)
	ch, unsub := d.Subscribe(2)

	d.broadcast(EventDTO{Type: "progress", ID: "a"})
	select {
	case e := <-ch:
		if e.ID != "a" {
			t.Fatalf("event = %+v, want ID a", e)
		}
	default:
		t.Fatal("the subscriber did not receive its event")
	}

	unsub()
	unsub() // must not panic on an already-closed channel
	d.broadcast(EventDTO{Type: "progress", ID: "b"})
}

// TestBroadcastDropsForSlowConsumers pins the documented drop-on-backpressure
// behaviour, so the fix to broadcast's locking cannot quietly turn into blocking
// a watcher behind a subscriber that stopped reading.
func TestBroadcastDropsForSlowConsumers(t *testing.T) {
	d := New(nil)
	_, unsub := d.Subscribe(1)
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more events than the buffer holds, with nothing draining.
		for i := 0; i < 1000; i++ {
			d.broadcast(EventDTO{Type: "progress", ID: "x"})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("broadcast blocked on a full subscriber buffer")
	}
}
