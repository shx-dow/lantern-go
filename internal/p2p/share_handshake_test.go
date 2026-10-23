package p2p

// Tests for the transfer handshake that runs before a share code is known.
//
// serveShare reads a JSON request from any peer that can open the protocol
// stream. Until the request has arrived there is no code to check and no
// authentication, so the read itself has to be bounded in both time and size.
// The two controls below fail against the old handler: one holds the stream
// open without writing, the other writes past the metadata cap without ever
// closing the JSON value. The third test is a regression net for clearing the
// deadline before the body is streamed.

import (
	"bytes"
	crand "crypto/rand"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/crypto"
	"github.com/shx-dow/lantern-go/internal/protocol"
)

// withShareHandshakeTimeout shortens the package setting for one test. No test
// in this package calls t.Parallel, so the global is safe to move.
func withShareHandshakeTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := shareHandshakeTimeout
	shareHandshakeTimeout = d
	t.Cleanup(func() { shareHandshakeTimeout = old })
}

// shareHosts registers path under code on a provider and returns the requester
// and the provider's address. The progress buffer is large enough that the
// handler never blocks reporting a chunk while a test reads the body.
func shareHosts(t *testing.T, code, path string) (*Node, peer.AddrInfo) {
	t.Helper()
	provider := fsHost(t)
	requester := fsHost(t)
	progress := make(chan TransferProgress, 256)
	if err := provider.RegisterShareHandler(fsCtx(t), code, path, progress); err != nil {
		t.Fatalf("share: %v", err)
	}
	t.Cleanup(func() {
		provider.ClearLocal(code)
		provider.forgetShare(code)
	})
	return requester, peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
}

func openShareStream(t *testing.T, requester *Node, pi peer.AddrInfo) network.Stream {
	t.Helper()
	ctx := fsCtx(t)
	if err := requester.Host.Connect(ctx, pi); err != nil {
		t.Fatalf("connect: %v", err)
	}
	s, err := requester.Host.NewStream(ctx, pi.ID, ProtocolID)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// A peer that opens the share stream and sends nothing must be disconnected
// within the handshake window. Without a deadline serveShare waits forever:
// the peer has not failed authentication, it simply has not spoken.
func TestShareHandshakeClosesIdleStream(t *testing.T) {
	const timeout = 250 * time.Millisecond
	withShareHandshakeTimeout(t, timeout)

	root := t.TempDir()
	path := filepath.Join(root, "shared.txt")
	writeFile(t, path, "shared bytes")
	requester, pi := shareHosts(t, "idle-handshake", path)
	s := openShareStream(t, requester, pi)

	// The local deadline is much longer than the server's. A timeout here
	// therefore means the server never closed its end, which is the defect.
	const localRead = 3 * time.Second
	if err := s.SetReadDeadline(time.Now().Add(localRead)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := s.Read(make([]byte, 1))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("the stream stayed readable after the handshake window")
	}
	if elapsed >= localRead {
		t.Fatalf("the idle handshake was still open after %v; the server never closed it", elapsed)
	}
	t.Logf("closed after %v with: %v", elapsed, err)
}

// A peer that sends more than the metadata cap without terminating its JSON
// value must be cut off at the cap. Before the limit the decoder consumed
// whatever arrived, and a sender that never finished the value held the
// handler open indefinitely.
func TestShareHandshakeRejectsOversizedRequest(t *testing.T) {
	// Keep the read deadline out of this test: the only thing that may close
	// the stream is the size cap.
	withShareHandshakeTimeout(t, 10*time.Second)

	root := t.TempDir()
	path := filepath.Join(root, "shared.txt")
	writeFile(t, path, "shared bytes")
	requester, pi := shareHosts(t, "oversized-handshake", path)
	s := openShareStream(t, requester, pi)

	// An unterminated JSON string. The first 64 KiB are what a bounded reader
	// accepts; the rest stays on the wire.
	payload := append([]byte(`{"challenge":"`), bytes.Repeat([]byte("A"), protocol.MaxMetadataSize+16*1024)...)
	_, _ = s.Write(payload) // a write error can be the server closing at the cap

	const localRead = 3 * time.Second
	if err := s.SetReadDeadline(time.Now().Add(localRead)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := s.Read(make([]byte, 1))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("the stream stayed readable after an oversized request")
	}
	if elapsed >= localRead {
		t.Fatalf("the oversized request was still being read after %v; the cap did not close it", elapsed)
	}
	t.Logf("closed after %v with: %v", elapsed, err)
}

// A valid handshake must not leave its deadline in place for the body. A file
// can take far longer to send than the peer was given to start one. This is a
// regression net: it passes without the fix because there was no deadline to
// clear, and it fails if a fix adds the deadline but forgets the clear.
func TestShareHandshakeClearsDeadlineBeforeStreaming(t *testing.T) {
	const timeout = 250 * time.Millisecond
	withShareHandshakeTimeout(t, timeout)

	want := bytes.Repeat([]byte("lantern"), 1<<17) // 1 MiB, larger than one stream window
	root := t.TempDir()
	path := filepath.Join(root, "large.bin")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	const code = "clear-deadline"
	requester, pi := shareHosts(t, code, path)
	s := openShareStream(t, requester, pi)

	challenge := make([]byte, crypto.ChallengeBytes)
	if _, err := crand.Read(challenge); err != nil {
		t.Fatal(err)
	}
	proof, err := crypto.AuthProof(code, challenge, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(s).Encode(protocol.TransferRequest{Challenge: challenge, Proof: proof}); err != nil {
		t.Fatal(err)
	}

	key, err := crypto.DeriveKey(code)
	if err != nil {
		t.Fatal(err)
	}
	er, err := crypto.NewEncryptedReader(s, key)
	if err != nil {
		t.Fatal(err)
	}
	var meta FileMeta
	if err := protocol.ReadMetadata(er, &meta); err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if meta.Size != int64(len(want)) {
		t.Fatalf("metadata size = %d, want %d", meta.Size, len(want))
	}

	// Stop reading long enough for the handshake deadline to pass. The server
	// fills the stream window and blocks mid-body; if it kept the deadline,
	// that blocked write fails and the body is truncated.
	time.Sleep(4 * timeout)
	if err := s.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(er)
	if err != nil {
		t.Fatalf("read body after the handshake deadline: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("body was truncated after the handshake deadline: got %d bytes, want %d", len(got), len(want))
	}
}
