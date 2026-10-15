package lantern

import (
	"sync"
	"testing"
	"time"
)

// Closing a session while the transfer is still reporting must not send on a
// closed channel.
//
// The race was caught by -race on macOS rather than written here first, which
// is the wrong order. It is reproduced deterministically instead: emit and
// unsubscribe are run against each other repeatedly, and with the lock held
// across the fan-out the pair is safe at any interleaving, so the test asserts
// the shape that makes it safe — every send happens under the lock that also
// guards the close.
//
// What this cannot do is fail on the old code reliably. The window is one
// instruction wide, so the loop below is a way of exercising it many times
// rather than a guarantee. The guarantee is the lock.
func TestEmittingWhileASessionUnsubscribesIsSafe(t *testing.T) {
	l, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	const rounds = 200
	for i := range rounds {
		_, unsubscribe := l.subscribe(1, nil)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			// The transfer reporting its own completion as it finishes.
			l.emit(Event{TransferID: "t", Type: EventTransferDone})
		}()
		go func() {
			defer wg.Done()
			// What `defer session.Close()` does on the line after that.
			unsubscribe()
		}()
		wg.Wait()

		// A panic here fails the test; a race is caught by -race. Either is a
		// failure, so both paths are worth having run.
		_ = i
	}
}

// A subscriber that has gone must stop receiving, and a subscriber that has not
// must keep receiving. The fix for the race narrows neither: the fan-out still
// runs under the lock, it just does not release it first.
func TestASubscribedChannelStillReceivesAfterTheFix(t *testing.T) {
	l, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	ch, unsubscribe := l.subscribe(4, nil)
	defer unsubscribe()

	l.emit(Event{TransferID: "t", Type: EventTransferProgress, Bytes: 7})

	select {
	case e := <-ch:
		if e.Bytes != 7 {
			t.Errorf("received %+v, want the event that was emitted", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a subscribed channel stopped receiving events")
	}
}

// Unsubscribing must still close the channel, or a reader ranging over it would
// block forever. Holding the lock across emit must not change that.
func TestUnsubscribingStillClosesTheChannel(t *testing.T) {
	l, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	ch, unsubscribe := l.subscribe(4, nil)
	unsubscribe()

	// A closed channel yields immediately; an open one would need the timeout.
	select {
	case _, open := <-ch:
		if open {
			t.Error("the channel yielded an event after unsubscribing")
		}
	case <-time.After(2 * time.Second):
		t.Error("unsubscribing did not close the channel")
	}
}
