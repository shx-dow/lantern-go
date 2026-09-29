package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/protocol"
)

func writePolicy(root string, max int64) *WritePolicy {
	return &WritePolicy{Mode: WriteSharedRoots, Roots: []string{root}, MaxBytes: max}
}

func TestFSWriteStoresFile(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	content := []byte("pushed from another device\n")
	dst := filepath.Join(root, "arrived.txt")
	entry, sum, err := requester.WriteFS(fsCtx(t), pi, dst, content, false)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Size != int64(len(content)) {
		t.Fatalf("entry size %d, want %d", entry.Size, len(content))
	}
	want := sha256.Sum256(content)
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("digest %q, want %q", sum, hex.EncodeToString(want[:]))
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("content %q, want %q", got, content)
	}
}

// Writes must be refused unless a policy explicitly enables them, even
// though the requester is trusted to read.
func TestFSWriteDeniedByDefault(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, nil)

	dst := filepath.Join(root, "sneaky.txt")
	_, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("nope"), false)
	if err == nil {
		t.Fatal("expected write refusal when no policy is configured")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatal("refused write must not create a file")
	}
}

func TestFSWriteDeniedWhenUnpaired(t *testing.T) {
	root := t.TempDir()
	provider, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })
	// Paired with nobody, and writes enabled: the pairing gate must still
	// win, because SetListAccess is what grants access at all.
	provider.SetListAccess([]string{root}, func(string) bool { return false })
	provider.SetWritePolicy(writePolicy(root, 0))
	provider.RegisterFSHandler()

	requester, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requester.Close() })

	pi := peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
	dst := filepath.Join(root, "sneaky.txt")
	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("nope"), false); err == nil {
		t.Fatal("expected pairing denial")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatal("denied write must not create a file")
	}
}

func TestFSWriteRejectsOutsideRoots(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	for _, dst := range []string{
		filepath.Join(outside, "escaped.txt"),
		"/tmp/lantern-should-not-exist.txt",
	} {
		if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("nope"), false); err == nil {
			t.Fatalf("expected refusal for %q", dst)
		}
		if _, err := os.Stat(dst); !os.IsNotExist(err) {
			t.Fatalf("refused write created %q", dst)
		}
	}
}

func TestFSWriteRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "precious.txt")
	if err := os.WriteFile(outside, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	if _, _, err := requester.WriteFS(fsCtx(t), pi, link, []byte("overwritten"), true); err == nil {
		t.Fatal("expected refusal to write through a symlink")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("symlink target was modified: %q", got)
	}
}

func TestFSWriteRefusesOverwriteUnlessAsked(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(dst, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("second"), false); err == nil {
		t.Fatal("expected overwrite refusal")
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "first" {
		t.Fatalf("file changed despite refusal: %q", got)
	}

	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("second"), true); err != nil {
		t.Fatalf("explicit overwrite should succeed: %v", err)
	}
	got, _ = os.ReadFile(dst)
	if string(got) != "second" {
		t.Fatalf("overwrite did not land: %q", got)
	}
}

func TestFSWriteRejectsOversizePayload(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 4))

	dst := filepath.Join(root, "big.txt")
	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("far too long"), false); err == nil {
		t.Fatal("expected refusal above the policy cap")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("oversize refusal must not create a file")
	}
}

func TestFSWriteRejectsDirectoryTarget(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "subdir")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	if _, _, err := requester.WriteFS(fsCtx(t), pi, sub, []byte("x"), true); err == nil {
		t.Fatal("expected refusal to write over a directory")
	}
}

func TestFSWriteCreatesMissingParents(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	dst := filepath.Join(root, "nested", "deeper", "file.txt")
	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("deep"), false); err != nil {
		t.Fatalf("expected parents to be created: %v", err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "deep" {
		t.Fatalf("nested write failed: %q %v", got, err)
	}
}

// writeTruncated speaks the write protocol by hand so it can lie about
// ContentLen and hang up mid-body, which the WriteFS client cannot do.
func writeTruncated(ctx context.Context, from *Node, pi peer.AddrInfo, path string, claim int64, body []byte) (string, error) {
	s, err := from.Host.NewStream(ctx, pi.ID, FSProtocolID)
	if err != nil {
		return "", err
	}
	defer s.Close()
	if err := s.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", err
	}
	if err := protocol.WriteMetadata(s, FSRequest{Op: OpWrite, Path: path, ContentLen: claim}); err != nil {
		return "", err
	}
	var ack FSResponse
	if err := protocol.ReadMetadata(s, &ack); err != nil {
		return "", err
	}
	if ack.Error != "" {
		return "", fmt.Errorf("remote: %s", ack.Error)
	}
	if err := protocol.WriteFull(s, body); err != nil {
		return "", err
	}
	// Hang up without the promised bytes.
	return "", s.Close()
}

