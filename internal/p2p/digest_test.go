package p2p

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/crypto"
	"github.com/shx-dow/lantern-go/internal/protocol"
	"github.com/shx-dow/lantern-go/internal/storage"
)

// CONTEXT.md requires that "nothing is placed until the digest of what arrived
// has been verified". Before this change neither route did that:
//
//   - push had no digest field at all. The receiver hashed what arrived only to
//     hand the hash back, and the sender noticed a mismatch after the bytes were
//     already renamed or expanded into place.
//   - read and fetch guarded the comparison with "if the peer sent a digest", so
//     a peer that omitted one skipped verification and the file was renamed in
//     as a completed transfer.
//
// These tests pin the refusal, and they pin that the refusal leaves nothing
// behind: a leftover partial or a landed tree is what made the old behaviour
// expensive rather than merely wrong.

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// pushOutcome records what a push attempt actually did, so a test can assert on
// *when* the receiver refused rather than only that it refused. The two are not
// the same: a receiver that reads the whole payload and then compares digests
// still refuses, but it has already done the work the pre-content gate exists to
// avoid, and a test that only reads the final error cannot tell the difference.
type pushOutcome struct {
	ack FSResponse
	// final is the response after content, and is zero when the receiver
	// refused at the acknowledgement and no body was ever sent.
	final FSResponse
	// sentContent reports whether a body was written to the stream.
	sentContent bool
}

// refused reports the error the receiver gave, preferring the acknowledgement:
// a pre-content refusal never gets as far as the final response.
func (o pushOutcome) refused() string {
	if o.ack.Error != "" {
		return o.ack.Error
	}
	return o.final.Error
}

