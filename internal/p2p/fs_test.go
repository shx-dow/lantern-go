package p2p

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

// fsPair brings up a provider serving root and a requester, trusting it.
// fsHost starts a bare libp2p host for fs tests. NewNode also brings up
// mDNS, a DHT, QUIC, and hole punching, none of which the fs protocol uses;
// paying for that once per test made this package slow enough to time out
// unrelated tests on a two-core CI runner.
func fsHost(t *testing.T) *Node {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return &Node{Host: h}
}

// fsPair returns a requester and the provider it may talk to, with the
// provider serving root and trusting the requester.
func fsPair(t *testing.T, root string) (*Node, peer.AddrInfo) {
	t.Helper()
	return fsPairWith(t, root, nil)
}

// fsPairWith is fsPair with an optional write policy for the provider.
func fsPairWith(t *testing.T, root string, pol *WritePolicy) (*Node, peer.AddrInfo) {
	t.Helper()
	provider := fsHost(t)
	requester := fsHost(t)

	provider.SetReadAccess(allowRootsFor([]string{root}, requester.Host.ID().String()))
	provider.SetWriteAccess(grantEveryone(pol))
	provider.RegisterFSHandler()
	return requester, peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func fsCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestFSStat(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.md"), "hello")
	requester, pi := fsPair(t, root)

	_, entry, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpStat, Path: filepath.Join(root, "notes.md")})
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.Size != 5 || entry.IsDir {
		t.Fatalf("bad stat: %+v", entry)
	}
}

func TestFSReadWholeFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "hello world")
	requester, pi := fsPair(t, root)

	_, _, data, eof, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: filepath.Join(root, "a.txt")})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Fatalf("got %q", data)
	}
	if !eof {
		t.Fatal("expected eof on a short file")
	}
}

func TestFSReadRange(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "0123456789")
	requester, pi := fsPair(t, root)

	_, entry, data, eof, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: filepath.Join(root, "a.txt"), Offset: 3, Length: 4})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "3456" {
		t.Fatalf("got %q, want 3456", data)
	}
	if eof {
		t.Fatal("did not expect eof mid-file")
	}
	if entry == nil || entry.Size != 10 {
		t.Fatalf("header must report true file size, got %+v", entry)
	}
}

func TestFSReadPastEndIsEmptyNotError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "abc")
	requester, pi := fsPair(t, root)

	_, _, data, eof, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: filepath.Join(root, "a.txt"), Offset: 99})
	if err != nil {
		t.Fatalf("reading past end should not error: %v", err)
	}
	if len(data) != 0 || !eof {
		t.Fatalf("want empty+EOF, got %q eof=%v", data, eof)
	}
}

func TestFSReadRejectsLengthOverCap(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "abc")
	requester, pi := fsPair(t, root)

	_, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: filepath.Join(root, "a.txt"), Length: MaxReadLength + 1})
	if err == nil {
		t.Fatal("expected rejection above the read cap")
	}
}

func TestFSList(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "1")
	requester, pi := fsPair(t, root)

	entries, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpList, Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("bad listing: %+v", entries)
	}
}

func TestFSRejectsBreakout(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	writeFile(t, secret, "classified")
	requester, pi := fsPair(t, root)

	for _, path := range []string{secret, "/etc/passwd", filepath.Join(root, "..", "..", "etc", "passwd")} {
		if _, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: path}); err == nil {
			t.Fatalf("expected breakout rejection for %q", path)
		}
	}
}

func TestFSRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	writeFile(t, secret, "classified")
	if err := os.Symlink(secret, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPair(t, root)

	if _, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: filepath.Join(root, "link.txt")}); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestFSDeniedWhenUnpaired(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "hi")
	provider := fsHost(t)
	provider.SetReadAccess(nil)
	provider.RegisterFSHandler()
	requester := fsHost(t)

	pi := peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
	if _, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: filepath.Join(root, "a.txt")}); err == nil {
		t.Fatal("expected pairing denial")
	}
}

func TestFSRejectsDirectoryRead(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPair(t, root)

	if _, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: root}); err == nil {
		t.Fatal("expected directory read to be refused")
	}
}

func TestFSRejectsUnknownOp(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPair(t, root)

	if _, _, _, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: "delete", Path: root}); err == nil {
		t.Fatal("expected unknown op to be refused")
	}
}

func TestFSReadSpansChunkBoundary(t *testing.T) {
	root := t.TempDir()
	// Larger than the metadata frame and the default read length, so the
	// content path is exercised across more than one write.
	big := bytes.Repeat([]byte("abcdefgh"), 64*1024)
	if err := os.WriteFile(filepath.Join(root, "big.bin"), big, 0600); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPair(t, root)

	_, _, data, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: filepath.Join(root, "big.bin")})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, big[:DefaultReadLength]) {
		t.Fatalf("large read mismatch: got %d bytes", len(data))
	}
}

// allowAllRoots is the test stand-in for "every peer may read these roots".
func allowAllRoots(roots ...string) ReadRoots {
	return func(string) []string { return roots }
}

// allowRootsFor allows only the listed peers, mirroring the old bool gate.
func allowRootsFor(roots []string, allowed ...string) ReadRoots {
	set := map[string]bool{}
	for _, a := range allowed {
		set[a] = true
	}
	return func(id string) []string {
		if set[id] {
			return roots
		}
		return nil
	}
}
