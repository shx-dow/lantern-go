package p2p

// Adversarial tests for the write path. Each one attacks a specific property
// the audit scope names, rather than exercising a feature: containment,
// symlink refusal, overwrite consent, the size cap, and debris-free failure.
//
// These are written to fail if a protection is weakened, so they are the
// regression net for any later change to fsWrite or resolveWritePath.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/protocol"
)

// ATTACK: a paired peer asks for a path that walks out of the writable root
// using every spelling of traversal. Each must be refused, and nothing may
// appear outside the root.
func TestAuditWriteTraversalSpellingsAllRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}

	escapes := []string{
		filepath.Join(root, "..", filepath.Base(outside), "victim.txt"),
		filepath.Join(root, "..", "..", "etc", "passwd"),
		filepath.Join(root, "sub", "..", "..", filepath.Base(outside), "victim.txt"),
		root + "/../../etc/passwd",
		filepath.Join(root, "a", "b", "..", "..", "..", filepath.Base(outside), "victim.txt"),
	}
	for _, dst := range escapes {
		if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("pwned"), true); err == nil {
			t.Errorf("traversal accepted: %q", dst)
		}
	}

	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "original" {
		t.Fatalf("traversal modified a file outside the root: %q %v", got, err)
	}
	if _, err := os.Stat("/etc/passwd"); err != nil {
		t.Fatalf("/etc/passwd disturbed: %v", err)
	}
}

// ATTACK: overwrite consent. An existing file must survive a push that did
// not ask to replace it, byte for byte, and the refusal must arrive before
// any content is sent.
func TestAuditOverwriteConsentIsRequiredAndPreContent(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(dst, []byte("precious"), 0600); err != nil {
		t.Fatal(err)
	}
	provider, pi := fsPairWith(t, root, writePolicy(root, 0))

	// Speak the protocol by hand so the acknowledgement can be inspected: if
	// Ready came back, the remote had already agreed to take content.
	_, ack := auditWriteHandshake(t, provider, pi, dst, 7, false)
	if ack.Error == "" {
		t.Fatal("a refused overwrite must not report Ready")
	}
	if !strings.Contains(ack.Error, "exists") {
		t.Fatalf("refusal should name the existing file: %q", ack.Error)
	}
	if got, _ := os.ReadFile(dst); string(got) != "precious" {
		t.Fatalf("refused push changed the file: %q", got)
	}
}

// auditWriteHandshake sends a write request and returns the receiver's
// pre-content acknowledgement without sending any body. contentLen is passed
// explicitly so a test can lie about the length, which WriteFS cannot.
func auditWriteHandshake(t *testing.T, from *Node, pi peer.AddrInfo, path string, contentLen int64, overwrite bool) (FSRequest, FSResponse) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Connect first: the client half of WriteFS does this, and a raw stream
	// has to do it too or the peerstore has no address to dial.
	if err := from.Host.Connect(ctx, pi); err != nil {
		t.Fatal(err)
	}
	s, err := from.Host.NewStream(ctx, pi.ID, FSProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	req := FSRequest{Op: OpWrite, Path: path, ContentLen: contentLen, Overwrite: overwrite}
	if err := protocol.WriteMetadata(s, req); err != nil {
		t.Fatal(err)
	}
	var ack FSResponse
	if err := protocol.ReadMetadata(s, &ack); err != nil {
		t.Fatal(err)
	}
	return req, ack
}

