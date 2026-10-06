package p2p

// Tests for resolving a share code onto the read path.
//
// The claim under test: a paired peer will tell a peer it trusts which local
// path a code names, but only when that path is inside the roots that peer is
// allowed to read. Everything else must come back empty, because an empty
// answer is the receiver's cue to use the transfer path instead.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func boolp(b bool) *bool { return &b }

// registerShare advertises path under code and returns the code's state.
func registerShare(t *testing.T, n *Node, code, path string) {
	t.Helper()
	progress := make(chan TransferProgress, 8)
	if err := n.RegisterShareHandler(code, path, progress); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		go func() {
			for range progress {
			}
		}()
		n.ClearLocal(code)
		n.forgetShare(code)
	})
}

// A paired peer resolving a code inside its shared roots gets the path and the
// sender's hash for it.
func TestResolveShareReturnsPathInsideRoots(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "shared.txt")
	if err := os.WriteFile(path, []byte("shared bytes"), 0600); err != nil {
		t.Fatal(err)
	}

	sender := fsHost(t)
	sender.SetReadAccess(allowAllRoots(root))
	sender.RegisterFSHandler()
	receiver := fsHost(t)
	if err := receiver.Host.Connect(fsCtx(t), peerAddr(sender)); err != nil {
		t.Fatal(err)
	}

	const code = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	registerShare(t, sender, code, path)

	info, err := receiver.ResolveShare(fsCtx(t), peerAddr(sender), code)
	if err != nil {
		t.Fatalf("resolving a share inside the roots must succeed: %v", err)
	}
	if info == nil {
		t.Fatal("a share inside the roots must resolve")
	}
	if info.Name != "shared.txt" {
		t.Errorf("name = %q, want shared.txt", info.Name)
	}
	if info.SHA256 == "" {
		t.Error("the resolve must carry the sender's hash, or the receiver cannot verify what it reads")
	}
	// The returned path must be one the same peer can actually read, or the
	// receiver resolves a code and then fails on the read that follows.
	_, _, data, _, readErr := receiver.ReadFS(fsCtx(t), peerAddr(sender), FSRequest{Op: OpRead, Path: info.Path})
	if readErr != nil {
		t.Errorf("the resolved path must be readable by the same peer: %v", readErr)
	}
	if string(data) != "shared bytes" {
		t.Errorf("the read returned %q, want the shared file's contents", data)
	}
}

// A share outside the shared roots must not resolve, and must not disclose the
// path. Otherwise a paired peer learns the location of files it has no right to
// read, and which of its guessed codes are live.
func TestResolveShareRefusesPathOutsideRoots(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}

	sender := fsHost(t)
	sender.SetReadAccess(allowAllRoots(root))
	sender.RegisterFSHandler()
	receiver := fsHost(t)

	const code = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	registerShare(t, sender, code, outside)

	info, err := receiver.ResolveShare(fsCtx(t), peerAddr(sender), code)
	if err != nil {
		t.Fatalf("a share outside the roots is not an error, it is unresolvable: %v", err)
	}
	if info != nil {
		t.Fatalf("a path outside the roots must not resolve, got %+v", info)
	}
}

// An unpaired peer gets nothing. The read gate has to apply to resolve as well
// as to read, or pairing would stop being the thing that grants access.
func TestResolveShareRefusesUnpairedPeer(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "shared.txt")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	sender := fsHost(t)
	sender.SetReadAccess(nil) // nobody may read
	sender.RegisterFSHandler()
	receiver := fsHost(t)

	const code = "cccccccccccccccccccccccccccccccc"
	registerShare(t, sender, code, path)

	info, err := receiver.ResolveShare(fsCtx(t), peerAddr(sender), code)
	if err == nil && info != nil {
		t.Fatal("a peer with no read access must not resolve a code")
	}
}

// A peer paired but granted no read access (tier none) gets nothing either.
func TestResolveShareRefusesPeerWithoutReadAccess(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "shared.txt")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	sender := fsHost(t)
	// Read access exists, but only for a different peer.
	sender.SetReadAccess(allowRootsFor([]string{root}, "someone-else"))
	sender.RegisterFSHandler()
	receiver := fsHost(t)

	const code = "dddddddddddddddddddddddddddddddd"
	registerShare(t, sender, code, path)

	info, err := receiver.ResolveShare(fsCtx(t), peerAddr(sender), code)
	if err == nil && info != nil {
		t.Fatal("a peer without read access must not resolve a code")
	}
}

// A code nobody is advertising does not resolve. This is the normal case that
// sends a fetch down the transfer path.
func TestResolveShareOfUnknownCodeIsEmpty(t *testing.T) {
	root := t.TempDir()
	sender := fsHost(t)
	sender.SetReadAccess(allowAllRoots(root))
	sender.RegisterFSHandler()
	receiver := fsHost(t)

	info, err := receiver.ResolveShare(fsCtx(t), peerAddr(sender), "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if err != nil {
		t.Fatalf("an unknown code is not an error: %v", err)
	}
	if info != nil {
		t.Fatalf("an unknown code must not resolve, got %+v", info)
	}
}

// A peer's own symlink pointing out of the roots must not make the share
// resolvable, or the read that follows would be the escape.
func TestResolveShareRefusesSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	sender := fsHost(t)
	sender.SetReadAccess(allowAllRoots(root))
	sender.RegisterFSHandler()
	receiver := fsHost(t)

	const code = "ffffffffffffffffffffffffffffffff"
	registerShare(t, sender, code, link)

	info, err := receiver.ResolveShare(fsCtx(t), peerAddr(sender), code)
	if err != nil {
		t.Fatalf("a symlink out of the roots is unresolvable, not an error: %v", err)
	}
	if info != nil {
		t.Fatalf("a share reached through a symlink must not resolve, got %+v", info)
	}
}

// A directory is never resolvable: directories move as an archive on the
// transfer path, which is a separate concern.
func TestResolveShareRefusesDirectory(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	sender := fsHost(t)
	sender.SetReadAccess(allowAllRoots(root))
	sender.RegisterFSHandler()
	receiver := fsHost(t)

	const code = "99999999999999999999999999999999"
	if err := sender.RegisterShareHandler(code, sub, make(chan TransferProgress, 4)); err == nil {
		t.Fatal("registering a directory as a share must fail at the source")
	}

	info, err := receiver.ResolveShare(fsCtx(t), peerAddr(sender), code)
	if err != nil || info != nil {
		t.Fatalf("a directory must not resolve: %+v %v", info, err)
	}
}

// peerAddr is the discovered form of a node.
func peerAddr(n *Node) peer.AddrInfo {
	return peer.AddrInfo{ID: n.Host.ID(), Addrs: n.Host.Addrs()}
}
