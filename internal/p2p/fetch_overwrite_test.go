package p2p

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/crypto"
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
