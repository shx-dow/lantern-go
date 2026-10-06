package lantern

// Tests for which path a fetch takes.
//
// Both paths deliver the same file, so a passing download proves nothing about
// which one ran. These drive the routing decision directly and assert it: when
// a sender will serve the code over the read path the fs protocol carries the
// fetch, and when it will not, the caller is told to fall back rather than
// half-committing to a path that cannot finish.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/p2p"
)

type routePair struct {
	sender   *Lantern
	receiver *Lantern
	root     string
	out      string
	addr     peer.AddrInfo
}

func newRoutePair(t *testing.T, content string) routePair {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "share")
	if err := os.MkdirAll(root, 0o755); err != nil {
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

	if err := os.WriteFile(filepath.Join(root, "gift.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	node := sender.Node()
	if node == nil || node.Host == nil {
		t.Fatal("the sender has no node")
	}
	return routePair{
		sender: sender, receiver: receiver, root: root,
		out:  filepath.Join(dir, "out"),
		addr: peer.AddrInfo{ID: node.Host.ID(), Addrs: node.Host.Addrs()},
	}
}

// grantReadTo lets any peer read the shared root on the sender.
func (p routePair) grantReadTo() {
	p.sender.Node().SetReadAccess(func(string) []string { return []string{p.root} })
}

// share advertises path and returns the code the sender minted for it.
func (p routePair) share(t *testing.T, path string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	session, _, err := p.sender.ShareSession(ctx, path)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	return session.ID(), func() { session.Close() }
}

func routeCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// A sender that grants read access to the directory holding the shared file
// must be fetched over the read path, and the transfer protocol never opens.
func TestFetchFromReadableSenderUsesReadPath(t *testing.T) {
	const content = "delivered without the transfer protocol"
	p := newRoutePair(t, content)
	p.grantReadTo()

	code, cleanup := p.share(t, filepath.Join(p.root, "gift.txt"))
	defer cleanup()

	progress := make(chan p2p.TransferProgress, 64)
	if err := p.receiver.tryFetchViaRead(routeCtx(t), p.addr, code, p.out, progress); err != nil {
		t.Fatalf("a readable sender must be fetched over the read path: %v", err)
	}
	if p.receiver.readPathFetches != 1 {
		t.Fatalf("the read path was used %d times, want 1", p.receiver.readPathFetches)
	}
	got, err := os.ReadFile(filepath.Join(p.out, "gift.txt"))
	if err != nil {
		t.Fatalf("the file did not land: %v", err)
	}
	if string(got) != content {
		t.Fatalf("content = %q", got)
	}
}

// A sender that does not grant read access must not be offered the read path:
// the caller falls back to the transfer protocol, which is the whole point of
// keeping it.
func TestFetchFromUnreadableSenderFallsBack(t *testing.T) {
	p := newRoutePair(t, "delivered by the transfer protocol")
	// No read access granted.

	code, cleanup := p.share(t, filepath.Join(p.root, "gift.txt"))
	defer cleanup()

	err := p.receiver.tryFetchViaRead(routeCtx(t), p.addr, code, p.out, make(chan p2p.TransferProgress, 8))
	if err == nil {
		t.Fatal("a sender that grants no read access must not be fetched over the read path")
	}
	if p.receiver.readPathFetches != 0 {
		t.Fatalf("the read path was used %d times, want 0", p.receiver.readPathFetches)
	}
	if _, statErr := os.Stat(filepath.Join(p.out, "gift.txt")); !os.IsNotExist(statErr) {
		t.Error("nothing may be written when the read path was not taken")
	}
}

// The common real case: the sender does grant read access, but the shared file
// is outside the granted root, so the read path cannot serve it.
func TestFetchOutsideSharedDirFallsBack(t *testing.T) {
	p := newRoutePair(t, "outside the shared dir")
	p.grantReadTo()

	elsewhere := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(elsewhere, []byte("outside the shared dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, cleanup := p.share(t, elsewhere)
	defer cleanup()

	if err := p.receiver.tryFetchViaRead(routeCtx(t), p.addr, code, p.out, make(chan p2p.TransferProgress, 8)); err == nil {
		t.Fatal("a file outside the sender's shared roots must not resolve over the read path")
	}
	if p.receiver.readPathFetches != 0 {
		t.Fatalf("the read path was used %d times, want 0", p.receiver.readPathFetches)
	}
}

// A code the sender never advertised must not resolve, so a fetch falls back
// rather than resolving to nothing.
func TestFetchOfUnknownCodeFallsBack(t *testing.T) {
	p := newRoutePair(t, "content")
	p.grantReadTo()

	if err := p.receiver.tryFetchViaRead(
		routeCtx(t), p.addr, "ffffffffffffffffffffffffffffffff",
		p.out, make(chan p2p.TransferProgress, 8),
	); err == nil {
		t.Fatal("a code nobody advertised must not fetch over the read path")
	}
	if p.receiver.readPathFetches != 0 {
		t.Fatalf("the read path was used %d times, want 0", p.receiver.readPathFetches)
	}
}

// Cancelling a share does not make its code resolve to a path the read gate
// would refuse. A share of a file outside the shared roots stays unresolvable,
// so cancelling changes nothing an unpaired or ungranted peer could exploit —
// and a share of a file inside the shared roots was already readable by any
// paired peer, codes aside.
func TestCancelledShareStillRespectsTheReadGate(t *testing.T) {
	p := newRoutePair(t, "outside the shared dir")
	p.grantReadTo()

	elsewhere := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(elsewhere, []byte("outside the shared dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, cleanup := p.share(t, elsewhere)
	cleanup() // cancel the share

	if err := p.receiver.tryFetchViaRead(
		routeCtx(t), p.addr, code, p.out, make(chan p2p.TransferProgress, 8),
	); err == nil {
		t.Fatal("a cancelled share of a file outside the shared roots must still not resolve")
	}
}

// Once the read path is taken, a failure inside it is a real failure and must
// not be mistaken for "try the transfer path instead". Half-committing would
// leave a partial file and a duplicate advertisement behind.
func TestReadPathFailureIsNotSilentlyRetried(t *testing.T) {
	p := newRoutePair(t, "the original")
	p.grantReadTo()

	code, cleanup := p.share(t, filepath.Join(p.root, "gift.txt"))
	defer cleanup()

	// Change the file after the share was registered, so what a read returns
	// no longer matches the hash the sender recorded.
	if err := os.WriteFile(filepath.Join(p.root, "gift.txt"), []byte("tampered!!!!"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := p.receiver.tryFetchViaRead(routeCtx(t), p.addr, code, p.out, make(chan p2p.TransferProgress, 8))
	if err == nil {
		t.Fatal("a hash mismatch inside the read path must be reported")
	}
	if !strings.Contains(err.Error(), "read-path fetch") {
		t.Errorf("the failure should be attributed to the read path, not swallowed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(p.out, "gift.txt")); !os.IsNotExist(statErr) {
		t.Error("a failed read-path fetch must not leave a completed file")
	}
}
