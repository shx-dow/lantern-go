package p2p

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// A read that delivers every byte completes the share.
//
// This is a regression guard for the read-path completion, not a negative
// control: nothing here is mutated to watch it fail, because the defect it
// guards was a silent one -- a session that never sees Done and so waits
// forever after a transfer that succeeded. The control for that lives in
// internal/lanterntest, where the session's event stream is observable.
//
// It also guards the ordering fix below. Reporting completion above io.CopyN
// rather than after it is strictly more correct, but no cheap test can observe
// the difference: a successful write into a stream buffer is indistinguishable
// from delivery, and the protocol caps a read at 256 KiB, so the final chunk
// is always small enough to be absorbed by the stream window.
func TestAReadThatDeliversEveryByteCompletesTheShare(t *testing.T) {
	root := t.TempDir()
	const content = "the bytes that must land"
	path := filepath.Join(root, "notes.txt")
	writeFile(t, path, content)

	requester, provider, pi := sharePair(t, root)

	const code = "completed-read"
	progress := make(chan TransferProgress, 8)
	if err := provider.RegisterShareHandler(fsCtx(t), code, path, progress); err != nil {
		t.Fatalf("share: %v", err)
	}

	info, err := requester.ResolveShare(fsCtx(t), pi, code)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if info == nil {
		t.Fatal("the share did not resolve")
	}

	_, _, data, eof, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: info.Path})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !eof || string(data) != content {
		t.Fatalf("short read: eof=%v data=%q", eof, data)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case p := <-progress:
			if p.Done {
				if p.Bytes != int64(len(content)) || p.Total != p.Bytes {
					t.Fatalf("completion reported %d/%d for %d bytes", p.Bytes, p.Total, len(content))
				}
				return
			}
		case <-deadline:
			t.Fatal("a read that delivered every byte never reported the share fetched")
		}
	}
}

// A read that stops short of the end of the file is not a completed fetch. The
// protocol chunks at 256 KiB, so every read of a larger file but the last is
// this case, and none of them may complete the share.
func TestAPartialReadDoesNotCompleteTheShare(t *testing.T) {
	root := t.TempDir()
	body := make([]byte, 8<<20)
	for i := range body {
		body[i] = byte('a' + i%26)
	}
	path := filepath.Join(root, "big.bin")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}

	requester, provider, pi := sharePair(t, root)

	const code = "partial-read"
	progress := make(chan TransferProgress, 8)
	if err := provider.RegisterShareHandler(fsCtx(t), code, path, progress); err != nil {
		t.Fatalf("share: %v", err)
	}

	info, err := requester.ResolveShare(fsCtx(t), pi, code)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// One chunk, deliberately short of the end.
	const chunk = 4096
	_, _, data, eof, err := requester.ReadFS(fsCtx(t), pi, FSRequest{
		Op: OpRead, Path: info.Path, Length: chunk,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if eof || len(data) != chunk {
		t.Fatalf("expected a short non-final read, got eof=%v len=%d", eof, len(data))
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case p := <-progress:
			if p.Done {
				t.Fatal("a read of one chunk completed an 8 MiB share")
			}
		case <-deadline:
			return
		}
	}
}

// sharePair is fsPair with the provider kept, so a test can register a share
// on the same node that serves the fs protocol.
func sharePair(t *testing.T, root string) (*Node, *Node, peer.AddrInfo) {
	t.Helper()
	provider := fsHost(t)
	requester := fsHost(t)
	provider.SetReadAccess(allowRootsFor([]string{root}, requester.Host.ID().String()))
	provider.SetWriteAccess(grantEveryone(nil))
	provider.RegisterFSHandler()
	return requester, provider, peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
}

// Completing a share closes its progress channel, the way serveShare does.
//
// The channel carrying Done is buffered, and the share's context is cancelled
// straight after the message is queued. A forwarder selecting on both can take
// the cancelled context and drop the queued message, so the receiver
// succeeds and the sender waits forever on an event that was produced but
// never delivered. Closing the channel is what rules that out: forwardProgress
// drains what is buffered, emits the event, then sees the close and returns.
//
// Asserting the close is what makes this control deterministic. Asserting the
// event's arrival would only catch the race some of the time -- with both cases
// ready, the select picks between them at random.
func TestCompletingAShareClosesItsProgressChannel(t *testing.T) {
	root := t.TempDir()
	const content = "the bytes that must land"
	path := filepath.Join(root, "notes.txt")
	writeFile(t, path, content)

	requester, provider, pi := sharePair(t, root)

	const code = "close-on-complete"
	progress := make(chan TransferProgress, 8)
	if err := provider.RegisterShareHandler(fsCtx(t), code, path, progress); err != nil {
		t.Fatalf("share: %v", err)
	}

	info, err := requester.ResolveShare(fsCtx(t), pi, code)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: info.Path}); err != nil {
		t.Fatalf("read: %v", err)
	}

	// The completion itself.
	select {
	case p := <-progress:
		if !p.Done {
			t.Fatalf("expected a completion, got %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the share was never completed")
	}

	// And the channel is closed, so a forwarder cannot miss the message above
	// by taking a cancelled context instead.
	select {
	case _, ok := <-progress:
		if ok {
			t.Fatal("the progress channel was left open after completion")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the progress channel was never closed after completion")
	}
}
