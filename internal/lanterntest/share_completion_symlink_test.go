package lanterntest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The macOS failure, reproduced on Linux.
//
// CI reported this test's sibling failing on macOS and Windows while passing on
// Linux. The cause was string comparison in the read-path correlation: the
// share holds the path the caller passed to ShareSession, while the read side
// has been through resolveWithinRoots, which calls EvalSymlinks. macOS temp
// directories live under /var, a symlink to /private/var, so the two sides
// differed by a symlink and matched nothing. Linux agrees, which is how that
// passed there.
//
// Sharing through a symlinked root is the shape of that failure, so it is
// pinned here rather than left to a platform's temp directory layout.
func TestASymlinkedShareRootStillReportsCompletion(t *testing.T) {
	real := t.TempDir()
	const name = "notes.txt"
	writeShared(t, real, name, "through a symlink")

	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("this platform will not make a symlink here: %v", err)
	}

	p := NewPair(t, link)
	out := t.TempDir()

	session, share, err := p.Sender.Lantern.ShareSession(Ctx(t), link+"/"+name)
	if err != nil {
		t.Fatalf("the sender could not share the file: %v", err)
	}
	defer session.Close()
	p.BridgeDiscovery(t, share.Code)

	received, _, err := p.Receiver.Lantern.ReceiveSession(Ctx(t), share.Code, out)
	if err != nil {
		t.Fatalf("the receiver could not redeem the code: %v", err)
	}
	for range received.Events() {
	}
	received.Close()

	deadline := time.After(15 * time.Second)
	for {
		for _, e := range pending(session.Events()) {
			if e.Type == 4 {
				t.Fatalf("the sender session reported an error: %v", e.Err)
			}
			if e.Type == 1 {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("the sender never learned a symlinked-root share was fetched")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