// A write that dies mid-flight must leave neither a partial destination nor
// a leftover temp file.
func TestFSWriteLeavesNoDebrisOnTruncation(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	// Claim 500 bytes but send 10, then hang up.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	writeTruncated(ctx, requester, pi, filepath.Join(root, "partial.txt"), 500, []byte("0123456789"))
	time.Sleep(300 * time.Millisecond) // let the server notice the hangup

	dst := filepath.Join(root, "partial.txt")
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("truncated write must not create the destination")
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if filepath.Ext(e.Name()) == "" && len(e.Name()) > 12 && e.Name()[:12] == ".lantern-push" {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestFSWritePolicyAnywhereStillPairs(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteAnywhere, MaxBytes: 1024})

	dst := filepath.Join(outside, "anywhere.txt")
	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("ok"), false); err != nil {
		t.Fatalf("anywhere policy should allow the write: %v", err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "ok" {
		t.Fatalf("write did not land: %q %v", got, err)
	}
}

// A policy with the shared-roots mode but no roots must refuse, not fall
// back to the read roots.
func TestFSWriteSharedRootsWithNoRootsRefuses(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteSharedRoots})

	if _, _, err := requester.WriteFS(fsCtx(t), pi, filepath.Join(root, "x.txt"), []byte("x"), false); err == nil {
		t.Fatal("expected refusal when no writable roots are configured")
	}
}

func TestFSWriteThenReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	content := []byte("round trip through the write path")
	dst := filepath.Join(root, "rt.txt")
	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, content, false); err != nil {
		t.Fatal(err)
	}
	_, _, data, eof, err := requester.ReadFS(fsCtx(t), pi, FSRequest{Op: OpRead, Path: dst})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) || !eof {
		t.Fatalf("round trip mismatch: %q", data)
	}
}

// A relative destination must land in the receiving device's first writable
// root, not relative to whatever directory the sender happens to run in.
func TestFSWriteRelativePathLandsInWritableRoot(t *testing.T) {
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	if err := os.MkdirAll(inbox, 0755); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, &WritePolicy{
		Mode:  WriteSharedRoots,
		Roots: []string{inbox},
	})

	if _, _, err := requester.WriteFS(fsCtx(t), pi, "note.txt", []byte("hi"), false); err != nil {
		t.Fatalf("relative write should succeed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(inbox, "note.txt")); err != nil || string(got) != "hi" {
		t.Fatalf("relative write landed wrong: %q %v", got, err)
	}
}

// A device with writes switched off must say so, not blame its roots.
func TestFSWriteDeniedErrorIsSpecific(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteDenied, Roots: []string{root}})

	_, _, err := requester.WriteFS(fsCtx(t), pi, "note.txt", []byte("hi"), false)
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("error should say writes are disabled, got: %v", err)
	}
}

// A relative destination under the anywhere policy has no root to anchor
// to, so it is refused rather than resolved against the process's cwd.
func TestFSWriteAnywhereRejectsRelativePath(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteAnywhere, MaxBytes: 1024})

	if _, _, err := requester.WriteFS(fsCtx(t), pi, "note.txt", []byte("hi"), false); err == nil {
		t.Fatal("expected refusal for a relative path with no writable root")
	}
}

// Nested destinations are created, but an ancestor that is a symlink out of
// the root must still be refused.
func TestFSWriteRejectsSymlinkedParentEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "hop")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	_, _, err := requester.WriteFS(fsCtx(t), pi, filepath.Join(root, "hop", "escaped.txt"), []byte("nope"), false)
	if err == nil {
		t.Fatal("expected refusal to write through a symlinked parent")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "escaped.txt")); !os.IsNotExist(statErr) {
		t.Fatal("write escaped the root via a symlinked parent")
	}
}
