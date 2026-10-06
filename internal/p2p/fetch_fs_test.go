package p2p

// End-to-end tests for fetching a share code over the read path.
//
// The claim under test: when the sender will serve a code over the read path,
// the bytes arrive through the fs protocol and the transfer protocol is never
// opened. That is the whole point of unifying the two, so it is asserted
// directly rather than inferred from a working download.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/storage"
)

// fetchSetup brings up a sender advertising code for a file inside root, a
// receiver that may read root, and a counter of transfer-protocol streams.
type fetchSetup struct {
	root   string
	code   string
	sender *Node
	// receiver is the node that fetches. FetchFS runs on it, because a fetch
	// is something a peer does to a sender, not to a sender to itself.
	receiver *Node
	pi       peer.AddrInfo
	// transferOpens counts streams that arrived on the transfer protocol. It
	// must stay zero when the read path carried the fetch.
	transferOpens int
}

func newFetchSetup(t *testing.T, code, content string) fetchSetup {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "gift.txt")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	sender := fsHost(t)
	sender.SetReadAccess(allowAllRoots(root))
	sender.RegisterFSHandler()

	// Count every stream on the transfer protocol. If a fetch that should have
	// gone over the read path opens one of these, the unification did not
	// happen, and a green download alone would not show it.
	opens := 0
	sender.Host.SetStreamHandler(ProtocolID, func(s network.Stream) {
		opens++
		defer s.Close()
		// Answer with nothing useful: this test is about which protocol ran.
		_ = s.Reset()
	})

	receiver := fsHost(t)
	pi := peer.AddrInfo{ID: sender.Host.ID(), Addrs: sender.Host.Addrs()}
	if err := receiver.Host.Connect(fsCtx(t), pi); err != nil {
		t.Fatal(err)
	}

	progress := make(chan TransferProgress, 8)
	if err := sender.RegisterShareHandler(code, path, progress); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		go func() {
			for range progress {
			}
		}()
		sender.ClearLocal(code)
		sender.forgetShare(code)
	})

	return fetchSetup{
		root: root, code: code, sender: sender, receiver: receiver,
		pi: pi, transferOpens: opens,
	}
}

