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

// A share reserved by one reader is not taken by a second resolve.
//
// A later resolve used to overwrite readPeer. So if peer A resolved the code
// and started downloading, and peer B then resolved the same code and stopped
// before reading, A's final read no longer matched anything: no completion, and
// the sender waited forever. A bystander who never read a byte was enough to
// hang the device that actually served the file.
func TestASecondResolveDoesNotStealTheShareFromItsReader(t *testing.T) {
	root := t.TempDir()
	const content = "the bytes that must land"
	path := filepath.Join(root, "notes.txt")
	writeFile(t, path, content)

	provider, readers, pi := providerWithReaders(t, root, 2)

	const code = "reserved"
	progress := make(chan TransferProgress, 8)
	if err := provider.RegisterShareHandler(fsCtx(t), code, path, progress); err != nil {
		t.Fatalf("share: %v", err)
	}
	a, b := readers[0], readers[1]

	// A resolves and is the reader.
	info, err := a.ResolveShare(fsCtx(t), pi, code)
	if err != nil {
		t.Fatalf("A resolve: %v", err)
	}
	// B resolves the same code, then goes away without reading.
	//
	// A must not resolve again to fetch: a second resolve re-marks the
	// reservation and hides the overwrite this test exists to catch. The path
	// from A's first resolve is what A reads.
	if _, err := b.ResolveShare(fsCtx(t), pi, code); err != nil {
		t.Fatalf("B resolve: %v", err)
	}

	// A fetches, and must still be credited with it.
	if _, _, data, eof, err := a.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: info.Path}); err != nil {
		t.Fatalf("A read: %v", err)
	} else if !eof || string(data) != content {
		t.Fatalf("A short read: eof=%v data=%q", eof, data)
	}

	select {
	case p := <-progress:
		if p.Done {
			return
		}
		t.Fatalf("unexpected progress before completion: %+v", p)
	case <-time.After(5 * time.Second):
		t.Fatal("the peer that actually fetched the share was not credited, after a second peer resolved it")
	}
}

// Two codes for one file are two shares. Resolving one must not spend the
// other.
//
// Marking by path marked both, so the completion could land on either, and
// map order decided which. The receiver would be told a code was spent that it
// never presented, while the share it did resolve was left waiting.
//
// Run repeatedly because the original behaviour was a coin flip: whichever
// share the ranging found first was the one spent. Twenty rounds miss that
// about once in a million.
func TestResolvingOneCodeDoesNotSpendAnotherForTheSameFile(t *testing.T) {
	const rounds = 20
	for round := range rounds {
		root := t.TempDir()
		const content = "the bytes that must land"
		path := filepath.Join(root, "notes.txt")
		writeFile(t, path, content)

		requester, provider, pi := sharePair(t, root)

		first := make(chan TransferProgress, 8)
		second := make(chan TransferProgress, 8)
		if err := provider.RegisterShareHandler(fsCtx(t), "code-one", path, first); err != nil {
			t.Fatalf("share one: %v", err)
		}
		if err := provider.RegisterShareHandler(fsCtx(t), "code-two", path, second); err != nil {
			t.Fatalf("share two: %v", err)
		}

		info, err := requester.ResolveShare(fsCtx(t), pi, "code-one")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if _, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: info.Path}); err != nil {
			t.Fatalf("read: %v", err)
		}

		// The share that was presented is spent.
		select {
		case p := <-first:
			if !p.Done {
				t.Fatalf("round %d: code-one reported %+v instead of a completion", round, p)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: code-one was never completed", round)
		}

		// The share that was not is untouched: silent, still open, still live.
		select {
		case p, ok := <-second:
			if !ok {
				t.Fatalf("round %d: the share that was never resolved was closed", round)
			}
			t.Fatalf("round %d: the share that was never resolved reported %+v", round, p)
		default:
		}
		again, err := requester.ResolveShare(fsCtx(t), pi, "code-two")
		if err != nil {
			t.Fatalf("round %d: the untouched share stopped resolving: %v", round, err)
		}
		if again == nil {
			t.Fatalf("round %d: the untouched share no longer resolves", round)
		}
	}
}

// providerWithReaders brings up a provider serving root, trusted by n
// requesters, and returns those requesters alongside its address.
func providerWithReaders(t *testing.T, root string, n int) (*Node, []*Node, peer.AddrInfo) {
	t.Helper()
	provider := fsHost(t)
	readers := make([]*Node, 0, n)
	allowed := make([]string, 0, n)
	for range n {
		r := fsHost(t)
		readers = append(readers, r)
		allowed = append(allowed, r.Host.ID().String())
	}
	provider.SetReadAccess(allowRootsFor([]string{root}, allowed...))
	provider.SetWriteAccess(grantEveryone(nil))
	provider.RegisterFSHandler()
	return provider, readers, peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
}

