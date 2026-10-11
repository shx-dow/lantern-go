package p2p

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/crypto"
	"github.com/shx-dow/lantern-go/internal/protocol"
	"github.com/shx-dow/lantern-go/internal/storage"
)

// A fetch must not silently replace a file the operator already has. The push
// path refuses unless the sender asks for overwrite; the fetch path had no such
// check at all, so a share named notes.md destroyed whatever notes.md was
// already in the output directory.
func TestFetchRefusesToReplaceAnExistingFile(t *testing.T) {
	content := []byte("contents from the peer")
	receiver, pi := lyingReadSender(t, content, digestOf(content))

	out := t.TempDir()
	existing := filepath.Join(out, "file.txt")
	if err := os.WriteFile(existing, []byte("the operator's own file"), 0600); err != nil {
		t.Fatal(err)
	}

	err := fetchInto(t, receiver, pi, "3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a", out)
	if err == nil {
		t.Fatal("a fetch must refuse to replace an existing file")
	}
	if !strings.Contains(err.Error(), "exists") {
		t.Fatalf("the refusal should say the destination exists, got: %v", err)
	}

	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("the existing file must survive: %v", err)
	}
	if string(got) != "the operator's own file" {
		t.Fatalf("existing file was replaced: %q", got)
	}
}

// The same check applies to a directory sitting where the file would land.
func TestFetchRefusesToReplaceAnExistingDirectory(t *testing.T) {
	content := []byte("a file where a directory already is")
	receiver, pi := lyingReadSender(t, content, digestOf(content))

	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "file.txt"), 0700); err != nil {
		t.Fatal(err)
	}

	if err := fetchInto(t, receiver, pi, "4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b", out); err == nil {
		t.Fatal("a fetch must refuse when a directory occupies the destination")
	}
	if fi, err := os.Stat(filepath.Join(out, "file.txt")); err != nil || !fi.IsDir() {
		t.Fatalf("the existing directory must survive, stat err = %v", err)
	}
}

// The happy path still lands when nothing is in the way, so the refusal cannot
// be tightened into breaking every fetch.
func TestFetchLandsWhenDestinationIsFree(t *testing.T) {
	content := []byte("nothing in the way")
	receiver, pi := lyingReadSender(t, content, digestOf(content))

	out := t.TempDir()
	if err := fetchInto(t, receiver, pi, "5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c", out); err != nil {
		t.Fatalf("a fetch into an empty directory must succeed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("contents = %q, want %q", got, content)
	}
}

// The pre-download check is not the one that decides. A fetch takes long enough
// for something to appear at the destination while the content is arriving, so
// the check immediately before the rename is what has to catch it. Without that
// second check this file is silently replaced even though the fetch passed every
// earlier gate.
func TestFetchRechecksDestinationBeforeRenaming(t *testing.T) {
	content := []byte(strings.Repeat("body that arrives slowly ", 200))

	receiver := fsHost(t)
	release := make(chan struct{})
	_, pi := lyingReadSenderInto(t, receiver, content, digestOf(content), bodyMode{release: release})

	out := t.TempDir()
	fetchErr := make(chan error, 1)
	go func() {
		fetchErr <- fetchInto(t, receiver, pi, "6d6d6d6d6d6d6d6d6d6d6d6d6d6d6d6d6d", out)
	}()

	// Wait until the fetch has staged its partial, so the sender is mid-body.
	partial, err := storage.PartialPath(out, "6d6d6d6d6d6d6d6d6d6d6d6d6d6d6d6d6d", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	waitForPath(t, partial)

	// Something turns up at the destination mid-flight.
	late := filepath.Join(out, "file.txt")
	if err := os.WriteFile(late, []byte("written while the fetch was running"), 0600); err != nil {
		t.Fatal(err)
	}
	close(release)

	if err := <-fetchErr; err == nil {
		t.Fatal("the fetch must refuse when the destination is taken before the rename")
	}
	got, err := os.ReadFile(late)
	if err != nil {
		t.Fatalf("the late file must survive: %v", err)
	}
	if string(got) != "written while the fetch was running" {
		t.Fatalf("the late file was replaced: %q", got)
	}
}

// waitForPath blocks until path exists, so a test can act while a transfer is
// genuinely mid-flight rather than guessing at a sleep.
func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

// The share-code route publishes by the same rename and had the same absence of
// a check. A share named notes.md destroyed the operator's notes.md.
func TestShareTransferRefusesToReplaceAnExistingFile(t *testing.T) {
	source := filepath.Join(t.TempDir(), "notes.md")
	content := []byte(strings.Repeat("shared body ", 200))
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}

	code := "7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e"
	ctx := fsCtx(t)
	sender := fsHost(t)
	sender.ctx = ctx
	if err := sender.RegisterShareHandler(fsCtx(t), code, source, make(chan TransferProgress, 64)); err != nil {
		t.Fatal(err)
	}
	pi := peer.AddrInfo{ID: sender.Host.ID(), Addrs: sender.Host.Addrs()}

	receiver := fsHost(t)
	receiver.ctx = ctx
	if err := receiver.Host.Connect(ctx, pi); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	existing := filepath.Join(out, "notes.md")
	if err := os.WriteFile(existing, []byte("the operator's own notes"), 0600); err != nil {
		t.Fatal(err)
	}

	err := receiver.receiveFile(ctx, pi, code, out, make(chan TransferProgress, 64))
	if err == nil {
		t.Fatal("a share-code transfer must refuse to replace an existing file")
	}
	if !strings.Contains(err.Error(), "exists") {
		t.Fatalf("the refusal should say the destination exists, got: %v", err)
	}
	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("the existing file must survive: %v", err)
	}
	if string(got) != "the operator's own notes" {
		t.Fatalf("existing file was replaced: %q", got)
	}
}

