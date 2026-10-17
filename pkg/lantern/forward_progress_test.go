package lantern

import (
	"context"
	"testing"
	"time"

	"github.com/shx-dow/lantern-go/internal/p2p"
)

// A completion queued before cancellation is still reported.
//
// A share's completion queues Done, closes the channel, and cancels the
// forwarder's context. If the forwarder is between iterations when that
// happens, both the closed channel and the cancelled context are ready at its
// next select, and which one it takes is arbitrary. Returning on cancellation
// can therefore return without emitting the completion: the receiver has the
// file, and the sender waits forever.
//
// Closing the channel narrows the window. It does not remove it, because the
// forwarder may not have been at the select when either happened.
//
// Found by Greptile on #50.
func TestACompletionQueuedBeforeCancellationIsStillReported(t *testing.T) {
	// Both ready before the forwarder ever looks: the message is queued, the
	// channel is closed, and the context is cancelled. There is no ordering
	// in which a forwarder that returns on cancellation can be blamed for
	// having taken the wrong branch late -- the window is simply already open.
	const rounds = 50
	for round := range rounds {
		l := &Lantern{events: make(chan Event, 8)}

		ctx, cancel := context.WithCancel(context.Background())
		progress := make(chan p2p.TransferProgress, 4)
		progress <- p2p.TransferProgress{FileName: "notes.txt", Bytes: 23, Total: 23, Done: true}
		close(progress)
		cancel()

		done := make(chan struct{})
		go func() {
			defer close(done)
			l.forwardProgress(ctx, progress, "share-1")
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: forwardProgress never returned", round)
		}

		// The completion was already queued when the forwarder started. It must
		// have been emitted rather than discarded.
		var sawDone bool
	drain:
		for {
			select {
			case e := <-l.events:
				if e.Type == EventTransferDone {
					sawDone = true
					break drain
				}
			default:
				break drain
			}
		}
		if !sawDone {
			t.Fatalf("round %d: a completion queued before cancellation was dropped", round)
		}
	}
}

// Draining on cancellation must not invent events, and must not loop on a
// closed channel: the forwarder returns rather than spinning.
func TestDrainingOnCancellationReturnsOnAClosedChannel(t *testing.T) {
	l := &Lantern{events: make(chan Event, 8)}

	ctx, cancel := context.WithCancel(context.Background())
	progress := make(chan p2p.TransferProgress, 4)
	close(progress)
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.forwardProgress(ctx, progress, "share-1")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("forwardProgress spun on a closed channel instead of returning")
	}

	if len(l.events) != 0 {
		t.Fatalf("a forwarder with nothing queued emitted %d events", len(l.events))
	}
}
