package daemon

// Tests for per-peer access: tiers, and the per-peer writable roots that may
// only narrow the device's own.
//
// The property under test throughout is that pairing no longer implies
// capability. A pairing is a long-lived, one-directional grant, so what it
// confers has to be a deliberate choice that survives a restart.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTierDefaultsToRead(t *testing.T) {
	// An entry written before tiers existed must acquire a defined, safe
	// meaning rather than failing to load or defaulting to write.
	for _, v := range []string{"", "   "} {
		got, err := ParseTier(v)
		if err != nil {
			t.Fatalf("ParseTier(%q) failed: %v", v, err)
		}
		if got != TierRead {
			t.Errorf("ParseTier(%q) = %q, want %q", v, got, TierRead)
		}
		if got.CanWrite() {
			t.Errorf("the default tier must not allow writes: %q", got)
		}
	}
}

func TestParseTierRejectsUnknown(t *testing.T) {
	if _, err := ParseTier("full-access"); err == nil {
		t.Fatal("an unknown tier must be rejected")
	} else if !strings.Contains(err.Error(), "none") {
		t.Errorf("the error should list the valid tiers: %v", err)
	}
}

func TestTierOrderingAndCapabilities(t *testing.T) {
	if !TierReadWrite.AtLeast(TierRead) {
		t.Error("read-write must include read")
	}
	if !TierRead.AtLeast(TierNone) {
		t.Error("read must include none")
	}
	if TierRead.AtLeast(TierReadWrite) {
		t.Error("read must not include read-write")
	}
	if TierNone.CanRead() || TierNone.CanWrite() {
		t.Error("tier none must grant nothing")
	}
	if !TierRead.CanRead() || TierRead.CanWrite() {
		t.Error("tier read must read and not write")
	}
	if !TierReadWrite.CanRead() || !TierReadWrite.CanWrite() {
		t.Error("tier read-write must read and write")
	}
}