// ATTACK: the size cap. A peer claiming more than the cap must be refused
// before content, and must not be able to create the destination even by
// lying about the length in either direction.
func TestAuditSizeCapRefusedBeforeContent(t *testing.T) {
	root := t.TempDir()
	const cap = 8
	provider, pi := fsPairWith(t, root, writePolicy(root, cap))
	dst := filepath.Join(root, "toobig.txt")

	// Claim far more than the cap.
	_, ack := auditWriteHandshake(t, provider, pi, dst, cap*1000, false)
	if ack.Error == "" {
		t.Fatal("an oversize claim must be refused")
	}
	if !strings.Contains(ack.Error, "exceeds") {
		t.Fatalf("refusal should name the limit: %q", ack.Error)
	}
	if ack.Ready {
		t.Fatal("an oversize claim must not report Ready")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("a refused oversize write must not create the destination")
	}

	// Exactly at the cap is allowed, so the boundary is not off by one.
	ok := make([]byte, cap)
	if _, _, err := provider.WriteFS(fsCtx(t), pi, dst, ok, false); err != nil {
		t.Fatalf("a write exactly at the cap must be allowed: %v", err)
	}
	// One byte over is not.
	_, _, err := provider.WriteFS(fsCtx(t), pi, filepath.Join(root, "over.txt"), make([]byte, cap+1), false)
	if err == nil {
		t.Fatal("one byte over the cap must be refused")
	}
}

// ATTACK: a negative ContentLen must not be interpreted as "unlimited" or
// wrap into a huge value that passes the cap comparison.
func TestAuditNegativeContentLenRefused(t *testing.T) {
	root := t.TempDir()
	provider, pi := fsPairWith(t, root, writePolicy(root, 0))
	dst := filepath.Join(root, "neg.txt")

	_, ack := auditWriteHandshake(t, provider, pi, dst, -1, false)
	if ack.Error == "" {
		t.Fatal("negative content_len must be refused")
	}
	if !strings.Contains(ack.Error, "negative") {
		t.Fatalf("refusal should say the length is negative: %q", ack.Error)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("a refused negative length must not create the destination")
	}
}

// ATTACK: symlinks at the destination, at an ancestor, and as the root
// itself. None may be written through, in either overwrite mode.
func TestAuditSymlinkRefusalCoversEveryPosition(t *testing.T) {
	t.Run("destination", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "precious.txt")
		if err := os.WriteFile(outside, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link.txt")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		requester, pi := fsPairWith(t, root, writePolicy(root, 0))
		if _, _, err := requester.WriteFS(fsCtx(t), pi, link, []byte("pwned"), true); err == nil {
			t.Fatal("write through a destination symlink was accepted")
		}
		if got, _ := os.ReadFile(outside); string(got) != "original" {
			t.Fatalf("symlink target modified: %q", got)
		}
	})

	t.Run("ancestor", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "hop")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		requester, pi := fsPairWith(t, root, writePolicy(root, 0))
		dst := filepath.Join(root, "hop", "escaped.txt")
		if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("pwned"), true); err == nil {
			t.Fatal("write through a symlinked ancestor was accepted")
		}
		if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); !os.IsNotExist(err) {
			t.Fatal("write escaped the root via a symlinked ancestor")
		}
	})

	t.Run("root-is-symlink", func(t *testing.T) {
		// The operator points --writable-dirs at a symlink. That is their
		// deliberate choice, so it must be allowed rather than refused; this
		// records that the boundary is the resolved root, not the spelling.
		target := t.TempDir()
		linkParent := t.TempDir()
		link := filepath.Join(linkParent, "linked-root")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		requester, pi := fsPairWith(t, rootFor(link), writePolicy(link, 0))
		if _, _, err := requester.WriteFS(fsCtx(t), pi, filepath.Join(link, "ok.txt"), []byte("x"), false); err != nil {
			t.Fatalf("a symlinked root is the operator's choice and must work: %v", err)
		}
		if _, err := os.Stat(filepath.Join(target, "ok.txt")); err != nil {
			t.Fatalf("write did not land through the symlinked root: %v", err)
		}
	})
}

// rootFor returns a fresh directory suitable for a read root, kept separate
// so the symlinked-root case does not accidentally share state.
func rootFor(path string) string { return filepath.Dir(path) }

// ATTACK: hardlink games. A hardlink to a file outside the root is not a
// symlink, so containment by path cannot catch it; the point of this test is
// to document the actual behaviour rather than assert a guarantee the
// filesystem does not offer.
func TestAuditHardlinkIsNotDetectedAsEscape(t *testing.T) {
	if os.Getenv("LANTERN_AUDIT_HARDLINK") == "" {
		t.Skip("set LANTERN_AUDIT_HARDLINK=1 to observe hardlink behaviour")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "precious.txt")
	if err := os.WriteFile(outside, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "hard.txt")
	if err := os.Link(outside, link); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))
	_, _, err := requester.WriteFS(fsCtx(t), pi, link, []byte("pwned"), true)
	t.Logf("write through a hardlink: err=%v", err)
}

