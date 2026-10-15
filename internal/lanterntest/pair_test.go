package lanterntest

import (
	"os"
	"path/filepath"
	"testing"
)

// The helper is only worth having if two devices it brings up can actually
// move a file. This is that assertion, and it is in this package rather than
// duplicated at each call site: if the helper's wiring regresses, one test says
// so instead of every end-to-end test failing at once for the same reason.
func TestAPairMovesAFileBetweenTwoDevices(t *testing.T) {
	shared := t.TempDir()
	const content = "the quick brown fox\njumps over the lazy dog\n"
	if err := os.WriteFile(filepath.Join(shared, "notes.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewPair(t, shared)
	out := t.TempDir()

	session, peer, err := p.Sender.Lantern.ShareSession(Ctx(t), filepath.Join(shared, "notes.txt"))
	if err != nil {
		t.Fatalf("the sender could not share the file: %v", err)
	}
	defer session.Close()

	if peer.Code == "" {
		t.Fatal("the sender minted no share code")
	}
	p.BridgeDiscovery(t, peer.Code)

	received, _, err := p.Receiver.Lantern.ReceiveSession(Ctx(t), peer.Code, out)
	if err != nil {
		t.Fatalf("the receiver could not redeem the code: %v", err)
	}
	// The receiver's events close when the transfer ends, so this is the wait.
	for range received.Events() {
	}
	received.Close()

	got, err := os.ReadFile(filepath.Join(out, "notes.txt"))
	if err != nil {
		t.Fatalf("the file did not arrive: %v", err)
	}
	if string(got) != content {
		t.Errorf("arrived as %q, want %q", got, content)
	}
}

// Each device keeps its own data directory, which is what gives them separate
// identities. Two devices sharing one would be the same device, and a test that
// accidentally collapsed them would pass while testing nothing.
func TestEachDeviceHasItsOwnIdentity(t *testing.T) {
	p := NewPair(t, t.TempDir())

	if p.Sender.ID == p.Receiver.ID {
		t.Fatalf("both devices report the same peer ID %s", p.Sender.ID)
	}
	if p.Sender.DataDir == p.Receiver.DataDir {
		t.Fatal("both devices share a data directory, so they share an identity")
	}
}

// A share code authorises a transfer on its own. The file does not have to sit
// in a shared root, and the holder does not have to be paired, because
// presenting the code is what authorises the transfer — CONTEXT.md calls it "a
// capability in its own right, independent of any tier", and SECURITY.md
// carves the same case out of its vulnerability list.
//
// The first draft of this test asserted the opposite, that a device serving no
// roots hands over nothing. It failed, and the code was right: that assertion
// would have asked the product to refuse the transfer it is designed to make.
func TestAShareCodeAuthorisesOnItsOwn(t *testing.T) {
	outside := t.TempDir()
	path := filepath.Join(outside, "gift.txt")
	const content = "served because the code was presented"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// No shared roots at all, and the two devices were never paired.
	p := NewPair(t)

	session, peer, err := p.Sender.Lantern.ShareSession(Ctx(t), path)
	if err != nil {
		t.Fatalf("sharing a file outside every shared root should still mint a code: %v", err)
	}
	defer session.Close()
	p.BridgeDiscovery(t, peer.Code)

	out := t.TempDir()
	received, _, err := p.Receiver.Lantern.ReceiveSession(Ctx(t), peer.Code, out)
	if err != nil {
		t.Fatalf("a valid share code must be honoured regardless of roots: %v", err)
	}
	defer received.Close()
	for range received.Events() {
	}

	got, err := os.ReadFile(filepath.Join(out, "gift.txt"))
	if err != nil {
		t.Fatalf("the file did not arrive: %v", err)
	}
	if string(got) != content {
		t.Errorf("arrived as %q, want %q", got, content)
	}
}