func TestAddDefaultsToReadOnly(t *testing.T) {
	s, err := NewTrustStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.Add(TrustSpec{PeerID: "peer-1", Alias: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Tier != TierRead {
		t.Fatalf("a newly paired device got tier %q, want %q", e.Tier, TierRead)
	}
	if s.CanWrite("peer-1") {
		t.Error("a newly paired device must not be able to write")
	}
}

func TestAddRejectsUnknownTier(t *testing.T) {
	s, _ := NewTrustStore(t.TempDir())
	if _, err := s.Add(TrustSpec{PeerID: "peer-1", Tier: "supervised"}); err == nil {
		t.Fatal("an unknown tier must be refused")
	}
	if s.Trusted("peer-1") {
		t.Error("a refused pairing must not be stored")
	}
}

// A pairing written before tiers existed loads as read-only rather than as
// full access or as broken.
func TestLegacyEntriesLoadAsReadOnly(t *testing.T) {
	dir := t.TempDir()
	raw := `[{"peer_id":"peer-legacy","alias":"old-nas","added_at":"2020-01-01T00:00:00Z"}]`
	if err := os.WriteFile(filepath.Join(dir, "trusted_peers.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewTrustStore(dir)
	if err != nil {
		t.Fatalf("a pre-tiers trust store must still load: %v", err)
	}
	if !s.Trusted("peer-legacy") {
		t.Fatal("the pairing must survive the upgrade")
	}
	if got := s.Tier("peer-legacy"); got != TierRead {
		t.Fatalf("a legacy entry loaded as tier %q, want %q", got, TierRead)
	}
	if s.CanWrite("peer-legacy") {
		t.Error("a legacy entry must not silently gain write access")
	}
}

func TestSetTierChangesExistingPairing(t *testing.T) {
	s, _ := NewTrustStore(t.TempDir())
	if _, err := s.Add(TrustSpec{PeerID: "peer-1", Alias: "laptop"}); err != nil {
		t.Fatal(err)
	}
	// Record an address so we can prove the update did not clobber it.
	addr := "/ip4/192.168.1.11/tcp/41001"
	if err := s.UpdateAddrs("peer-1", []string{addr}); err != nil {
		t.Fatal(err)
	}

	e, err := s.SetTier("peer-1", string(TierReadWrite))
	if err != nil {
		t.Fatal(err)
	}
	if e.Tier != TierReadWrite {
		t.Fatalf("tier = %q", e.Tier)
	}
	if e.Alias != "laptop" {
		t.Errorf("changing a tier must keep the alias, got %q", e.Alias)
	}
	if len(e.Addrs) != 1 || e.Addrs[0] != addr {
		t.Errorf("changing a tier must keep cached addresses, got %v", e.Addrs)
	}
}

func TestSetTierOnUnknownPeerFails(t *testing.T) {
	s, _ := NewTrustStore(t.TempDir())
	if _, err := s.SetTier("nobody", string(TierReadWrite)); err == nil {
		t.Fatal("changing the tier of an unpaired device must fail")
	}
}

// Tier none keeps the pairing but grants nothing. This is the "keep it paired
// but inert" case, and it must be revocable again.
func TestTierNoneIsPairedButInert(t *testing.T) {
	s, _ := NewTrustStore(t.TempDir())
	if _, err := s.Add(TrustSpec{PeerID: "peer-1", Tier: string(TierNone)}); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted("peer-1") {
		t.Error("a device at tier none must still be paired")
	}
	if s.CanRead("peer-1") || s.CanWrite("peer-1") {
		t.Error("a device at tier none must grant nothing")
	}
	if _, err := s.SetTier("peer-1", string(TierRead)); err != nil {
		t.Fatal(err)
	}
	if !s.CanRead("peer-1") {
		t.Error("raising the tier back must work")
	}
}

func TestTierSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewTrustStore(dir)
	if _, err := s.Add(TrustSpec{PeerID: "peer-1", Alias: "nas", Tier: string(TierReadWrite)}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Tier("peer-1"); got != TierReadWrite {
		t.Fatalf("after restart the tier is %q, want %q", got, TierReadWrite)
	}
	if !reopened.CanWrite("peer-1") {
		t.Error("write access must survive a restart, or pairing silently stops working")
	}
}

// Per-peer roots may narrow the device's writable dirs and never widen them.
// This is the property that makes it safe for a pairing record to carry paths
// at all: a hand-edited or synced-in record cannot name its way past
// --writable-dirs.
func TestPerPeerRootsMayOnlyNarrow(t *testing.T) {
	deviceRoot := t.TempDir()
	narrow := filepath.Join(deviceRoot, "inbox")
	if err := os.MkdirAll(narrow, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	s, _ := NewTrustStore(t.TempDir())

	// Inside: accepted.
	e, err := s.Add(TrustSpec{
		PeerID: "peer-ok", WritableRoots: []string{narrow}, DeviceRoots: []string{deviceRoot},
	})
	if err != nil {
		t.Fatalf("a root inside the writable dirs must be accepted: %v", err)
	}
	if len(e.WritableRoots) != 1 {
		t.Fatalf("roots = %v", e.WritableRoots)
	}

	// Outside: refused.
	_, err = s.Add(TrustSpec{
		PeerID: "peer-bad", WritableRoots: []string{outside}, DeviceRoots: []string{deviceRoot},
	})
	if err == nil {
		t.Fatal("a root outside the writable dirs must be refused")
	}
	if !strings.Contains(err.Error(), "narrowed") {
		t.Errorf("the refusal should explain the direction of the limit: %v", err)
	}
	if s.Trusted("peer-bad") {
		t.Error("a refused root must not leave a pairing behind")
	}
}

// A shared prefix must not pass as containment: /srv/inbox-evil is not inside
// /srv/inbox.
func TestPerPeerRootsRejectSharedPrefix(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "inbox")
	sibling := filepath.Join(parent, "inbox-evil")
	for _, d := range []string{root, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := NewTrustStore(t.TempDir())
	if _, err := s.Add(TrustSpec{
		PeerID: "peer-1", WritableRoots: []string{sibling}, DeviceRoots: []string{root},
	}); err == nil {
		t.Fatal("a shared prefix must not count as containment")
	}
}

func TestPerPeerRootsRefusedWhenDeviceHasNone(t *testing.T) {
	s, _ := NewTrustStore(t.TempDir())
	_, err := s.Add(TrustSpec{
		PeerID: "peer-1", WritableRoots: []string{t.TempDir()}, DeviceRoots: nil,
	})
	if err == nil {
		t.Fatal("a device with no writable dirs cannot host a per-peer root")
	}
}

func TestSetWritableRootsClears(t *testing.T) {
	deviceRoot := t.TempDir()
	inbox := filepath.Join(deviceRoot, "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	s, _ := NewTrustStore(t.TempDir())
	if _, err := s.Add(TrustSpec{
		PeerID: "peer-1", WritableRoots: []string{inbox}, DeviceRoots: []string{deviceRoot},
	}); err != nil {
		t.Fatal(err)
	}
	e, err := s.SetWritableRoots("peer-1", nil, []string{deviceRoot})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.WritableRoots) != 0 {
		t.Fatalf("clearing should leave no per-peer roots, got %v", e.WritableRoots)
	}
}

func TestWriteScopeFallsBackToDeviceRoots(t *testing.T) {
	deviceRoots := []string{"/srv/inbox"}
	// No per-peer restriction: the device's own roots apply.
	e := TrustEntry{PeerID: "p", Tier: TierReadWrite}
	if got := e.WriteScope(deviceRoots); len(got) != 1 || got[0] != "/srv/inbox" {
		t.Errorf("WriteScope = %v, want the device roots", got)
	}
	// A restriction wins over the device's own roots.
	e.WritableRoots = []string{"/srv/inbox/narrow"}
	if got := e.WriteScope(deviceRoots); len(got) != 1 || got[0] != "/srv/inbox/narrow" {
		t.Errorf("WriteScope = %v, want the per-peer root", got)
	}
}

func TestTierOfUnpairedDeviceIsNone(t *testing.T) {
	s, _ := NewTrustStore(t.TempDir())
	// An unpaired device must not default to some capability.
	if got := s.Tier("stranger"); got != TierNone {
		t.Fatalf("an unpaired device read as tier %q, want %q", got, TierNone)
	}
	if s.CanRead("stranger") {
		t.Error("an unpaired device must not read")
	}
}

// Re-pairing keeps the original added date, so granting a tier back does not
// read as a brand new pairing and reset the record.
func TestRepairingKeepsAddedDateAndAddrs(t *testing.T) {
	s, _ := NewTrustStore(t.TempDir())
	first, err := s.Add(TrustSpec{PeerID: "peer-1", Alias: "nas"})
	if err != nil {
		t.Fatal(err)
	}
	addr := "/ip4/10.0.0.5/tcp/41001"
	if err := s.UpdateAddrs("peer-1", []string{addr}); err != nil {
		t.Fatal(err)
	}
	second, err := s.Add(TrustSpec{PeerID: "peer-1", Alias: "nas-renamed", Tier: string(TierReadWrite)})
	if err != nil {
		t.Fatal(err)
	}
	if second.AddedAt != first.AddedAt {
		t.Errorf("re-pairing changed added_at: %q -> %q", first.AddedAt, second.AddedAt)
	}
	if len(second.Addrs) != 1 {
		t.Errorf("re-pairing dropped cached addresses: %v", second.Addrs)
	}
	if second.Alias != "nas-renamed" || second.Tier != TierReadWrite {
		t.Errorf("re-pairing did not apply the new policy: %+v", second)
	}
}

// The store addresses devices by peer ID. Alias resolution belongs to the HTTP
// layer, which is where a human or an agent supplies a name instead.
func TestStoreAddressesDevicesByPeerID(t *testing.T) {
	s, _ := NewTrustStore(t.TempDir())
	if _, err := s.Add(TrustSpec{PeerID: "peer-1", Alias: "Laptop"}); err != nil {
		t.Fatal(err)
	}
	if !s.Remove("peer-1") {
		t.Fatal("a peer ID should unpair its device")
	}
	if s.Trusted("peer-1") {
		t.Error("the device should be gone")
	}
	if s.Remove("peer-1") {
		t.Error("removing twice should report false")
	}
}

// Aliases are case-insensitive everywhere else in the API, so they are here
// too: an operator typing "Laptop" and one typing "laptop" mean one device.
func TestTrustRefResolvesAliasCaseInsensitively(t *testing.T) {
	h := newTrustHandler(t, TrustEntry{PeerID: "peer-1", Alias: "Laptop"})

	for _, ref := range []string{"Laptop", "laptop", "LAPTOP", "peer-1"} {
		got, err := h.resolveTrustRef(ref)
		if err != nil {
			t.Fatalf("resolveTrustRef(%q): %v", ref, err)
		}
		if got != "peer-1" {
			t.Errorf("resolveTrustRef(%q) = %q, want peer-1", ref, got)
		}
	}

	// An unknown ref is passed through so the store reports "not found".
	got, err := h.resolveTrustRef("nobody")
	if err != nil || got != "nobody" {
		t.Errorf("resolveTrustRef(nobody) = %q %v, want it passed through", got, err)
	}
	if _, err := h.resolveTrustRef("   "); err == nil {
		t.Error("an empty ref must be refused")
	}
}