// Completing a share removes its local advertisement, as serveShare does.
//
// Removing the share from the map is not the same as retiring the code. The
// rendezvous file on disk is what a later lookup on this machine finds, so
// without this a long-running sender leaves every completed read-path code
// still discoverable, pointing at a device that can no longer serve it.
func TestCompletingAShareRemovesItsLocalAdvertisement(t *testing.T) {
	root := t.TempDir()
	const content = "the bytes that must land"
	path := filepath.Join(root, "notes.txt")
	writeFile(t, path, content)

	requester, provider, pi := sharePair(t, root)
	provider.localDir = t.TempDir()

	const code = "advert-left-behind"
	if err := provider.AdvertiseLocal(code); err != nil {
		t.Fatalf("advertise: %v", err)
	}
	adFile, err := provider.peerFilePath(code)
	if err != nil {
		t.Fatalf("ad path: %v", err)
	}
	if _, err := os.Stat(adFile); err != nil {
		t.Fatalf("the advertisement was never written: %v", err)
	}

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

	select {
	case p := <-progress:
		if !p.Done {
			t.Fatalf("expected a completion, got %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the share was never completed")
	}

	// The advertisement for a spent code must not outlive the share.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(adFile); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the advertisement for a completed share is still on disk at %s", adFile)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A share registered under a relative path still completes.
//
// RegisterShareHandler stores the caller's path unchanged, while the resolve
// side works in a resolved one. Greptile reported that sharing a relative path
// therefore never records a matching reader and never completes the sender's
// session. Testing it rather than taking it: sameFile stats both sides, and a
// relative path stats against the working directory, which for the serving
// process is the same directory it was shared from.
func TestARelativeSharePathStillCompletes(t *testing.T) {
	const content = "shared by a relative path"
	const name = "relative-share-probe.txt"

	// Written into the working directory so the relative path below is the
	// path the caller would actually have passed. No chdir, which would move
	// the ground under every other test in this binary.
	abs := filepath.Join(".", name)
	if err := os.WriteFile(abs, []byte(content), 0600); err != nil {
		t.Skipf("the working directory is not writable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(abs) })

	root := filepath.Dir(mustAbs(t, "."))
	requester, provider, pi := sharePair(t, root)

	const code = "relative-path"
	progress := make(chan TransferProgress, 8)
	// The relative path, exactly as a caller sharing from here would pass it.
	if err := provider.RegisterShareHandler(fsCtx(t), code, name, progress); err != nil {
		t.Fatalf("share: %v", err)
	}

	info, err := requester.ResolveShare(fsCtx(t), pi, code)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, _, data, eof, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: info.Path}); err != nil {
		t.Fatalf("read: %v", err)
	} else if !eof || string(data) != content {
		t.Fatalf("short read: eof=%v data=%q", eof, data)
	}

	select {
	case p := <-progress:
		if p.Done {
			return
		}
		t.Fatalf("unexpected progress: %+v", p)
	case <-time.After(5 * time.Second):
		t.Fatal("a share registered under a relative path never completed")
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// Completion is decided by the size of the file that was read.
//
// fsRead opens the file, stats it, and copies from that open handle. If
// something replaces the path while the read runs, the handle still delivers
// every original byte and the receiver's hash check passes -- so the transfer
// succeeded. Re-stat-ing the path afterwards asks about a different file, sees
// a different size, and skips the completion for a fetch that plainly
// finished. Found by Greptile on #50.
//
// Deterministic rather than raced: the point is the contract, that the size
// passed in is authoritative, so the replacement is made first and the size
// the read would have seen is passed explicitly.
func TestCompletionUsesTheSizeOfTheFileThatWasRead(t *testing.T) {
	root := t.TempDir()
	const original = "the bytes that must land"
	path := filepath.Join(root, "notes.txt")
	writeFile(t, path, original)

	requester, provider, pi := sharePair(t, root)

	const code = "replaced-mid-read"
	progress := make(chan TransferProgress, 8)
	if err := provider.RegisterShareHandler(fsCtx(t), code, path, progress); err != nil {
		t.Fatalf("share: %v", err)
	}

	info, err := requester.ResolveShare(fsCtx(t), pi, code)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// The path is replaced with a file of a different size, as it would be if
	// another process rewrote it while the read was in flight.
	if err := os.WriteFile(path, []byte("s"), 0600); err != nil {
		t.Fatal(err)
	}

	// The read had already opened the original, and had its size.
	read := int64(len(original))
	provider.maybeCompleteShareRead(
		requester.Host.ID().String(), info.Path, true, 0, read, read,
	)

	select {
	case p := <-progress:
		if !p.Done {
			t.Fatalf("expected a completion, got %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a read that delivered every byte it opened was not completed, because the path changed underneath it")
	}
}
