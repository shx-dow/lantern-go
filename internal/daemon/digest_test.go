package daemon

// Tests for the push digest check. The receiving device both writes the bytes
// and computes the digest, so it is the party being trusted: these tests are
// about what happens when it reports no digest, or reports a wrong one.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/shx-dow/lantern-go/internal/p2p"
	"github.com/shx-dow/lantern-go/internal/protocol"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

func testDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// A receiver that reports no digest must not be able to pass as verified. This
// is the finding: the previous comparison skipped the check on an empty digest.
func TestVerifyRemoteDigestFailsClosed(t *testing.T) {
	local := testDigest("important bytes")

	if err := verifyRemoteDigest(local, ""); err == nil {
		t.Fatal("an absent digest must fail closed, not be skipped")
	}
	if err := verifyRemoteDigest(local, "   "); err == nil {
		t.Fatal("a blank digest must fail closed")
	}
	if err := verifyRemoteDigest(local, strings.Repeat("0", 64)); err == nil {
		t.Fatal("a wrong digest must fail")
	}
	if err := verifyRemoteDigest(local, local); err != nil {
		t.Fatalf("an identical digest must be accepted: %v", err)
	}
}

// The comparison is exact: case, whitespace, and length all matter, because a
// digest differing in any byte is a different digest. hex.EncodeToString emits
// lowercase, so uppercasing is the case variant worth testing.
func TestVerifyRemoteDigestComparisonIsExact(t *testing.T) {
	local := testDigest("payload")
	if local != strings.ToLower(local) {
		t.Fatalf("test setup produced a non-lowercase digest: %s", local)
	}

	rejected := []string{
		strings.ToUpper(local),
		" " + local,
		local + " ",
		local[:len(local)-1],
		local + "0",
	}
	for _, got := range rejected {
		if err := verifyRemoteDigest(local, got); err == nil {
			t.Errorf("a near-miss digest was accepted: %q", got)
		}
	}
}

// The two failures must be distinguishable, so an operator can tell a
// corrupted copy from an unverifiable one, and both must name the sent digest.
func TestVerifyRemoteDigestErrorsAreDistinguishable(t *testing.T) {
	local := testDigest("payload")

	absent := verifyRemoteDigest(local, "")
	if absent == nil {
		t.Fatal("expected a failure for an absent digest")
	}
	if !strings.Contains(absent.Error(), "no digest") {
		t.Errorf("an absent digest should say so: %v", absent)
	}

	mismatch := verifyRemoteDigest(local, strings.Repeat("a", 64))
	if mismatch == nil {
		t.Fatal("expected a failure for a mismatched digest")
	}
	if !strings.Contains(mismatch.Error(), "mismatch") {
		t.Errorf("a mismatch should say so: %v", mismatch)
	}
	for _, err := range []error{absent, mismatch} {
		if !strings.Contains(err.Error(), local) {
			t.Errorf("the error should name the sent digest: %v", err)
		}
	}
}

// End-to-end: a receiving device that consumes the bytes but reports no digest
// must make the whole push fail, not succeed with an empty sha256.
func TestPushFailsWhenRemoteReportsNoDigest(t *testing.T) {
	content := "important bytes"
	src := writePayload(t, content)

	d, peerID, cleanup := lyingReceiverDaemon(t, "", content)
	defer cleanup()

	_, err := d.PushFile(context.Background(), peerID, src, "", false)
	if err == nil {
		t.Fatal("a push to a receiver that reports no digest must fail")
	}
	if !strings.Contains(err.Error(), "no digest") {
		t.Fatalf("the failure should say the copy is unverifiable: %v", err)
	}
}

// The mirror case: a receiver that reports a wrong digest must also fail.
func TestPushFailsWhenRemoteDigestIsWrong(t *testing.T) {
	content := "important bytes"
	src := writePayload(t, content)

	d, peerID, cleanup := lyingReceiverDaemon(t, strings.Repeat("b", 64), content)
	defer cleanup()

	_, err := d.PushFile(context.Background(), peerID, src, "", false)
	if err == nil {
		t.Fatal("a push with a mismatched digest must fail")
	}
	if !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("the failure should name the mismatch: %v", err)
	}
}