// pushWithDigest performs an OpWrite carrying a caller-chosen digest, standing
// in for a sender that mis-hashes or predates the field. writeFS always
// computes an honest one, so the dishonest cases have to open the stream here.
func pushWithDigest(t *testing.T, requester *Node, pi peer.AddrInfo, path string, content []byte, sum string, overwrite, unpack bool) pushOutcome {
	t.Helper()
	ctx := fsCtx(t)
	if err := requester.Host.Connect(ctx, pi); err != nil {
		t.Fatal(err)
	}
	s, err := requester.Host.NewStream(ctx, pi.ID, FSProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	req := FSRequest{
		Op:         OpWrite,
		Path:       path,
		ContentLen: int64(len(content)),
		Overwrite:  overwrite,
		Unpack:     unpack,
		SHA256:     sum,
	}
	if err := protocol.WriteMetadata(s, req); err != nil {
		t.Fatal(err)
	}
	var ack FSResponse
	if err := protocol.ReadMetadata(s, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Error != "" {
		return pushOutcome{ack: ack}
	}
	if err := protocol.WriteFull(s, content); err != nil {
		t.Fatal(err)
	}
	var resp FSResponse
	if err := protocol.ReadMetadata(s, &resp); err != nil {
		t.Fatal(err)
	}
	return pushOutcome{ack: ack, final: resp, sentContent: true}
}

// archiveOf builds a zip archive from a slash-separated file map.
func archiveOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	zipPath, cleanup, err := storage.ZipDirToTemp(writeTree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	b, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assertNoDebris checks root holds nothing but the named survivors, and no
// staging file.
//
// It walks recursively on purpose. A refused push that creates the destination's
// parent directories leaves empty directories behind rather than files, and a
// single-level scan sees an empty directory as one unexplained entry at best —
// it cannot tell an empty directory the push created from a legitimate one.
func assertNoDebris(t *testing.T, root string, survivors ...string) {
	t.Helper()
	keep := map[string]bool{}
	for _, s := range survivors {
		keep[s] = true
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		if strings.HasPrefix(d.Name(), ".lantern-push-") {
			t.Errorf("staging file %q survived a refused push", rel)
			return nil
		}
		if !keep[rel] && !keep[d.Name()] {
			t.Errorf("a refused push left %q behind in %s", rel, root)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------- push route

// TestFSWriteRefusesSenderWithoutDigest is the hard break: a peer that offers no
// digest is refused, and the refusal reports the protocol revision so an operator
// can tell an old device from a broken one.
//
// pushWithDigest stops at the acknowledgement, so this also pins that the
// refusal is pre-content: the sender never writes a body. Pulling a payload
// across the wire only to discard it would be a way to make a device do work on
// behalf of a peer it is about to refuse.
func TestFSWriteRefusesSenderWithoutDigest(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	dst := filepath.Join(root, "arrived.txt")
	out := pushWithDigest(t, requester, pi, dst, []byte("content with nothing to verify it"), "", false, false)
	if out.refused() == "" {
		t.Fatal("a write with no digest must be refused")
	}
	if !strings.Contains(out.refused(), "digest") {
		t.Fatalf("the refusal should name the missing digest, got: %v", out.refused())
	}
	if out.ack.Version != FSProtocolVersion {
		t.Fatalf("the refusal should report protocol version %d, got %d", FSProtocolVersion, out.ack.Version)
	}

	// The refusal must come before the payload. Reading the content and then
	// comparing it also refuses, so the error alone does not distinguish the two —
	// a peer with no digest would still get the device to pull every byte across
	// the wire, which is the work this gate exists to avoid.
	if out.sentContent {
		t.Error("the receiver asked for content before refusing the missing digest; the refusal is not pre-content")
	}
	if out.ack.Ready {
		t.Error("the receiver acknowledged the write before checking for a digest")
	}
	assertNoDebris(t, root)
}

// TestFSWriteRefusesMismatchedDigest pins that a corrupted push is refused
// before anything lands.
func TestFSWriteRefusesMismatchedDigest(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	dst := filepath.Join(root, "arrived.txt")
	out := pushWithDigest(t, requester, pi, dst,
		[]byte("these bytes are not what the sender said it sent"),
		digestOf([]byte("something else entirely")), false, false)
	if out.refused() == "" {
		t.Fatal("a write whose digest does not match must be refused")
	}
	if !strings.Contains(out.refused(), "mismatch") {
		t.Fatalf("the refusal should name the mismatch, got: %v", out.refused())
	}
	assertNoDebris(t, root)
}

// TestFSWriteMismatchLeavesExistingFileIntact pins that the refusal is
// non-destructive when the destination already exists.
func TestFSWriteMismatchLeavesExistingFileIntact(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	dst := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(dst, []byte("the original contents"), 0600); err != nil {
		t.Fatal(err)
	}

	out := pushWithDigest(t, requester, pi, dst,
		[]byte("replacement bytes"), digestOf([]byte("unrelated")), true /* overwrite */, false)
	if out.refused() == "" {
		t.Fatal("a mismatched overwrite must be refused")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("the destination must survive a refused write: %v", err)
	}
	if string(got) != "the original contents" {
		t.Fatalf("contents = %q, want the original", got)
	}
	assertNoDebris(t, root, "existing.txt")
}

// TestFSWriteRefusalCreatesNoDirectories pins that refusing a push leaves
// nothing at all behind, not just no file. The digest refusal has to sit before
// MkdirAll and CreateTemp, or the peer being refused still gets to materialise a
// directory tree inside a writable root. That is a smaller thing than landing
// unverified content, but it is still a refused write having an effect.
func TestFSWriteRefusalCreatesNoDirectories(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	dst := filepath.Join(root, "sub", "dir", "arrived.txt")
	out := pushWithDigest(t, requester, pi, dst, []byte("content with no digest"), "", false, false)
	if out.refused() == "" {
		t.Fatal("a write with no digest must be refused")
	}
	assertNoDebris(t, root)
}

// TestFSWriteMismatchLeavesNoTree is the directory case, which was the worst of
// the three: expandArchive ran before anything was compared, so a failed push
// left a whole tree on disk for the operator to clean up by hand.
func TestFSWriteMismatchLeavesNoTree(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	archive := archiveOf(t, map[string]string{
		"tree/a.txt":     "alpha",
		"tree/nested/b":  "beta",
		"tree/c/d/e.txt": "gamma",
	})
	dest := filepath.Join(root, "tree")

	out := pushWithDigest(t, requester, pi, dest, archive,
		digestOf([]byte("not the archive")), true /* overwrite */, true /* unpack */)
	if out.refused() == "" {
		t.Fatal("a directory push whose digest does not match must be refused")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("a refused directory push must not leave a tree at the destination")
	}
	assertNoDebris(t, root)
}

// TestFSWriteAcceptsMatchingDigest is the other side of the break: an honest
// sender is unaffected, so the new field cannot be tightened into a refusal of
// every legitimate push.
func TestFSWriteAcceptsMatchingDigest(t *testing.T) {
	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	content := []byte("verified content lands normally")
	dst := filepath.Join(root, "arrived.txt")
	out := pushWithDigest(t, requester, pi, dst, content, digestOf(content), false, false)
	if out.refused() != "" {
		t.Fatalf("a matching digest must be accepted, got refusal: %v", out.refused())
	}
	if !out.sentContent {
		t.Fatal("a valid push must actually send its content")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("contents = %q, want %q", got, content)
	}
	if out.final.SHA256 != digestOf(content) {
		t.Fatalf("response digest = %q, want %q", out.final.SHA256, digestOf(content))
	}
	if out.final.Version != FSProtocolVersion {
		t.Fatalf("a success should also report version %d, got %d", FSProtocolVersion, out.final.Version)
	}
	assertNoDebris(t, root, "arrived.txt")
}

// ---------------------------------------------------------------- read route

// lyingReadSender serves the read path with a caller-chosen digest, so the
// fetch tests can exercise a peer that reports none or reports the wrong one.
// A digest of "" models a revision 1 peer, which omits the field entirely.
// It returns the receiver to fetch from and the sender's address, because a
// fetch is something the receiver does to the sender.
func lyingReadSender(t *testing.T, content []byte, digest string) (*Node, peer.AddrInfo) {
	t.Helper()
	receiver, pi := lyingReadSenderInto(t, fsHost(t), content, digest, nil)
	return receiver, pi
}

// lyingReadSenderInto serves the read path to an existing receiver, so a test can
// have one device fetch from two peers — which is how two downloads end up
// competing for the same partial file.
//
// When release is non-nil the OpRead body is split around it, so the sender
// stalls mid-transfer until the test closes the channel. That is what lets a
// second download run while the first still holds the file.
func lyingReadSenderInto(t *testing.T, receiver *Node, content []byte, digest string, release <-chan struct{}) (*Node, peer.AddrInfo) {
	t.Helper()
	sender := fsHost(t)
	sender.Host.SetStreamHandler(FSProtocolID, func(s network.Stream) {
		defer s.Close()
		var req FSRequest
		if err := protocol.ReadMetadata(s, &req); err != nil {
			return
		}
		switch req.Op {
		case OpResolveShare:
			_ = protocol.WriteMetadata(s, stamped(FSResponse{
				Path:   "/served/file.txt",
				Entry:  &FSEntry{Name: "file.txt", Size: int64(len(content))},
				SHA256: digest,
			}))
		case OpRead:
			_ = protocol.WriteMetadata(s, stamped(FSResponse{
				Entry: &FSEntry{Name: "file.txt", Size: int64(len(content))},
				Bytes: int64(len(content)),
				EOF:   true,
			}))
			if release == nil {
				if int64(len(content)) > 0 {
					_, _ = s.Write(content)
				}
				return
			}
			half := len(content) / 2
			if _, err := s.Write(content[:half]); err != nil {
				return
			}
			<-release
			_, _ = s.Write(content[half:])
		default:
			_ = protocol.WriteMetadata(s, fail("unexpected op"))
		}
	})

	pi := peer.AddrInfo{ID: sender.Host.ID(), Addrs: sender.Host.Addrs()}
	if err := receiver.Host.Connect(fsCtx(t), pi); err != nil {
		t.Fatal(err)
	}
	return receiver, pi
}

// fetchInto runs a read-path fetch from a receiver built by lyingReadSender.
func fetchInto(t *testing.T, receiver *Node, pi peer.AddrInfo, code, out string) error {
	t.Helper()
	return receiver.FetchFS(fetchCtx(t), pi, code, out, make(chan TransferProgress, 32))
}

// assertNoFetchArtifacts checks a refused fetch left neither a delivered file
// nor the staged partial or resume checkpoint. Both used to linger, so the next
// attempt resumed into bytes that had already failed verification.
func assertNoFetchArtifacts(t *testing.T, out string) {
	t.Helper()
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("a refused fetch left %q in %s; the next attempt would resume into it", e.Name(), out)
	}
}

func TestFetchRefusesSenderWithoutDigest(t *testing.T) {
	receiver, pi := lyingReadSender(t, []byte("content with nothing to verify it"), "")

	out := t.TempDir()
	err := fetchInto(t, receiver, pi, "66666666666666666666666666666666", out)
	if err == nil {
		t.Fatal("a fetch from a sender that reports no digest must be refused")
	}
	if !strings.Contains(err.Error(), "no digest") {
		t.Fatalf("the refusal should name the missing digest, got: %v", err)
	}
	assertNoFetchArtifacts(t, out)
}

func TestFetchRefusesMismatchedDigest(t *testing.T) {
	receiver, pi := lyingReadSender(t, []byte("content that will not match"), strings.Repeat("a", 64))

	out := t.TempDir()
	err := fetchInto(t, receiver, pi, "77777777777777777777777777777777", out)
	if err == nil {
		t.Fatal("a fetch whose digest does not match must be refused")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("the refusal should name the mismatch, got: %v", err)
	}
	assertNoFetchArtifacts(t, out)
}

// TestFetchAcceptsMatchingDigest keeps the happy path honest: an honest sender
// still fetches, and nothing is left behind on success beyond the file.
func TestFetchAcceptsMatchingDigest(t *testing.T) {
	content := []byte("verified content arrives over the read path")
	receiver, pi := lyingReadSender(t, content, digestOf(content))

	out := t.TempDir()
	if err := fetchInto(t, receiver, pi, "88888888888888888888888888888888", out); err != nil {
		t.Fatalf("a matching digest must be accepted: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "file.txt"))
	if err != nil {
		t.Fatalf("the fetch did not land: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("contents = %q, want %q", got, content)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("a successful fetch left %d entries, want just the delivered file", len(entries))
	}
}

// ------------------------------------------------------------- share-code route

// shareSender registers sourcePath under code and then reports a caller-chosen
// digest in the transfer header, standing in for a sender that predates the
// field or mis-hashes. RegisterShareHandler always hashes honestly, so the
// dishonest cases have to overwrite what it recorded.
//
// The digest is blanked on the registered share rather than in the serving path,
// so the sender still streams real bytes: the receiver must refuse them on the
// header alone, before it could tell that anything was wrong.
func shareSender(t *testing.T, sourcePath, code, digest string) peer.AddrInfo {
	t.Helper()
	ctx := fsCtx(t)
	sender := fsHost(t)
	sender.ctx = ctx
	if err := sender.RegisterShareHandler(code, sourcePath, make(chan TransferProgress, 64)); err != nil {
		t.Fatal(err)
	}

	sender.mu.Lock()
	state := sender.shares[code]
	if state == nil {
		sender.mu.Unlock()
		t.Fatal("share was not registered")
	}
	// Under the same lock RegisterShareHandler writes it, so a serving goroutine
	// that reads it later has a happens-before edge on this write.
	state.fileHash = digest
	sender.mu.Unlock()

	pi := peer.AddrInfo{ID: sender.Host.ID(), Addrs: sender.Host.Addrs()}
	receiver := fsHost(t)
	receiver.ctx = ctx
	if err := receiver.Host.Connect(ctx, pi); err != nil {
		t.Fatal(err)
	}
	return pi
}

// assertNoTransferArtifacts checks a refused share-code transfer left neither a
// delivered file nor the staged partial or resume checkpoint. A leftover partial
// is worse than a missing file: the next attempt resumes into bytes that already
// failed verification.
func assertNoTransferArtifacts(t *testing.T, out string) {
	t.Helper()
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("a refused transfer left %q in %s; the next attempt would resume into it", e.Name(), out)
	}
}

// TestShareTransferRefusesSenderWithoutDigest is the share-code half of the hard
// break. The read route had this covered and the transfer route did not, which
// is the same shape of gap the audit found: the check existed on one path and
// not the other.
func TestShareTransferRefusesSenderWithoutDigest(t *testing.T) {
	source := filepath.Join(t.TempDir(), "payload.bin")
	content := make([]byte, 3*crypto.ChunkSize+11)
	for i := range content {
		content[i] = byte(i % 251)
	}
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}

	code := "5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a"
	pi := shareSender(t, source, code, "" /* revision 1: no digest */)

	out := t.TempDir()
	receiver := fsHost(t)
	receiver.ctx = fsCtx(t)
	err := receiver.receiveFile(fsCtx(t), pi, code, out, make(chan TransferProgress, 64))
	if err == nil {
		t.Fatal("a transfer from a sender that reports no digest must be refused")
	}
	if !strings.Contains(err.Error(), "no digest") {
		t.Fatalf("the refusal should name the missing digest, got: %v", err)
	}
	assertNoTransferArtifacts(t, out)
}

// TestShareTransferRefusesMismatchedDigest pins that corrupted content over the
// share-code route is refused before the file is renamed into place.
func TestShareTransferRefusesMismatchedDigest(t *testing.T) {
	source := filepath.Join(t.TempDir(), "payload.bin")
	content := make([]byte, 2*crypto.ChunkSize+7)
	for i := range content {
		content[i] = byte((i * 7) % 251)
	}
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}

	code := "6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b"
	pi := shareSender(t, source, code, strings.Repeat("a", 64))

	out := t.TempDir()
	receiver := fsHost(t)
	receiver.ctx = fsCtx(t)
	err := receiver.receiveFile(fsCtx(t), pi, code, out, make(chan TransferProgress, 64))
	if err == nil {
		t.Fatal("a transfer whose digest does not match must be refused")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("the refusal should name the mismatch, got: %v", err)
	}
	assertNoTransferArtifacts(t, out)
}

// TestShareTransferAcceptsMatchingDigest keeps the honest path working, so the
// new refusal cannot be tightened into a rejection of every real transfer.
func TestShareTransferAcceptsMatchingDigest(t *testing.T) {
	source := filepath.Join(t.TempDir(), "payload.bin")
	content := make([]byte, crypto.ChunkSize+5)
	for i := range content {
		content[i] = byte(i % 251)
	}
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}

	code := "7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c"
	pi := shareSender(t, source, code, digestOf(content))

	out := t.TempDir()
	receiver := fsHost(t)
	receiver.ctx = fsCtx(t)
	if err := receiver.receiveFile(fsCtx(t), pi, code, out, make(chan TransferProgress, 64)); err != nil {
		t.Fatalf("a matching digest must be accepted: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "payload.bin"))
	if err != nil {
		t.Fatalf("the transfer did not land: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("contents differ from the source (%d bytes vs %d)", len(got), len(content))
	}
}

// Two concurrent downloads of the same code into the same directory resolve to
// the same partial file, so a refusal must not delete the other's in-flight work.
func TestRefusedFetchDoesNotDeleteConcurrentFetch(t *testing.T) {
	content := []byte(strings.Repeat("payload ", 500))
	code := "9d9d9d9d9d9d9d9d9d9d9d9d9d9d9d9d9d"
	out := t.TempDir()

	// One receiver, as in production: a device has a single node.
	receiver := fsHost(t)
	release := make(chan struct{})
	_, honestPi := lyingReadSenderInto(t, receiver, content, digestOf(content), release)

	honestErr := make(chan error, 1)
	go func() {
		honestErr <- receiver.FetchFS(fsCtx(t), honestPi, code, out, make(chan TransferProgress, 64))
	}()

	// Wait until the honest fetch has staged its partial and is mid-body.
	partial, err := storage.PartialPath(out, code, "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(partial); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatalf("the honest fetch never staged %s: %v", partial, err)
	}

	// A second fetch for the same code and directory now runs, from a peer whose
	// digest will not match. It must be refused up front rather than allowed to
	// delete the partial the first one is still writing.
	_, liarPi := lyingReadSenderInto(t, receiver, content, strings.Repeat("c", 64), nil)

	secondErr := receiver.FetchFS(fsCtx(t), liarPi, code, out, make(chan TransferProgress, 64))
	if secondErr == nil {
		t.Error("the second fetch should have been refused while the first holds the download")
	} else {
		t.Logf("second fetch refused with: %v", secondErr)
	}

	// The honest fetch must still be able to land its file.
	close(release)
	if err := <-honestErr; err != nil {
		t.Errorf("concurrent fetch was broken by the other one being refused: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "file.txt"))
	if err != nil {
		t.Errorf("the concurrent fetch did not land: %v", err)
	} else if string(got) != string(content) {
		t.Errorf("landed %d bytes, want %d", len(got), len(content))
	}
}

// A sender that offers no digest and sends no body: the receiver must refuse on
// the header alone rather than waiting for bytes that never come.
func TestShareTransferRefusesBeforeReadingBody(t *testing.T) {
	source := filepath.Join(t.TempDir(), "payload.bin")
	content := []byte(strings.Repeat("never sent", 100))
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}

	code := "8e8e8e8e8e8e8e8e8e8e8e8e8e8e8e8e8e"
	ctx := fsCtx(t)
	sender := fsHost(t)
	sender.ctx = ctx
	if err := sender.RegisterShareHandler(code, source, make(chan TransferProgress, 64)); err != nil {
		t.Fatal(err)
	}
	sender.mu.Lock()
	sender.shares[code].fileHash = ""
	sender.mu.Unlock()

	pi := peer.AddrInfo{ID: sender.Host.ID(), Addrs: sender.Host.Addrs()}
	receiver := fsHost(t)
	receiver.ctx = ctx
	if err := receiver.Host.Connect(ctx, pi); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	start := time.Now()
	err := receiver.receiveFile(ctx, pi, code, out, make(chan TransferProgress, 64))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "no digest") {
		t.Fatalf("the refusal should name the missing digest, got: %v", err)
	}
	t.Logf("refused in %v with: %v", elapsed, err)

	entries, rerr := os.ReadDir(out)
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, e := range entries {
		t.Errorf("a pre-body refusal left %q behind in %s", e.Name(), out)
	}
}
