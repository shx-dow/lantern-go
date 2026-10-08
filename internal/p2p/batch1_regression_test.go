package p2p

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/protocol"
)

// TestReadFSRejectsImplausibleContentLength pins the client-side bound on the
// length a peer claims in its response.
//
// The serving side clamps its own reply to MaxReadLength, but that is the peer's
// promise rather than an invariant of the stream: a peer is free to answer with
// any int64 it likes. Before the check in ReadFS, a negative value panicked
// inside make ("makeslice: len out of range") and an enormous one allocated on
// the peer's say-so. Both were reachable by any peer that could open a stream.
func TestReadFSRejectsImplausibleContentLength(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes int64
	}{
		{"negative", -1},
		{"absurdly large", 1 << 40},
		{"one past the cap", MaxReadLength + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()

			// A sender that answers with a length no honest Lantern would send.
			sender := fsHost(t)
			sender.Host.SetStreamHandler(FSProtocolID, func(s network.Stream) {
				defer s.Close()
				var req FSRequest
				_ = protocol.ReadMetadata(s, &req)
				_ = protocol.WriteMetadata(s, FSResponse{
					Entry: &FSEntry{Name: "a.txt", Size: tc.bytes},
					Bytes: tc.bytes,
				})
			})

			requester := fsHost(t)
			pi := peer.AddrInfo{ID: sender.Host.ID(), Addrs: sender.Host.Addrs()}
			_, _, data, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{
				Op:   OpRead,
				Path: filepath.Join(root, "a.txt"),
				// Ask for the maximum a real peer may send, so the only thing
				// under test is the peer's answer rather than our request.
				Length: MaxReadLength,
			})
			if err == nil {
				t.Fatalf("a response claiming %d bytes must be refused; got %d bytes of data", tc.bytes, len(data))
			}
			if data != nil {
				t.Fatalf("a refused response must not return data, got %d bytes", len(data))
			}
			if !strings.Contains(err.Error(), "implausible content length") {
				t.Fatalf("the refusal should name the bad length, got: %v", err)
			}
		})
	}
}

// TestReadFSAcceptsLengthAtCap is the other side of the same bound: a peer
// answering with exactly MaxReadLength is legitimate and must still work, so the
// new check cannot be tightened into an off-by-one that refuses real reads.
func TestReadFSAcceptsLengthAtCap(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("x", 512)

	sender := fsHost(t)
	sender.Host.SetStreamHandler(FSProtocolID, func(s network.Stream) {
		defer s.Close()
		var req FSRequest
		_ = protocol.ReadMetadata(s, &req)
		_ = protocol.WriteMetadata(s, FSResponse{
			Entry: &FSEntry{Name: "big.txt"},
			Bytes: int64(len(content)),
		})
		_, _ = s.Write([]byte(content))
	})

	requester := fsHost(t)
	pi := peer.AddrInfo{ID: sender.Host.ID(), Addrs: sender.Host.Addrs()}
	_, _, data, _, err := requester.ReadFS(fsCtx(t), pi, FSRequest{
		Op:     OpRead,
		Path:   filepath.Join(root, "big.txt"),
		Length: MaxReadLength,
	})
	if err != nil {
		t.Fatalf("a response at the cap must be accepted: %v", err)
	}
	if string(data) != content {
		t.Fatalf("content = %q, want %q", data, content)
	}
}

// TestFSWriteAllowedWithNoSharedRoots pins the gate that lets a push through on a
// device the operator gave writable roots but no shared roots.
//
// Reads must stay refused there, because ReadRoots documents an empty list as a
// refusal. What must not happen is the write being refused for that same reason:
// writes are authorised by the writable roots and the peer's tier, and serveFS
// used to consult the read roots before it ever reached grantWrite. This is the
// configuration in which a read-write peer was permanently unable to push.
func TestFSWriteAllowedWithNoSharedRoots(t *testing.T) {
	root := t.TempDir()
	provider := fsHost(t)
	requester := fsHost(t)

	// Paired, but the operator shared nothing, so every read is refused...
	provider.SetReadAccess(func(string) []string { return nil })
	// ...while this peer is granted the writable root and nothing else.
	provider.SetWriteAccess(grantEveryone(writePolicy(root, 0)))
	provider.RegisterFSHandler()

	pi := peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
	dst := filepath.Join(root, "landed.txt")
	if _, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("ok"), false); err != nil {
		t.Fatalf("a peer granted write access must be able to push when no shared roots are configured: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("the push did not land: %v", err)
	}
}

// TestFSWriteDeniedWithNoTrustStore is the other side of the same gate: a node
// with no pairing resolver at all has no trust store, so it must refuse every
// write no matter what the write grant says. A grant that ignores its peer
// argument must not be able to hand access to a stranger.
//
// This is the property TestFSWriteDeniedWhenUnpaired and
// TestAuditUnpairedPeerCannotProbeWriteConfig already assert through a message
// match; this pins the mechanism, which is trustConfigured rather than a
// root count.
func TestFSWriteDeniedWithNoTrustStore(t *testing.T) {
	root := t.TempDir()
	provider := fsHost(t)
	provider.SetReadAccess(nil)
	provider.SetWriteAccess(grantEveryone(writePolicy(root, 0)))
	provider.RegisterFSHandler()

	requester := fsHost(t)
	pi := peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
	dst := filepath.Join(root, "sneaky.txt")
	_, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("nope"), false)
	if err == nil {
		t.Fatal("a node with no trust store must refuse every write")
	}
	if !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("the refusal should be the pairing gate, got: %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatal("a refused write must not create a file")
	}
}

// TestTrustConfiguredDistinguishesEmptyFromAbsent pins the distinction the write
// gate rests on. A resolver that returns no roots is a peer with no read access;
// a nil resolver is a node with no trust store at all. Both make rootsFor return
// nothing, and conflating them is what broke pushes on a device with writable
// roots and no shared roots.
func TestTrustConfiguredDistinguishesEmptyFromAbsent(t *testing.T) {
	n := &Node{}
	if n.trustConfigured() {
		t.Fatal("a node that never had SetReadAccess called has no trust store")
	}

	n.SetReadAccess(func(string) []string { return nil })
	if !n.trustConfigured() {
		t.Fatal("an installed resolver is a trust store, even if it grants this peer nothing")
	}
	if got := n.rootsFor("some-peer"); len(got) != 0 {
		t.Fatalf("rootsFor = %v, want empty", got)
	}
}
