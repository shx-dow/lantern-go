package daemon

// The end-to-end reason identity backup exists. A device that comes back with
// the same key is still the device the other side paired with; a device that
// comes back with a fresh key is a stranger, even on the same address.

import (
	"path/filepath"
	"testing"

	"github.com/shx-dow/lantern-go/internal/identity"
	"github.com/shx-dow/lantern-go/internal/p2p"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

func TestARestoredIdentityKeepsItsPairing(t *testing.T) {
	root := t.TempDir()
	writeAt(t, filepath.Join(root, "notes.txt"), "hello")
	base := t.TempDir()

	server, err := lantern.New(lantern.Config{
		Port:        0,
		DataDir:     filepath.Join(base, "server"),
		DialTimeout: testDialTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	serverNode := server.Node()
	if serverNode == nil || serverNode.Host == nil {
		t.Fatal("the serving device did not expose a host")
	}

	store, err := NewTrustStore(base)
	if err != nil {
		t.Fatal(err)
	}
	d := New(server)
	d.Trust = store
	d.SharedDirs = []string{root}
	d.MaxWriteBytes = p2p.DefaultMaxWriteBytes
	serverNode.SetReadAccess(d.ReadableRootsFor)
	serverNode.RegisterListHandler()
	serverNode.RegisterFSHandler()
	server.WithKnownAddrs(d.PeerAddrs)
	serverID := serverNode.Host.ID().String()

	clientDir := filepath.Join(base, "client")
	client, err := lantern.New(lantern.Config{
		Port:        0,
		DataDir:     clientDir,
		DialTimeout: testDialTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	clientNode := client.Node()
	clientID := clientNode.Host.ID().String()
	if _, err := store.Add(TrustSpec{PeerID: clientID, Alias: "laptop"}); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAddrs(clientID, addrStrings(clientNode))
	client.WithKnownAddrs(func(string) []string { return addrStrings(serverNode) })

	ctx := pushCtx()
	note := filepath.Join(root, "notes.txt")
	got, err := client.ReadRemote(ctx, serverID, note, 0, 0)
	if err != nil {
		t.Fatalf("the paired client cannot read: %v", err)
	}
	if string(got.Data) != "hello" {
		t.Fatalf("read returned %q", got.Data)
	}
	client.Close()

	// The machine is lost. The encrypted archive is what survives.
	pass := []byte("correct horse battery")
	archive, header, err := identity.Export(clientDir, "laptop", pass)
	if err != nil {
		t.Fatal(err)
	}
	if header.PeerID != clientID {
		t.Fatalf("archive names peer %s, want %s", header.PeerID, clientID)
	}

	restoredDir := filepath.Join(base, "restored")
	if _, err := identity.Import(restoredDir, archive, pass, false); err != nil {
		t.Fatal(err)
	}
	restored, err := lantern.New(lantern.Config{
		Port:        0,
		DataDir:     restoredDir,
		DialTimeout: testDialTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restored.Close() })
	if id := restored.Node().Host.ID().String(); id != clientID {
		t.Fatalf("restored peer ID %s, want %s", id, clientID)
	}
	restored.WithKnownAddrs(func(string) []string { return addrStrings(serverNode) })

	got, err = restored.ReadRemote(ctx, serverID, note, 0, 0)
	if err != nil {
		t.Fatalf("the device it is paired with refused the restored identity: %v", err)
	}
	if string(got.Data) != "hello" {
		t.Fatalf("read returned %q", got.Data)
	}
	restored.Close()

	// Negative control: without the archive, a new identity on the same
	// machine and the same address is a stranger to the server.
	fresh, err := lantern.New(lantern.Config{
		Port:        0,
		DataDir:     filepath.Join(base, "fresh"),
		DialTimeout: testDialTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fresh.Close() })
	fresh.WithKnownAddrs(func(string) []string { return addrStrings(serverNode) })
	if _, err := fresh.ReadRemote(ctx, serverID, note, 0, 0); err == nil {
		t.Fatal("a new identity read from a device that never paired with it")
	}
	if store.Trusted(fresh.Node().Host.ID().String()) {
		t.Fatal("the trust store gained an entry for the new identity")
	}
}