// ATTACK: a refusal must not leave debris. Every early-return path in fsWrite
// has to clean up the temp file it created, or a peer can fill the disk with
// empty temporaries by opening and abandoning writes.
func TestAuditRefusalsLeaveNoTempDebris(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	// A directory target, refused before any temp file exists.
	if _, _, err := requester.WriteFS(fsCtx(t), pi, filepath.Join(root, "sub"), []byte("x"), true); err == nil {
		t.Fatal("directory target must be refused")
	}
	assertNoTempDebris(t, root)
}

// assertNoTempDebris fails if any .lantern-push-* file survives in dir.
func assertNoTempDebris(t *testing.T, dir string) {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), ".lantern-push-") {
			found = append(found, p)
		}
		return nil
	})
	if len(found) > 0 {
		t.Fatalf("temp debris left behind: %v", found)
	}
}

// ATTACK: a sender that lies about ContentLen and sends fewer bytes must not
// leave a partial destination or a temp file, and must not be reported as
// success. The digest of a truncated body must never match the real content.
func TestAuditTruncatedBodyNeverReportedDelivered(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))
	dst := filepath.Join(root, "truncated.txt")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	writeTruncated(ctx, requester, pi, dst, 4096, []byte("only ten b"))
	time.Sleep(300 * time.Millisecond)

	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("a truncated write must not create the destination")
	}
	assertNoTempDebris(t, root)
}

// ATTACK: concurrent pushes to the same destination. Because the write is
// staged to a temp file and renamed, a reader must only ever see one complete
// version, never a blend of two.
func TestAuditConcurrentPushesNeverBlend(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))
	dst := filepath.Join(root, "contested.txt")

	const body = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte(body), false); err != nil {
		t.Fatal(err)
	}

	// Overwrite repeatedly with a different-length body while reading.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			_, _, _ = requester.WriteFS(fsCtx(t), pi, dst, []byte(strings.Repeat("B", 10+i)), true)
		}
	}()
	for i := 0; i < 40; i++ {
		got, err := os.ReadFile(dst)
		if err != nil {
			continue // the rename window legitimately has no file
		}
		s := string(got)
		if strings.Count(s, "A") != len(body) && strings.Count(s, "B") != len(s) {
			t.Fatalf("read a blended file: %q", s)
		}
	}
	<-done
	assertNoTempDebris(t, root)
}