// An honest receiver must still succeed, so the check does not break the
// normal path.
func TestPushSucceedsWithCorrectDigest(t *testing.T) {
	content := "important bytes"
	src := writePayload(t, content)

	d, peerID, cleanup := lyingReceiverDaemon(t, testDigest(content), content)
	defer cleanup()

	res, err := d.PushFile(context.Background(), peerID, src, "", false)
	if err != nil {
		t.Fatalf("an honest receiver must succeed: %v", err)
	}
	if res.LocalSHA256 != res.SHA256 {
		t.Fatalf("digests disagree: %+v", res)
	}
}

func writePayload(t *testing.T, content string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(src, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return src
}

// lyingReceiverDaemon builds a Daemon whose peer is a hand-rolled receiver that
// consumes the content and then reports whatever digest the test asks for. It
// returns the daemon, the receiver's peer ID, and a cleanup func.
//
// The receiver never writes the bytes anywhere, which is the point: it is fully
// in control of what it reports, and that is exactly the case the previous
// check skipped.
func lyingReceiverDaemon(t *testing.T, reportedDigest, content string) (*Daemon, string, func()) {
	t.Helper()
	dir := t.TempDir()

	node, err := p2p.NewNode(0, []string{"none"}, nil, filepath.Join(dir, "receiver"))
	if err != nil {
		t.Fatal(err)
	}
	node.Host.SetStreamHandler(p2p.FSProtocolID, func(s network.Stream) {
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(10 * time.Second))

		var req p2p.FSRequest
		if err := protocol.ReadMetadata(s, &req); err != nil {
			return
		}
		if req.Op != p2p.OpWrite {
			_ = protocol.WriteMetadata(s, p2p.FSResponse{Error: "unsupported op"})
			return
		}
		if err := protocol.WriteMetadata(s, p2p.FSResponse{Ready: true}); err != nil {
			return
		}
		buf := make([]byte, req.ContentLen)
		if _, err := readExactly(s, buf); err != nil {
			return
		}
		_ = protocol.WriteMetadata(s, p2p.FSResponse{
			Entry:  &p2p.FSEntry{Name: filepath.Base(req.Path), Size: req.ContentLen},
			Bytes:  req.ContentLen,
			EOF:    true,
			SHA256: reportedDigest,
		})
	})

	ln, err := lantern.New(lantern.Config{Port: 0, DataDir: filepath.Join(dir, "sender")})
	if err != nil {
		node.Close()
		t.Fatal(err)
	}
	d := New(ln)

	store, err := NewTrustStore(dir)
	if err != nil {
		d.ln.Close()
		node.Close()
		t.Fatal(err)
	}
	peerID := node.Host.ID().String()
	if _, err := store.Add(TrustSpec{PeerID: peerID, Alias: "laptop"}); err != nil {
		t.Fatal(err)
	}
	var addrs []string
	for _, a := range node.Host.Addrs() {
		addrs = append(addrs, a.String())
	}
	if err := store.UpdateAddrs(peerID, addrs); err != nil {
		t.Fatal(err)
	}
	d.Trust = store
	d.SharedDirs = []string{dir}
	// The daemon wires the trust store's address cache into the session layer
	// so a restart reaches a paired device before the network announces it.
	// The test must do the same or resolvePeer has nowhere to dial from.
	d.ln.WithKnownAddrs(d.PeerAddrs)
	if len(d.PeerAddrs(peerID)) == 0 {
		t.Fatalf("the receiver has no usable address; it advertised %v", addrs)
	}

	return d, peerID, func() {
		d.ln.Close()
		node.Close()
	}
}

// readExactly fills buf or fails, so the receiver really consumes the body.
func readExactly(s network.Stream, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := s.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
