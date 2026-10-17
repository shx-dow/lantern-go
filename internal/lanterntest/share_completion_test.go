package lanterntest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

// `lantern send` must return after a paired receiver has taken the share.
//
// This is the test the CLI work needed and could not build: driving a command
// needs the process-level stdout seam, whereas the property lives a layer
// down, in the serving device's session.
//
// The hang was this. A paired receiver resolves the share code and then reads
// the file over the fs read protocol, so serveShare never runs and nothing
// reported completion. The sender's session saw no progress and no Done, and
// runSend waited for an event that could not arrive -- after a transfer that
// had already succeeded.
func TestASenderLearnsThatAPairedReceiverFetchedItsShare(t *testing.T) {
	shared := t.TempDir()
	const name = "notes.txt"
	const content = "the bytes that must land"
	writeShared(t, shared, name, content)

	p := NewPair(t, shared)
	out := t.TempDir()

	session, share, err := p.Sender.Lantern.ShareSession(Ctx(t), shared+"/"+name)
	if err != nil {
		t.Fatalf("the sender could not share the file: %v", err)
	}
	defer session.Close()
	p.BridgeDiscovery(t, share.Code)

	events := session.Events()
	received, _, err := p.Receiver.Lantern.ReceiveSession(Ctx(t), share.Code, out)
	if err != nil {
		t.Fatalf("the receiver could not redeem the code: %v", err)
	}
	for range received.Events() {
	}
	received.Close()

	// The sender must now know. Bounded, so a regression fails here rather
	// than waiting out the helper's own deadline.
	deadline := time.After(15 * time.Second)
	for {
		done := false
		for _, e := range pending(events) {
			switch e.Type {
			case 1: // EventTransferDone
				done = true
			case 4: // EventError
				t.Fatalf("the sender session reported an error: %v", e.Err)
			}
		}
		if done {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the sender never learned the share was fetched, after a transfer that succeeded")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// And the share is spent. Asserted on the resolve rather than on a fetch,
	// because ReceiveSession returns as soon as it has a peer and reports any
	// refusal later on the event stream -- it would succeed here either way.
	again, err := p.Sender.Node.ResolveShare(Ctx(t), peer.AddrInfo{
		ID: p.Sender.Node.Host.ID(), Addrs: p.Sender.Node.Host.Addrs(),
	}, share.Code)
	if err == nil && again != nil {
		t.Error("a spent share code still resolves over the read path")
	}
}

// pending drains whatever the session has emitted so far, without blocking.
// Events are buffered, so a non-blocking receive sees everything already sent.
func pending(events <-chan lantern.Event) []lantern.Event {
	var out []lantern.Event
	for {
		select {
		case e, ok := <-events:
			if !ok {
				return out
			}
			out = append(out, e)
		default:
			return out
		}
	}
}

// An unrelated reader of the same file must not complete a share it never
// asked for. A peer that reads the path directly, without ever presenting the
// code, is reading -- not fetching.
func TestAReaderThatNeverPresentedTheCodeDoesNotCompleteTheShare(t *testing.T) {
	shared := t.TempDir()
	const name = "notes.txt"
	const content = "read directly, not fetched"
	writeShared(t, shared, name, content)

	p := NewPair(t, shared)

	session, _, err := p.Sender.Lantern.ShareSession(Ctx(t), shared+"/"+name)
	if err != nil {
		t.Fatalf("the sender could not share the file: %v", err)
	}
	defer session.Close()

	// ReadRemote by peer ID. No code is presented anywhere.
	res, err := p.Receiver.Lantern.ReadRemote(Ctx(t), p.Sender.ID, shared+"/"+name, 0, 0)
	if err != nil {
		t.Fatalf("a paired read must work: %v", err)
	}
	if string(res.Data) != content {
		t.Fatalf("read returned %q", res.Data)
	}

	// The share must still be live: no completion was earned.
	select {
	case e, ok := <-session.Events():
		if !ok {
			t.Fatal("the session ended")
		}
		if e.Type == 1 || e.Type == 4 {
			t.Fatalf("a plain read completed a share that was never fetched: %+v", e)
		}
	case <-time.After(500 * time.Millisecond):
		// Expected: nothing at all.
	}
}

// writeShared puts a file into a shared root for a test to serve.
func writeShared(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
