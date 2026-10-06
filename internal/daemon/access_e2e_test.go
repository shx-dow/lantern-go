package daemon

// End-to-end tests for per-peer access over a real libp2p connection.
//
// The point is that a tier is enforced on the stream handler, where a paired
// peer actually arrives. A tier checked only in the HTTP layer would pass every
// store test and still let a peer read or write directly.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shx-dow/lantern-go/internal/p2p"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

// pair is a serving device and a client that dials it, both real
// lantern.Lantern instances.
type pair struct {
	// client is the device making requests.
	client *lantern.Lantern
	// server is the serving device's peer ID: what the client addresses.
	server string
	// daemon is the serving device's daemon, for trust changes.
	daemon *Daemon
	// paired is the client as the trust store knows it.
	paired string
	// cleanup tears both down.
	cleanup func()
}

// readFile asks the client to read a path from the serving device.
func (p pair) readFile(path string) ([]byte, error) {
	res, err := p.client.ReadRemote(pushCtx(), p.server, path, 0, 0)
	return res.Data, err
}

// push asks the client to write content onto the serving device.
func (p pair) push(remotePath, content string) error {
	_, err := p.client.PushRemote(pushCtx(), p.server, remotePath, []byte(content), false)
	return err
}

func pushCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	// A generous leak is fine for tests; the context is short enough that a
	// stuck dial still ends the test.
	_ = cancel
	return ctx
}

// tierPair brings up a serving daemon with a trust store and a client that
// dials it. Handlers go on the serving daemon's own node, because that is the
// host a paired peer connects to; registering them on a separately constructed
// node would leave the real one ungated.
func tierPair(t *testing.T, root string, deviceRoots []string, writesEnabled bool) pair {
	t.Helper()
	base := t.TempDir()

	ln, err := lantern.New(lantern.Config{Port: 0, DataDir: filepath.Join(base, "server")})
	if err != nil {
		t.Fatal(err)
	}
	client, err := lantern.New(lantern.Config{Port: 0, DataDir: filepath.Join(base, "client")})
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	serverNode, clientNode := ln.Node(), client.Node()
	if serverNode == nil || serverNode.Host == nil || clientNode == nil || clientNode.Host == nil {
		t.Fatal("both nodes must expose a host")
	}

	store, err := NewTrustStore(base)
	if err != nil {
		t.Fatal(err)
	}
	clientID := clientNode.Host.ID().String()
	if _, err := store.Add(TrustSpec{PeerID: clientID, Alias: "laptop"}); err != nil {
		t.Fatal(err)
	}

	d := New(ln)
	d.Trust = store
	d.SharedDirs = []string{root}
	d.WritableRoots = deviceRoots
	d.WritesEnabled = writesEnabled
	d.MaxWriteBytes = p2p.DefaultMaxWriteBytes

	// The same wiring daemonapp does: the daemon owns the decision, the
	// transport asks per request, and the client lends the session layer the
	// serving device's addresses so a dial works before discovery runs.
	serverNode.SetReadAccess(d.ReadableRootsFor)
	serverNode.RegisterListHandler()
	serverNode.RegisterFSHandler()
	serverNode.SetWriteAccess(d.WritePolicyFor)
	ln.WithKnownAddrs(d.PeerAddrs)

	_ = store.UpdateAddrs(clientID, addrStrings(clientNode))
	client.WithKnownAddrs(func(string) []string { return addrStrings(serverNode) })

	return pair{
		client:  client,
		server:  serverNode.Host.ID().String(),
		daemon:  d,
		paired:  clientID,
		cleanup: func() { client.Close(); ln.Close() },
	}
}

func addrStrings(n *p2p.Node) []string {
	var out []string
	for _, a := range n.Host.Addrs() {
		out = append(out, a.String())
	}
	return out
}

func writeAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s exists, but the write was refused", path)
	}
}

