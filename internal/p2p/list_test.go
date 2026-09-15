package p2p

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func testListNode(t *testing.T, root string, trusted map[string]bool) *Node {
	t.Helper()
	n, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	n.SetListAccess([]string{root}, func(id string) bool { return trusted[id] })
	n.RegisterListHandler()
	return n
}

func TestListRemoteAllowed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := testListNode(t, root, nil)
	// Trust the requester after boot (its ID is stable per temp dir key,
	// but here both nodes are fresh; allow by actual requester ID below).
	requester, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requester.Close() })
	provider.SetListAccess([]string{root}, func(id string) bool { return id == requester.Host.ID().String() })

	pi := peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	files, err := requester.ListRemote(ctx, pi, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "a.txt" {
		t.Fatalf("bad listing: %+v", files)
	}
}

func TestListRemoteDeniedWhenUnpaired(t *testing.T) {
	root := t.TempDir()
	provider := testListNode(t, root, map[string]bool{})
	requester, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requester.Close() })
	pi := peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := requester.ListRemote(ctx, pi, root); err == nil {
		t.Fatal("expected pairing denial")
	}
}

func TestListRemoteRejectsBreakout(t *testing.T) {
	root := t.TempDir()
	provider := testListNode(t, root, nil)
	requester, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requester.Close() })
	provider.SetListAccess([]string{root}, func(string) bool { return true })
	pi := peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := requester.ListRemote(ctx, pi, "/etc"); err == nil {
		t.Fatal("expected breakout rejection")
	}
}
