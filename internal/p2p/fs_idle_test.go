package p2p

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/protocol"
)

// withFSTimeout shortens the package setting for one test. No test in this
// package calls t.Parallel, so the global is safe to move.
func withFSTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := fsiOTimeout
	fsiOTimeout = d
	t.Cleanup(func() { fsiOTimeout = old })
}

// TestFSWriteSurvivesTransferLongerThanTimeout pins that the fs stream
// deadline measures silence, not total transfer time.
//
// The sender takes longer in total than the provider's timeout while never
// pausing longer than it. An absolute deadline set when the stream opened cut
// the transfer off mid-body and the push failed even though bytes were still
// arriving; an idle deadline only fires when the peer actually stops.
func TestFSWriteSurvivesTransferLongerThanTimeout(t *testing.T) {
	root := t.TempDir()
	provider := fsHost(t)
	requester := fsHost(t)

	// Long enough that ordinary scheduling jitter is not mistaken for a
	// stalled peer, short enough that the test does not wait 30 seconds.
	withFSTimeout(t, 800*time.Millisecond)

	provider.SetReadAccess(allowRootsFor([]string{root}, requester.Host.ID().String()))
	provider.SetWriteAccess(grantEveryone(writePolicy(root, 0)))
	provider.RegisterFSHandler()

	pi := peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
	dst := filepath.Join(root, "slow.bin")
	content := bytes.Repeat([]byte("abcdefgh"), 96) // 768 bytes

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := requester.Host.Connect(ctx, pi); err != nil {
		t.Fatal(err)
	}
	s, err := requester.Host.NewStream(ctx, pi.ID, FSProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sum := sha256.Sum256(content)
	if err := protocol.WriteMetadata(s, FSRequest{
		Op:         OpWrite,
		Path:       dst,
		PathB64:    protocol.Base64Of(dst),
		ContentLen: int64(len(content)),
		SHA256:     hex.EncodeToString(sum[:]),
	}); err != nil {
		t.Fatal(err)
	}
	var ack FSResponse
	if err := protocol.ReadMetadata(s, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Error != "" {
		t.Fatalf("remote refused the write: %s", ack.Error)
	}

	// Twelve chunks, 150ms apart: 1.65s in total, but no single gap reaches
	// the 800ms idle limit. An absolute deadline expires during this loop.
	const chunk = 64
	for off := 0; off < len(content); off += chunk {
		end := off + chunk
		if end > len(content) {
			end = len(content)
		}
		if err := protocol.WriteFull(s, content[off:end]); err != nil {
			t.Fatalf("send at offset %d: %v", off, err)
		}
		if end < len(content) {
			time.Sleep(150 * time.Millisecond)
		}
	}

	var resp FSResponse
	if err := protocol.ReadMetadata(s, &resp); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("remote: %s", resp.Error)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("stored content does not match what was sent")
	}
}
