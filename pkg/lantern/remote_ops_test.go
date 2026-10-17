package lantern

// Tests for the paired-device operations.
//
// These five are the whole privileged surface: read, stat, list, push, and
// reachability against a device the caller has been paired with. Every one of
// them was at zero coverage, because the tests that exercised this behaviour
// drove the fs protocol directly at the p2p layer and never came back up
// through these wrappers.
//
// What that left untested is not the protocol, which internal/p2p covers, but
// the seam: that each wrapper resolves a peer reference, sends the right op,
// and returns what the protocol said. A wrapper that sent the wrong op, or
// dropped the entry, or reported success with nothing stored would have passed
// every test in the repository.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/p2p"
)

// remotePair is a caller and the paired device it addresses. The caller holds
// root, the device shares it back, and the two are connected so that
// resolvePeer can find the other end.
type remotePair struct {
	caller   *Lantern
	device   *Lantern
	ref      string
	root     string
	writable string
}

func newRemotePair(t *testing.T, name, content string) remotePair {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "share")
	writable := filepath.Join(dir, "inbox")
	for _, d := range []string{root, writable} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	caller := newTestNode(t, dir, "caller")
	device := newTestNode(t, dir, "device")

	// The device shares root for reading and inbox for writing, to the caller.
	callerID := caller.Node().Host.ID().String()
	device.Node().SetReadAccess(func(string) []string { return []string{root} })
	device.Node().SetWriteAccess(func(peerID string) (*p2p.WritePolicy, error) {
		if peerID != callerID {
			return nil, fmt.Errorf("peer %s is not paired for writes", peerID)
		}
		return &p2p.WritePolicy{Mode: p2p.WriteSharedRoots, Roots: []string{writable}}, nil
	})

	addr := peer.AddrInfo{ID: device.Node().Host.ID(), Addrs: device.Node().Host.Addrs()}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := caller.Node().Host.Connect(ctx, addr); err != nil {
		t.Fatalf("the caller could not reach the device: %v", err)
	}

	return remotePair{
		caller: caller, device: device,
		ref:      addr.ID.String(),
		root:     root,
		writable: writable,
	}
}

func remoteCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestReadRemoteReturnsTheBytesAndTheEntry(t *testing.T) {
	const content = "the bytes that must land"
	p := newRemotePair(t, "notes.txt", content)
	path := filepath.Join(p.root, "notes.txt")

	res, err := p.caller.ReadRemote(remoteCtx(t), p.ref, path, 0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(res.Data) != content {
		t.Fatalf("read returned %q", res.Data)
	}
	if !res.EOF {
		t.Fatal("a whole-file read did not report the end of the file")
	}
	if res.Entry.Size != int64(len(content)) {
		t.Fatalf("entry size %d, want %d", res.Entry.Size, len(content))
	}
	if res.Entry.Name != "notes.txt" {
		t.Fatalf("entry name %q", res.Entry.Name)
	}
	if res.Offset != 0 {
		t.Fatalf("result claims offset %d", res.Offset)
	}
}

// A range read is the operation Fetch is built on, so a wrapper that ignored
// offset or length would still pass every whole-file test.
func TestReadRemoteHonoursOffsetAndLength(t *testing.T) {
	const content = "0123456789"
	p := newRemotePair(t, "notes.txt", content)
	path := filepath.Join(p.root, "notes.txt")

	res, err := p.caller.ReadRemote(remoteCtx(t), p.ref, path, 3, 4)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(res.Data) != "3456" {
		t.Fatalf("range read returned %q", res.Data)
	}
	if res.EOF {
		t.Fatal("a four-byte range out of ten reported the end of the file")
	}
	if res.Offset != 3 {
		t.Fatalf("result claims offset %d", res.Offset)
	}
}

// ReadRemote must not be a way to read what the device has not agreed to
// share. The device grants root; everything else is outside the pairing.
func TestReadRemoteRefusesOutsideTheSharedRoot(t *testing.T) {
	p := newRemotePair(t, "notes.txt", "inside")
	// One level above the shared root: readable on disk, outside the pairing.
	if err := os.WriteFile(filepath.Join(filepath.Dir(p.root), "outside.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(filepath.Dir(p.root), "outside.txt")

	if _, err := p.caller.ReadRemote(remoteCtx(t), p.ref, elsewhere, 0, 0); err == nil {
		t.Fatal("a read outside the shared root succeeded")
	}
}

func TestStatRemoteReturnsMetadataWithoutTheBytes(t *testing.T) {
	const content = "the bytes that must land"
	p := newRemotePair(t, "notes.txt", content)
	path := filepath.Join(p.root, "notes.txt")

	entry, err := p.caller.StatRemote(remoteCtx(t), p.ref, path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if entry.Name != "notes.txt" {
		t.Fatalf("name %q", entry.Name)
	}
	if entry.Size != int64(len(content)) {
		t.Fatalf("size %d, want %d", entry.Size, len(content))
	}
	if entry.IsDir {
		t.Fatal("a file was reported as a directory")
	}
}

func TestStatRemoteRefusesAMissingPath(t *testing.T) {
	p := newRemotePair(t, "notes.txt", "here")
	if _, err := p.caller.StatRemote(remoteCtx(t), p.ref, filepath.Join(p.root, "absent.txt")); err == nil {
		t.Fatal("stat of a missing path succeeded")
	}
}

func TestListRemoteEntriesListsOneLevel(t *testing.T) {
	p := newRemotePair(t, "notes.txt", "one")
	for _, extra := range []string{"second.txt", "third.txt"} {
		if err := os.WriteFile(filepath.Join(p.root, extra), []byte(extra), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := p.caller.ListRemoteEntries(remoteCtx(t), p.ref, p.root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := make(map[string]bool, len(entries))
	for _, e := range entries {
		got[e.Name] = true
	}
	for _, want := range []string{"notes.txt", "second.txt", "third.txt"} {
		if !got[want] {
			t.Fatalf("listing omitted %s: got %v", want, got)
		}
	}
	// One level only: a file in the directory must not drag its children in.
	if err := os.MkdirAll(filepath.Join(p.root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.root, "sub", "buried.txt"), []byte("deep"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err = p.caller.ListRemoteEntries(remoteCtx(t), p.ref, p.root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, e := range entries {
		if e.Name == "buried.txt" {
			t.Fatal("a one-level listing recursed into a subdirectory")
		}
	}
}

// An empty dir lists the device's shared roots, which is how a caller learns
// where it is allowed to read in the first place.
func TestListRemoteEntriesWithNoDirListsTheSharedRoots(t *testing.T) {
	p := newRemotePair(t, "notes.txt", "one")
	entries, err := p.caller.ListRemoteEntries(remoteCtx(t), p.ref, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("listing with no directory returned nothing, not even the shared root")
	}
	var found bool
	for _, e := range entries {
		if e.Name == filepath.Base(p.root) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the shared root was not listed: %v", entries)
	}
}

func TestPushRemoteStoresTheContentOnTheDevice(t *testing.T) {
	p := newRemotePair(t, "notes.txt", "")
	const content = "pushed from the other device"
	dest := filepath.Join(p.writable, "pushed.txt")

	res, err := p.caller.PushRemote(remoteCtx(t), p.ref, dest, []byte(content), false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if res.Bytes != int64(len(content)) {
		t.Fatalf("push reported %d bytes, sent %d", res.Bytes, len(content))
	}
	if res.SHA256 == "" {
		t.Fatal("push reported no digest")
	}

	stored, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("the pushed file is not on the device: %v", err)
	}
	if string(stored) != content {
		t.Fatalf("the device stored %q", stored)
	}
	if res.Entry.Name != "pushed.txt" {
		t.Fatalf("entry name %q", res.Entry.Name)
	}
}

// Push is refused or accepted by the remote, and a caller must not hear
// "pushed" when nothing was stored.
func TestPushRemoteRefusesOutsideTheWritableRoot(t *testing.T) {
	p := newRemotePair(t, "notes.txt", "")
	elsewhere := filepath.Join(p.root, "sneaked.txt")

	if _, err := p.caller.PushRemote(remoteCtx(t), p.ref, elsewhere, []byte("nope"), true); err == nil {
		t.Fatal("a push outside the writable root succeeded")
	}
	if _, err := os.Stat(elsewhere); err == nil {
		t.Fatal("a refused push left a file behind")
	}
}

// A second push to the same path must be told so rather than silently
// replacing what is there.
func TestPushRemoteRefusesToOverwriteUnlessAsked(t *testing.T) {
	p := newRemotePair(t, "notes.txt", "")
	dest := filepath.Join(p.writable, "pushed.txt")

	if _, err := p.caller.PushRemote(remoteCtx(t), p.ref, dest, []byte("first"), false); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if _, err := p.caller.PushRemote(remoteCtx(t), p.ref, dest, []byte("second"), false); err == nil {
		t.Fatal("a second push overwrote a file without being asked to")
	}
	if stored, _ := os.ReadFile(dest); string(stored) != "first" {
		t.Fatalf("the refused push replaced the file: %q", stored)
	}

	if _, err := p.caller.PushRemote(remoteCtx(t), p.ref, dest, []byte("third"), true); err != nil {
		t.Fatalf("overwriting push: %v", err)
	}
	if stored, _ := os.ReadFile(dest); string(stored) != "third" {
		t.Fatalf("an overwriting push did not replace the file: %q", stored)
	}
}

func TestProbePeerReachesAPairedDevice(t *testing.T) {
	p := newRemotePair(t, "notes.txt", "")
	if err := p.caller.ProbePeer(remoteCtx(t), p.ref); err != nil {
		t.Fatalf("probe: %v", err)
	}
}

// A probe must be able to fail. Without this, a wrapper that swallowed the
// dial error would pass, and doctor would report every peer as reachable.
// gonePeer returns the address of a real host that has been switched off.
//
// The peer ID has to be well-formed to get past resolvePeer's decoding, so it
// comes from a real host rather than a plausible-looking string. Two things
// are then separately testable: a peer that is known but gone, and a peer that
// is not known at all.
func gonePeer(t *testing.T) peer.AddrInfo {
	t.Helper()
	// A real listen address, so the peer has addresses a dial can be aimed at.
	// A host that never listened would fail in resolvePeer instead, and the
	// dial would never be reached -- which is a different test.
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	info := peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	return info
}

// A probe must be able to fail at the dial.
//
// The peer here is real and its addresses are known, so resolvePeer succeeds
// and Host.Connect is what has to fail. That distinction is the point: a
// wrapper that swallowed the dial error would report every absent device as
// reachable, and doctor would become a machine for lying about that.
func TestProbePeerFailsWhenAPairedDeviceIsGone(t *testing.T) {
	p := newRemotePair(t, "notes.txt", "")
	gone := gonePeer(t)

	// Known to the peerstore, so resolvePeer hands the addresses over and the
	// dial is what fails.
	p.caller.Node().Host.Peerstore().AddAddrs(gone.ID, gone.Addrs, time.Minute)

	if err := p.caller.ProbePeer(remoteCtx(t), gone.ID.String()); err == nil {
		t.Fatal("probing a device that has been switched off succeeded")
	}
}

// A peer the caller has never heard of is refused before any dial, with a
// message that says what to do about it.
func TestProbePeerFailsForAPeerNotOnTheNetwork(t *testing.T) {
	p := newRemotePair(t, "notes.txt", "")
	gone := gonePeer(t)

	err := p.caller.ProbePeer(remoteCtx(t), gone.ID.String())
	if err == nil {
		t.Fatal("probing a peer that is not on the network succeeded")
	}
	if !strings.Contains(err.Error(), "not on this network") {
		t.Fatalf("the refusal does not say the peer is not on the network: %v", err)
	}
}

func TestProbePeerRejectsAMalformedPeerID(t *testing.T) {
	p := newRemotePair(t, "notes.txt", "")
	if err := p.caller.ProbePeer(remoteCtx(t), "not-a-peer-id"); err == nil {
		t.Fatal("probing a malformed peer reference succeeded")
	}
}

// Every one of these resolves a peer reference first, so a bad reference must
// fail before any request goes out.
func TestTheRemoteOperationsRejectAMalformedPeerReference(t *testing.T) {
	p := newRemotePair(t, "notes.txt", "x")
	const bad = "not-a-peer-id"
	ctx := remoteCtx(t)
	root := filepath.Join(p.root, "notes.txt")

	if _, err := p.caller.ReadRemote(ctx, bad, root, 0, 0); err == nil {
		t.Error("ReadRemote accepted a malformed peer reference")
	}
	if _, err := p.caller.StatRemote(ctx, bad, root); err == nil {
		t.Error("StatRemote accepted a malformed peer reference")
	}
	if _, err := p.caller.ListRemoteEntries(ctx, bad, p.root); err == nil {
		t.Error("ListRemoteEntries accepted a malformed peer reference")
	}
	if _, err := p.caller.PushRemote(ctx, bad, filepath.Join(p.writable, "x"), []byte("x"), true); err == nil {
		t.Error("PushRemote accepted a malformed peer reference")
	}
	if err := p.caller.ProbePeer(ctx, bad); err == nil {
		t.Error("ProbePeer accepted a malformed peer reference")
	}
}