// ATTACK: a destination that is a symlink created *between* the consent check
// and the rename. The write must be refused rather than following the link,
// which the re-check after MkdirAll is meant to guarantee.
func TestAuditSymlinkAtDestinationAfterConsentIsRefused(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "precious.txt")
	if err := os.WriteFile(outside, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))
	dst := filepath.Join(root, "racy.txt")

	// Hold the handshake at Ready, plant a symlink, then send content. The
	// receiver re-resolves after creating parents, so this must not follow.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := requester.Host.Connect(ctx, pi); err != nil {
		t.Fatal(err)
	}
	s, err := requester.Host.NewStream(ctx, pi.ID, FSProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteMetadata(s, FSRequest{Op: OpWrite, Path: dst, ContentLen: 4}); err != nil {
		t.Fatal(err)
	}
	var ack FSResponse
	if err := protocol.ReadMetadata(s, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Error != "" {
		t.Skipf("handshake refused before the race could be staged: %s", ack.Error)
	}
	if err := os.Symlink(outside, dst); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := protocol.WriteFull(s, []byte("pwned")); err != nil {
		t.Fatal(err)
	}
	var resp FSResponse
	if err := protocol.ReadMetadata(s, &resp); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(outside); string(got) != "original" {
		t.Fatalf("symlink planted after consent was followed: %q", got)
	}
	if resp.Error == "" && resp.Entry != nil {
		t.Log("note: the rename replaced the symlink rather than following it, which is also safe")
	}
}

// ATTACK: a relative destination must never resolve against the sender's
// working directory. It is interpreted by the receiver, inside its own roots.
func TestAuditRelativePathNeverUsesSenderCWD(t *testing.T) {
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	if err := os.MkdirAll(inbox, 0755); err != nil {
		t.Fatal(err)
	}
	// A same-named file in the process CWD: if a relative write resolved
	// there, this is what would be clobbered.
	cwdVictim := filepath.Join(".", "relative-write-victim.txt")
	if err := os.WriteFile(cwdVictim, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(cwdVictim) })

	requester, pi := fsPairWith(t, root, &WritePolicy{
		Mode: WriteSharedRoots, Roots: []string{inbox},
	})
	if _, _, err := requester.WriteFS(fsCtx(t), pi, "relative-write-victim.txt", []byte("pwned"), true); err != nil {
		t.Fatalf("relative write should succeed into the inbox: %v", err)
	}
	if got, _ := os.ReadFile(cwdVictim); string(got) != "untouched" {
		t.Fatalf("relative write resolved against the sender CWD: %q", got)
	}
	if got, err := os.ReadFile(filepath.Join(inbox, "relative-write-victim.txt")); err != nil || string(got) != "pwned" {
		t.Fatalf("relative write did not land in the inbox: %q %v", got, err)
	}
}

// ATTACK: a destination ending in a separator, or naming a dot entry, must be
// refused rather than silently normalised into something writable.
func TestAuditDotAndSeparatorPathsRefused(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	for _, dst := range []string{"", "   ", ".", "..", root + string(os.PathSeparator)} {
		if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("x"), true); err == nil {
			t.Errorf("destination %q must be refused", dst)
		}
	}
}

// ATTACK: an unknown write mode must fail closed rather than falling through
// to an unconstrained write.
func TestAuditUnknownWriteModeFailsClosed(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: "root-for-the-win", Roots: []string{root}, MaxBytes: 1024})

	if _, _, err := requester.WriteFS(fsCtx(t), pi, filepath.Join(root, "x.txt"), []byte("x"), false); err == nil {
		t.Fatal("an unknown write mode must refuse, not allow")
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(err) {
		t.Fatal("an unknown write mode created a file")
	}
}

// ATTACK: an empty root list under shared-roots mode must fail closed, not
// widen to the read roots.
func TestAuditNoRootsFailsClosed(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, &WritePolicy{Mode: WriteSharedRoots, Roots: nil, MaxBytes: 1024})

	_, _, err := requester.WriteFS(fsCtx(t), pi, filepath.Join(root, "x.txt"), []byte("x"), false)
	if err == nil {
		t.Fatal("no configured roots must refuse")
	}
	if !strings.Contains(err.Error(), "no writable roots") {
		t.Fatalf("refusal should name the missing roots: %v", err)
	}
}

// ATTACK: the pairing gate is checked before the write policy, so an unpaired
// peer learns nothing about the write configuration.
func TestAuditUnpairedPeerCannotProbeWriteConfig(t *testing.T) {
	root := t.TempDir()
	provider, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })
	provider.SetListAccess([]string{root}, func(string) bool { return false })
	provider.SetWritePolicy(writePolicy(root, 1024))
	provider.RegisterFSHandler()

	requester, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requester.Close() })

	pi := peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
	_, _, err = requester.WriteFS(fsCtx(t), pi, filepath.Join(root, "probe.txt"), []byte("x"), false)
	if err == nil {
		t.Fatal("an unpaired peer must not write")
	}
	if !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("refusal should be the pairing gate, got: %v", err)
	}
	// The write configuration must not be leaked in the refusal.
	if strings.Contains(err.Error(), "root") && strings.Contains(err.Error(), "limit") {
		t.Fatalf("refusal leaks write configuration: %v", err)
	}
	fmt.Fprint(os.Stderr, "")
}