// A paired receiver fetches the file over the read path: content lands, digests
// match, and the transfer protocol is never touched.
func TestFetchFromPairedSenderUsesReadPath(t *testing.T) {
	const content = "fetched over the read path, not the transfer protocol"
	const code = "11111111111111111111111111111111"
	s := newFetchSetup(t, code, content)

	out := t.TempDir()
	progress := make(chan TransferProgress, 16)
	if err := s.receiver.FetchFS(fetchCtx(t), s.pi, code, out, progress); err != nil {
		t.Fatalf("a paired fetch should succeed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(out, "gift.txt"))
	if err != nil {
		t.Fatalf("the file did not land: %v", err)
	}
	if string(got) != content {
		t.Fatalf("content = %q, want %q", got, content)
	}
	if s.transferOpens != 0 {
		t.Fatalf("the transfer protocol was opened %d times; the read path should have carried this fetch", s.transferOpens)
	}
}

// Progress and the terminal Done event must still arrive, because the daemon's
// record and the CLI's progress line depend on them.
func TestFetchOverReadPathReportsProgress(t *testing.T) {
	const code = "22222222222222222222222222222222"
	s := newFetchSetup(t, code, strings.Repeat("x", 300_000))

	out := t.TempDir()
	progress := make(chan TransferProgress, 64)
	if err := s.receiver.FetchFS(fetchCtx(t), s.pi, code, out, progress); err != nil {
		t.Fatal(err)
	}
	close(progress)

	var sawProgress, sawDone bool
	var lastBytes, total int64
	for p := range progress {
		if p.Err != nil {
			t.Fatalf("progress carried an error: %v", p.Err)
		}
		if p.Bytes > 0 && !p.Done {
			sawProgress = true
		}
		if p.Done {
			sawDone = true
		}
		lastBytes, total = p.Bytes, p.Total
	}
	if !sawProgress {
		t.Error("a fetch must report incremental progress, or the CLI shows nothing until it finishes")
	}
	if !sawDone {
		t.Error("a completed fetch must report Done, or the record never terminates")
	}
	if lastBytes != total || total != 300_000 {
		t.Errorf("final progress = %d/%d, want 300000/300000", lastBytes, total)
	}
}

// A large file spans several read chunks, since the read caps each call.
func TestFetchOverReadPathHandlesLargeFiles(t *testing.T) {
	// Comfortably more than one 4 MiB chunk.
	content := strings.Repeat("abcdefgh", 700_000) // 5.6 MB
	const code = "33333333333333333333333333333333"
	s := newFetchSetup(t, code, content)

	out := t.TempDir()
	progress := make(chan TransferProgress, 256)
	if err := s.receiver.FetchFS(fetchCtx(t), s.pi, code, out, progress); err != nil {
		t.Fatalf("a multi-chunk fetch should succeed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "gift.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(content) {
		t.Fatalf("got %d bytes, want %d", len(got), len(content))
	}
	if string(got) != content {
		t.Fatal("the reassembled file differs from the source")
	}
}

// An empty file is a legitimate share and must land as an empty file, not hang
// or fail.
func TestFetchOverReadPathHandlesEmptyFile(t *testing.T) {
	const code = "44444444444444444444444444444444"
	s := newFetchSetup(t, code, "")

	out := t.TempDir()
	progress := make(chan TransferProgress, 8)
	if err := s.receiver.FetchFS(fetchCtx(t), s.pi, code, out, progress); err != nil {
		t.Fatalf("an empty file should fetch cleanly: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "gift.txt"))
	if err != nil {
		t.Fatalf("the empty file did not land: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected an empty file, got %d bytes", len(got))
	}
}

// A file outside the sender's shared roots must not fetch over the read path at
// all. The receiver is told nothing and is expected to use the transfer path.
func TestFetchFromPairedSenderRefusesPathOutsideRoots(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}

	sender := fsHost(t)
	sender.SetReadAccess(allowAllRoots(root))
	sender.RegisterFSHandler()

	const code = "55555555555555555555555555555555"
	progress := make(chan TransferProgress, 8)
	if err := sender.RegisterShareHandler(code, outside, progress); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		go func() {
			for range progress {
			}
		}()
		sender.forgetShare(code)
	})

	receiver := fsHost(t)
	pi := peer.AddrInfo{ID: sender.Host.ID(), Addrs: sender.Host.Addrs()}
	if err := receiver.Host.Connect(fsCtx(t), pi); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	err := receiver.FetchFS(fetchCtx(t), pi, code, out, make(chan TransferProgress, 8))
	if err == nil {
		t.Fatal("a share outside the sender's shared roots must not fetch over the read path")
	}
	if _, statErr := os.Stat(filepath.Join(out, "private.txt")); !os.IsNotExist(statErr) {
		t.Error("nothing may be written for a share that does not resolve")
	}
}

// An unpaired receiver cannot resolve the code, so the read path is not offered
// at all. This is the case the transfer path exists for.
func TestFetchFromUnpairedReceiverIsNotOffered(t *testing.T) {
	const code = "66666666666666666666666666666666"
	s := newFetchSetup(t, code, "for a paired peer only")

	s.sender.SetReadAccess(nil) // nobody may read
	receiver := fsHost(t)
	pi := peer.AddrInfo{ID: s.sender.Host.ID(), Addrs: s.sender.Host.Addrs()}
	if err := receiver.Host.Connect(fsCtx(t), pi); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	if err := receiver.FetchFS(fetchCtx(t), pi, code, out, make(chan TransferProgress, 8)); err == nil {
		t.Fatal("an unpaired receiver must not be offered the read path")
	}
}

// A fetch picks up an existing partial download and produces a byte-identical
// file. Resume is a feature of the transfer path; dropping it on the read path
// would be a regression the caller never asked for.
//
// The partial is seeded rather than raced, so the test asserts the behaviour
// instead of the scheduler's timing.
func TestFetchOverReadPathResumes(t *testing.T) {
	content := strings.Repeat("resume-me-", 120_000) // ~1.2 MB
	const code = "77777777777777777777777777777777"
	s := newFetchSetup(t, code, content)
	out := t.TempDir()

	// What an interrupted attempt leaves behind: a partial file holding the
	// first third, plus resume state pointing at it.
	const prefix = 400_000
	partial, err := storage.PartialPath(out, code, "gift.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, []byte(content[:prefix]), 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveResume(out, storage.ResumeState{
		Code: code, FileName: "gift.txt", FileSize: int64(len(content)), Offset: prefix,
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.receiver.FetchFS(fetchCtx(t), s.pi, code, out, make(chan TransferProgress, 256)); err != nil {
		t.Fatalf("the resumed fetch should succeed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "gift.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("the resumed file differs from the source: %d bytes vs %d", len(got), len(content))
	}
	// A completed fetch must clear its resume state, or a later fetch of the
	// same code would try to resume a file that is already whole.
	if _, ok, err := storage.LoadResume(out, code); err != nil {
		t.Errorf("resume state should be readable: %v", err)
	} else if ok {
		t.Error("a completed fetch must clear its resume state")
	}
}

// A resume whose recorded file name no longer matches what the code names must
// be discarded, not mixed in. Two different files under one code would
// otherwise produce a spliced file.
func TestFetchOverReadPathDiscardsMismatchedResume(t *testing.T) {
	content := "the current contents of this code"
	const code = "ababababababababababababababababab"
	s := newFetchSetup(t, code, content)
	out := t.TempDir()

	// Resume state for a different file name.
	if err := storage.SaveResume(out, storage.ResumeState{
		Code: code, FileName: "somethingelse.txt", FileSize: 999, Offset: 10,
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.receiver.FetchFS(fetchCtx(t), s.pi, code, out, make(chan TransferProgress, 32)); err != nil {
		t.Fatalf("a mismatched resume must not fail the fetch: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "gift.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("content = %q, want the current share's contents", got)
	}
}

// The sender's hash is authoritative: if what the read path returns does not
// match it, the fetch fails rather than landing a corrupted file.
func TestFetchOverReadPathVerifiesSenderHash(t *testing.T) {
	const code = "88888888888888888888888888888888"
	s := newFetchSetup(t, code, "the original contents")

	// Replace the shared file after the share was registered, so the recorded
	// hash no longer describes what a read would return.
	if err := os.WriteFile(filepath.Join(s.root, "gift.txt"), []byte("tampered!!!!!!"), 0600); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	err := s.receiver.FetchFS(fetchCtx(t), s.pi, code, out, make(chan TransferProgress, 8))
	if err == nil {
		t.Fatal("a fetch whose bytes do not match the sender's hash must fail")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("the failure should name the mismatch: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(out, "gift.txt")); !os.IsNotExist(statErr) {
		t.Error("a hash mismatch must not leave a completed file on disk")
	}
}

func fetchCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}