// A newly paired device is read-only: it can read, and it cannot write. This is
// the headline change — pairing alone no longer confers a write.
func TestPairedDeviceDefaultsToReadOnly(t *testing.T) {
	root := t.TempDir()
	writeAt(t, filepath.Join(root, "notes.txt"), "hello")

	p := tierPair(t, root, []string{root}, true)
	defer p.cleanup()

	got, err := p.readFile(filepath.Join(root, "notes.txt"))
	if err != nil {
		t.Fatalf("a device paired at the default tier must be able to read: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("read returned %q", got)
	}

	err = p.push("payload.txt", "injected")
	if err == nil {
		t.Fatal("a device paired at the default tier must not be able to write")
	}
	if !strings.Contains(err.Error(), string(TierRead)) {
		t.Errorf("the refusal should name the tier it holds: %v", err)
	}
	mustNotExist(t, filepath.Join(root, "payload.txt"))
}

// Raising the tier lets the same pairing write, with no restart and no
// re-pairing.
func TestRaisingTierGrantsWrite(t *testing.T) {
	root := t.TempDir()
	p := tierPair(t, root, []string{root}, true)
	defer p.cleanup()

	if _, err := p.daemon.Trust.SetTier(p.paired, string(TierReadWrite)); err != nil {
		t.Fatal(err)
	}
	if err := p.push("payload.txt", "now allowed"); err != nil {
		t.Fatalf("a device at read-write must be able to write: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "payload.txt"))
	if err != nil || string(got) != "now allowed" {
		t.Fatalf("the write did not land: %q %v", got, err)
	}
}

// Lowering the tier revokes the write again, immediately.
func TestLoweringTierRevokesWrite(t *testing.T) {
	root := t.TempDir()
	p := tierPair(t, root, []string{root}, true)
	defer p.cleanup()

	if _, err := p.daemon.Trust.SetTier(p.paired, string(TierReadWrite)); err != nil {
		t.Fatal(err)
	}
	if err := p.push("first.txt", "allowed"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.daemon.Trust.SetTier(p.paired, string(TierRead)); err != nil {
		t.Fatal(err)
	}
	if err := p.push("second.txt", "refused"); err == nil {
		t.Fatal("lowering the tier must revoke write access without a restart")
	}
	mustNotExist(t, filepath.Join(root, "second.txt"))
}

// Tier none keeps the pairing but grants nothing, on either path.
func TestTierNoneRefusesBothPaths(t *testing.T) {
	root := t.TempDir()
	writeAt(t, filepath.Join(root, "secret.txt"), "classified")

	p := tierPair(t, root, []string{root}, true)
	defer p.cleanup()

	if _, err := p.daemon.Trust.SetTier(p.paired, string(TierNone)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.readFile(filepath.Join(root, "secret.txt")); err == nil {
		t.Fatal("a device at tier none must not be able to read")
	}
	if err := p.push("payload.txt", "x"); err == nil {
		t.Fatal("a device at tier none must not be able to write")
	}
	if !p.daemon.Trust.Trusted(p.paired) {
		t.Error("the device must still be paired; tier none is not unpairing")
	}
}

// --allow-writes is still a separate gate, and its wording is what agents match.
func TestDeviceWriteGateStillApplies(t *testing.T) {
	root := t.TempDir()
	p := tierPair(t, root, []string{root}, false)
	defer p.cleanup()
	if _, err := p.daemon.Trust.SetTier(p.paired, string(TierReadWrite)); err != nil {
		t.Fatal(err)
	}

	err := p.push("payload.txt", "x")
	if err == nil {
		t.Fatal("a device started without --allow-writes must refuse every write")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("the refusal should say writes are disabled: %v", err)
	}
}

// Per-peer roots confine a peer to a subset of the device's writable dirs.
func TestPerPeerRootsConfineWrites(t *testing.T) {
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	elsewhere := filepath.Join(root, "elsewhere")
	mkdirAll(t, inbox)
	mkdirAll(t, elsewhere)

	p := tierPair(t, root, []string{inbox, elsewhere}, true)
	defer p.cleanup()

	if _, err := p.daemon.Trust.SetTier(p.paired, string(TierReadWrite)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.daemon.Trust.SetWritableRoots(p.paired, []string{inbox}, p.daemon.WritableRoots); err != nil {
		t.Fatal(err)
	}

	if err := p.push(filepath.Join(inbox, "ok.txt"), "confined"); err != nil {
		t.Fatalf("a write inside the peer's own roots must be allowed: %v", err)
	}

	err := p.push(filepath.Join(elsewhere, "nope.txt"), "refused")
	if err == nil {
		t.Fatal("a write outside the peer's own roots must be refused, even though the device allows it")
	}
	mustNotExist(t, filepath.Join(elsewhere, "nope.txt"))
}

// An unpaired device is refused, and the refusal does not leak this device's
// tiers or roots.
func TestUnpairedDeviceRefusedWithoutConfigLeak(t *testing.T) {
	root := t.TempDir()
	writeAt(t, filepath.Join(root, "secret.txt"), "classified")

	p := tierPair(t, root, []string{root}, true)
	defer p.cleanup()
	if _, err := p.daemon.Trust.SetTier(p.paired, string(TierReadWrite)); err != nil {
		t.Fatal(err)
	}

	stranger := "12D3KooWSomeUnpairedPeerIDThatIsNotPairedAtAll"
	if _, err := p.client.ReadRemote(pushCtx(), stranger, filepath.Join(root, "secret.txt"), 0, 0); err == nil {
		t.Fatal("an unpaired device must not read")
	}
	_, err := p.client.PushRemote(pushCtx(), stranger, "payload.txt", []byte("x"), false)
	if err == nil {
		t.Fatal("an unpaired device must not write")
	}
	for _, leak := range []string{string(TierReadWrite), root} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the refusal leaks device configuration (%q): %v", leak, err)
		}
	}
}

// A device at read-write with no writable roots configured cannot write, and the
// refusal names the missing configuration rather than the tier.
func TestNoWritableRootsIsRefusedDistinctly(t *testing.T) {
	root := t.TempDir()
	p := tierPair(t, root, nil, true)
	defer p.cleanup()
	if _, err := p.daemon.Trust.SetTier(p.paired, string(TierReadWrite)); err != nil {
		t.Fatal(err)
	}

	err := p.push("payload.txt", "x")
	if err == nil {
		t.Fatal("a device with no writable roots must refuse")
	}
	if !strings.Contains(err.Error(), "writable roots") {
		t.Errorf("the refusal should name the missing roots: %v", err)
	}
}

// The policy a write would get reflects the peer, not just the device.
func TestWritePolicyForIsPerPeer(t *testing.T) {
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	mkdirAll(t, inbox)

	p := tierPair(t, root, []string{root}, true)
	defer p.cleanup()

	if _, err := p.daemon.WritePolicyFor(p.paired); err == nil {
		t.Fatal("a read-only device must get no write policy")
	}

	if _, err := p.daemon.Trust.SetTier(p.paired, string(TierReadWrite)); err != nil {
		t.Fatal(err)
	}
	pol, err := p.daemon.WritePolicyFor(p.paired)
	if err != nil {
		t.Fatal(err)
	}
	if len(pol.Roots) != 1 || pol.Roots[0] != root {
		t.Errorf("roots = %v, want the device's", pol.Roots)
	}

	if _, err := p.daemon.Trust.SetWritableRoots(p.paired, []string{inbox}, p.daemon.WritableRoots); err != nil {
		t.Fatal(err)
	}
	pol, err = p.daemon.WritePolicyFor(p.paired)
	if err != nil {
		t.Fatal(err)
	}
	if len(pol.Roots) != 1 || pol.Roots[0] != inbox {
		t.Errorf("roots = %v, want the peer's subset", pol.Roots)
	}
	if pol.Mode != p2p.WriteSharedRoots {
		t.Errorf("mode = %q, want shared-roots; the daemon must never hand out a wider mode", pol.Mode)
	}
	if pol.MaxBytes <= 0 {
		t.Error("the policy must carry the size cap")
	}
}

// A tier survives a restart, because a device that silently stops being able
// to read or write after a reboot is indistinguishable from a bug.
func TestTierSurvivesRestartOnTheWire(t *testing.T) {
	root := t.TempDir()
	writeAt(t, filepath.Join(root, "notes.txt"), "still readable")

	p := tierPair(t, root, []string{root}, true)
	defer p.cleanup()

	if _, err := p.daemon.Trust.SetTier(p.paired, string(TierReadWrite)); err != nil {
		t.Fatal(err)
	}
	// Re-read the store from disk, as a restart would. NewTrustStore takes the
	// directory, not the file.
	reopened, err := NewTrustStore(filepath.Dir(p.daemon.Trust.path))
	if err != nil {
		t.Fatal(err)
	}
	p.daemon.Trust = reopened

	got, err := p.readFile(filepath.Join(root, "notes.txt"))
	if err != nil || string(got) != "still readable" {
		t.Fatalf("a tier must survive a restart: %q %v", got, err)
	}
	if err := p.push("after-restart.txt", "still writable"); err != nil {
		t.Fatalf("write access must survive a restart: %v", err)
	}
}