// A download takes its permissions from the directory it lands in, the same way
// a push does. It used to hardcode 0644 for the file and 0755 for any directory
// it had to create, so receiving into a 0700 directory produced a world-readable
// file — the exact bug write-path-audit Finding 2 recorded for the push side.
func TestFetchInheritsPermissionsFromDestination(t *testing.T) {
	posixOnly(t)

	content := []byte(strings.Repeat("private body ", 100))
	receiver, pi := lyingReadSender(t, content, digestOf(content))

	out := filepath.Join(t.TempDir(), "inbox")
	if err := os.MkdirAll(out, 0700); err != nil {
		t.Fatal(err)
	}

	if err := fetchInto(t, receiver, pi, "8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f", out); err != nil {
		t.Fatalf("fetch into a private directory must succeed: %v", err)
	}

	fi, err := os.Stat(filepath.Join(out, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("delivered file mode = %04o, want 0600 inherited from a 0700 directory", got)
	}
}

// The same holds for a directory the download has to create: it must not be
// born world-readable under a private parent.
func TestFetchCreatedDirectoryInheritsPermissions(t *testing.T) {
	posixOnly(t)

	content := []byte(strings.Repeat("nested body ", 100))
	receiver, pi := lyingReadSender(t, content, digestOf(content))

	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(parent, "made-by-us")

	if err := fetchInto(t, receiver, pi, "90909090909090909090909090909090", out); err != nil {
		t.Fatalf("fetch into a new directory must succeed: %v", err)
	}

	di, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got&0o077 != 0 {
		t.Errorf("created directory mode = %04o, want no group or other access", got)
	}
	fi, err := os.Stat(filepath.Join(out, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("delivered file mode = %04o, want 0600", got)
	}
}

// A partial left behind by an earlier build — or by a different umask — carries
// whatever mode it was created with, and OpenFile does not change the mode of a
// file that already exists. The delivered file would therefore keep the stale
// mode rather than the destination's, which is what the chmod before the rename
// exists to prevent.
func TestFetchResumedPartialGetsDestinationMode(t *testing.T) {
	posixOnly(t)

	content := []byte(strings.Repeat("resumed body ", 100))
	receiver, pi := lyingReadSender(t, content, digestOf(content))

	out := t.TempDir()
	if err := os.Chmod(out, 0700); err != nil {
		t.Fatal(err)
	}
	code := "ababababababababababababababababab"

	partial, err := storage.PartialPath(out, code, "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, nil, 0666); err != nil {
		t.Fatal(err)
	}
	// umask would mask the mode passed to WriteFile, so set it explicitly.
	if err := os.Chmod(partial, 0666); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveResume(out, storage.ResumeState{
		Code: code, FileName: "file.txt", FileSize: int64(len(content)), Offset: 0,
	}); err != nil {
		t.Fatal(err)
	}

	if err := fetchInto(t, receiver, pi, code, out); err != nil {
		t.Fatalf("resuming a partial must succeed: %v", err)
	}
	fi, err := os.Stat(filepath.Join(out, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("resumed file mode = %04o, want 0600 inherited from a 0700 directory", got)
	}
}

// A cancelled transfer must not leave a goroutine blocked forever on its
// progress channel. The sender used to do a bare `progress <- msg`, so the
// moment the consumer stopped reading — a cancelled context, a closed CLI — the
// transfer goroutine parked on a full channel holding an open file and a libp2p
// stream, for the lifetime of the process.
//
// Counting goroutines is too blunt here: libp2p starts and finishes goroutines
// throughout, so the total moves for unrelated reasons and can mask a parked
// one. This looks at where each goroutine actually is instead — a leaked sender
// is stuck inside serveShare, and nothing else is.
func TestCancelledShareDoesNotLeakItsGoroutine(t *testing.T) {
	source := filepath.Join(t.TempDir(), "big.bin")
	content := make([]byte, 16*crypto.ChunkSize)
	for i := range content {
		content[i] = byte(i % 251)
	}
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}

	code := "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := fsHost(t)
	sender.ctx = ctx
	// Capacity of one, and nobody ever reads it: the second progress report can
	// only complete if the context unblocks the send.
	if err := sender.RegisterShareHandler(ctx, code, source, make(chan TransferProgress, 1)); err != nil {
		t.Fatal(err)
	}

	receiver := fsHost(t)
	receiver.ctx = ctx
	pi := peer.AddrInfo{ID: sender.Host.ID(), Addrs: sender.Host.Addrs()}
	if err := receiver.Host.Connect(ctx, pi); err != nil {
		t.Fatal(err)
	}

	transferCtx, transferCancel := context.WithCancel(ctx)
	progress := make(chan TransferProgress, 1)
	done := make(chan error, 1)
	go func() { done <- receiver.receiveFile(transferCtx, pi, code, t.TempDir(), progress) }()

	time.Sleep(200 * time.Millisecond)
	transferCancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("receiveFile did not return after cancellation")
	}
	cancel() // the sender's share context

	if stack := waitForGoroutineGone("(*Node).serveShare", 10*time.Second); stack != "" {
		t.Fatalf("a transfer goroutine was still parked 10s after cancellation:\n%s", stack)
	}
}

// waitForGoroutineGone polls the runtime's stack dump until no goroutine matches
// frame, and returns the offending stack if one is still there at the deadline.
// Matching on the frame rather than a count is what makes this reliable: a
// goroutine that is genuinely stuck cannot be confused with unrelated churn.
func waitForGoroutineGone(frame string, within time.Duration) string {
	deadline := time.Now().Add(within)
	for {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		if !strings.Contains(string(buf[:n]), frame) {
			return ""
		}
		if time.Now().After(deadline) {
			return stacksContaining(string(buf[:n]), frame)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// stacksContaining returns just the goroutine blocks mentioning frame, so a
// failure shows the parked send rather than the whole dump.
func stacksContaining(dump, frame string) string {
	var out []string
	for _, block := range strings.Split(dump, "\n\n") {
		if strings.Contains(block, frame) {
			out = append(out, block)
		}
	}
	return strings.Join(out, "\n\n")
}

// publishIfAbsent must be atomic: creating the destination has to fail, not
// race. This is the property the check-then-rename sequence could not offer, and
// the reason it was replaced — two downloads with different codes are allowed to
// overlap and can both target the same file name.
func TestPublishIfAbsentIsAtomic(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "staged")
	if err := os.WriteFile(src, []byte("staged content"), 0600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "final")

	// The first publish succeeds and leaves exactly one name for the content.
	if err := publishIfAbsent(src, dst); err != nil {
		t.Fatalf("publishing into an empty directory must succeed: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the staged file should be gone once published")
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "staged content" {
		t.Fatalf("published content = %q, err = %v", got, err)
	}

	// The second must fail rather than replace it.
	src2 := filepath.Join(dir, "staged2")
	if err := os.WriteFile(src2, []byte("a different file"), 0600); err != nil {
		t.Fatal(err)
	}
	err = publishIfAbsent(src2, dst)
	if err == nil {
		t.Fatal("publishing over an existing destination must fail")
	}
	if !strings.Contains(err.Error(), "exists") {
		t.Errorf("the refusal should say the destination exists, got: %v", err)
	}
	got, err = os.ReadFile(dst)
	if err != nil || string(got) != "staged content" {
		t.Fatalf("the existing file was replaced: %q (err %v)", got, err)
	}
}

// Two downloads with different codes are allowed to overlap by design — the
// per-download claim is keyed on the code — so when both peers offer a file with
// the same name into one directory, exactly one may land it.
//
// This documents the invariant and would catch a regression that made the
// collision deterministic. It is not proof that the race is closed: the window
// between "check" and "rename" is a few microseconds, and reverting the atomic
// publish to a check-then-rename did not make this test fail across five runs.
// TestPublishIfAbsentIsAtomic is the deterministic half. The guarantee comes from
// link(2) being atomic, not from a timing argument.
func TestConcurrentDifferentCodesCannotBothLandTheSameName(t *testing.T) {
	first := []byte("the first peer's content")
	second := []byte("the second peer's content!!!") // same length, different bytes

	receiver := fsHost(t)
	_, piA := lyingReadSenderInto(t, receiver, first, digestOf(first), bodyMode{})
	_, piB := lyingReadSenderInto(t, receiver, second, digestOf(second), bodyMode{})

	out := t.TempDir()
	results := make([]chan error, 2)
	for i := range results {
		results[i] = make(chan error, 1)
	}
	start := make(chan struct{})
	for i, pi := range []peer.AddrInfo{piA, piB} {
		go func(i int, pi peer.AddrInfo) {
			<-start
			code := string(rune('A'+i)) + strings.Repeat("c", 31)
			results[i] <- receiver.FetchFS(fetchCtx(t), pi, code, out, make(chan TransferProgress, 64))
		}(i, pi)
	}
	close(start)

	var errs [2]error
	for i := range results {
		select {
		case errs[i] = <-results[i]:
		case <-time.After(20 * time.Second):
			t.Fatalf("download %d never finished", i)
		}
	}

	landed := 0
	for _, err := range errs {
		if err == nil {
			landed++
		}
	}
	if landed != 1 {
		t.Errorf("%d downloads reported success, want exactly 1 (errors: %v, %v)", landed, errs[0], errs[1])
	}

	// Whoever won, the file is one of the two in full — never a blend.
	got, err := os.ReadFile(filepath.Join(out, "file.txt"))
	if err != nil {
		t.Fatalf("no file landed: %v", err)
	}
	if string(got) != string(first) && string(got) != string(second) {
		t.Fatalf("landed content is neither peer's: %q", got)
	}
}

// gatedShareSender serves the share-code protocol by hand so it can stall
// mid-body: it sends the header, half the content, and then waits. That is what
// lets a destination file appear while the transfer is genuinely in flight.
//
// A registered share cannot do this — serveShare streams to completion as soon
// as it has a receiver — so the handler is written out here.
func gatedShareSender(t *testing.T, code, name string, content []byte, release <-chan struct{}) peer.AddrInfo {
	t.Helper()
	sender := fsHost(t)
	sender.ctx = fsCtx(t)
	sender.Host.SetStreamHandler(ProtocolID, func(s network.Stream) {
		defer s.Close()
		var req protocol.TransferRequest
		if err := json.NewDecoder(s).Decode(&req); err != nil {
			return
		}
		key, err := crypto.DeriveKey(code)
		if err != nil {
			return
		}
		ew, err := crypto.NewEncryptedWriter(s, key)
		if err != nil {
			return
		}
		if err := protocol.WriteMetadata(ew, FileMeta{Name: name, Size: int64(len(content)), Hash: digestOf(content)}); err != nil {
			return
		}
		if err := ew.Close(); err != nil {
			return
		}
		half := len(content) / 2
		if _, err := ew.Write(content[:half]); err != nil {
			return
		}
		<-release
		_, _ = ew.Write(content[half:])
		if err := ew.Close(); err != nil {
			return
		}
	})
	pi := peer.AddrInfo{ID: sender.Host.ID(), Addrs: sender.Host.Addrs()}
	receiver := fsHost(t)
	receiver.ctx = fsCtx(t)
	if err := receiver.Host.Connect(fsCtx(t), pi); err != nil {
		t.Fatal(err)
	}
	return pi
}

// receiveFile has its own copy of the publish, so it needs its own version of
// the late-arrival case. Removing the check from the share route alone would
// leave every FetchFS test green.
func TestShareTransferDoesNotReplaceALateArrivingFile(t *testing.T) {
	content := []byte(strings.Repeat("late arrival body ", 400))
	code := "efefefefefefefefefefefefefefefefefef"

	ctx := fsCtx(t)
	release := make(chan struct{})
	pi := gatedShareSender(t, code, "notes.md", content, release)

	receiver := fsHost(t)
	receiver.ctx = ctx
	out := t.TempDir()

	errc := make(chan error, 1)
	go func() { errc <- receiver.receiveFile(ctx, pi, code, out, make(chan TransferProgress, 64)) }()

	// Let it get far enough to stage a partial, then occupy the destination.
	partial, err := storage.PartialPath(out, code, "notes.md")
	if err != nil {
		t.Fatal(err)
	}
	waitForPath(t, partial)

	late := filepath.Join(out, "notes.md")
	if err := os.WriteFile(late, []byte("written mid-transfer"), 0600); err != nil {
		t.Fatal(err)
	}
	close(release)

	if err := <-errc; err == nil {
		t.Fatal("the share transfer must refuse when the destination is taken before publishing")
	}
	got, err := os.ReadFile(late)
	if err != nil {
		t.Fatalf("the late file must survive: %v", err)
	}
	if string(got) != "written mid-transfer" {
		t.Fatalf("the late file was replaced: %q", got)
	}
}

// The share route chmods its staged file before publishing, and a partial left by
// a previous attempt carries whatever mode it had. Without the chmod the
// delivered file keeps that stale mode.
func TestShareTransferResumedPartialGetsDestinationMode(t *testing.T) {
	posixOnly(t)

	content := []byte(strings.Repeat("resumed share body ", 100))
	source := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}

	code := "abababababababababababababababab"
	pi := shareSender(t, source, code, digestOf(content))

	receiver := fsHost(t)
	receiver.ctx = fsCtx(t)

	out := t.TempDir()
	if err := os.Chmod(out, 0700); err != nil {
		t.Fatal(err)
	}
	partial, err := storage.PartialPath(out, code, "payload.bin")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, nil, 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(partial, 0666); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveResume(out, storage.ResumeState{
		Code: code, FileName: "payload.bin", FileSize: int64(len(content)), Offset: 0,
	}); err != nil {
		t.Fatal(err)
	}

	if err := receiver.receiveFile(fsCtx(t), pi, code, out, make(chan TransferProgress, 64)); err != nil {
		t.Fatalf("resuming a partial must succeed: %v", err)
	}
	fi, err := os.Stat(filepath.Join(out, "payload.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("resumed share file mode = %04o, want 0600 inherited from a 0700 directory", got)
	}
}
