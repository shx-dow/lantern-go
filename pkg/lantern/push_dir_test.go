package lantern

// End-to-end test for pushing a directory between two real nodes.
//
// The protocol-level tests in internal/p2p cover the receiving half. This one
// drives the whole path a caller uses — zip a directory on the sender, send it
// over real libp2p, expand it on the receiver — because the gap it fills is
// exactly the seam between those halves: the daemon layer that decides a
// directory is archivable and what digest gets verified.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/p2p"
	"github.com/shx-dow/lantern-go/internal/storage"
)

type dirPair struct {
	sender    *Lantern
	receiver  *Lantern
	writeRoot string
	ref       string
}

// newDirPair builds two nodes on real libp2p hosts. The receiver allows writes
// into writeRoot from any peer, which is the operator granting that standing.
func newDirPair(t *testing.T) dirPair {
	t.Helper()
	dir := t.TempDir()
	writeRoot := filepath.Join(dir, "inbox")
	if err := os.MkdirAll(writeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	sender, err := New(Config{DataDir: filepath.Join(dir, "sender")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sender.Close() })
	receiver, err := New(Config{DataDir: filepath.Join(dir, "receiver")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { receiver.Close() })

	// A peer has to be paired for reads before it can be reached for a write
	// at all, so grant that too. It is the ordinary state of a paired device.
	receiver.Node().SetReadAccess(func(string) []string { return []string{writeRoot} })
	receiver.Node().SetWriteAccess(func(string) (*p2p.WritePolicy, error) {
		return &p2p.WritePolicy{Mode: p2p.WriteSharedRoots, Roots: []string{writeRoot}, MaxBytes: 1 << 20}, nil
	})
	receiver.Node().RegisterFSHandler()

	// Connect the two so the sender resolves the receiver by peer ID the way a
	// caller names it, rather than being handed addresses behind resolvePeer's
	// back. Pairing is what puts these addresses in a peerstore in real use.
	receiverInfo := peer.AddrInfo{ID: receiver.Node().Host.ID(), Addrs: receiver.Node().Host.Addrs()}
	connCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := sender.Node().Host.Connect(connCtx, receiverInfo); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return dirPair{
		sender: sender, receiver: receiver, writeRoot: writeRoot,
		ref: receiver.Node().Host.ID().String(),
	}
}

// sourceTree builds a directory with nested content and returns its path.
func sourceTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"readme.txt":        "hello",
		"docs/notes.md":     "notes",
		"docs/img/logo.png": "png-bytes",
	} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// pushDir archives and sends src the way the daemon's pushDir does, and returns
// the result. This is the caller-facing shape, kept here so a change to either
// half that breaks the seam shows up as one failure.
func pushDir(t *testing.T, p dirPair, src, dest string, overwrite bool) PushResult {
	t.Helper()
	zipPath, cleanup, err := storage.ZipDirToTemp(src)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	defer cleanup()
	archive, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.sender.PushDirRemote(context.Background(), p.ref, dest, archive, overwrite)
	if err != nil {
		t.Fatalf("push directory: %v", err)
	}
	return res
}

// A directory pushed between two real nodes arrives as a directory, with its
// nesting intact and the archive nowhere to be seen.
func TestPushDirEndToEnd(t *testing.T) {
	p := newDirPair(t)
	src := sourceTree(t)
	dest := filepath.Join(p.writeRoot, "project")

	res := pushDir(t, p, src, dest, false)

	if res.SHA256 == "" {
		t.Error("a push must report the digest of what was stored")
	}
	for name, want := range map[string]string{
		"readme.txt":        "hello",
		"docs/notes.md":     "notes",
		"docs/img/logo.png": "png-bytes",
	} {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s did not land: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(dest + ".zip"); !os.IsNotExist(err) {
		t.Error("the archive was left behind instead of being expanded")
	}
	if len(res.Entries) == 0 {
		t.Error("a directory push should report the entries it created")
	}
	// The reported entries are the receiver's own view of what it created.
	names := map[string]bool{}
	for _, e := range res.Entries {
		names[e.Name] = true
	}
	if !names["readme.txt"] || !names["docs/notes.md"] {
		t.Errorf("the reported tree is missing files: %v", names)
	}
}

// The receiving device's own roots decide where a tree lands. A destination
// outside them is refused, and the failure leaves nothing behind on either
// side of the boundary.
func TestPushDirEndToEndRefusesOutsideRoots(t *testing.T) {
	p := newDirPair(t)
	src := sourceTree(t)
	outside := t.TempDir()

	zipPath, cleanup, err := storage.ZipDirToTemp(src)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	archive, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(outside, "planted")
	if _, err := p.sender.PushDirRemote(context.Background(), p.ref, dest, archive, false); err == nil {
		t.Fatal("a destination outside the receiver's roots must be refused")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("a refused push created a tree outside the roots")
	}
}

// Pushing the same directory twice is refused the second time, because the
// first one created the destination. This is the property that stops a repeat
// from quietly changing what a peer is holding.
func TestPushDirEndToEndRefusesExistingWithoutOverwrite(t *testing.T) {
	p := newDirPair(t)
	src := sourceTree(t)
	dest := filepath.Join(p.writeRoot, "twice")

	pushDir(t, p, src, dest, false)

	zipPath, cleanup, err := storage.ZipDirToTemp(src)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	archive, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.sender.PushDirRemote(context.Background(), p.ref, dest, archive, false); err == nil {
		t.Fatal("a second push without overwrite must be refused")
	}
	// And with overwrite it lands, replacing rather than merging.
	res, err := p.sender.PushDirRemote(context.Background(), p.ref, dest, archive, true)
	if err != nil {
		t.Fatalf("with overwrite set: %v", err)
	}
	if len(res.Entries) == 0 {
		t.Error("the replacing push reported no entries")
	}
}
