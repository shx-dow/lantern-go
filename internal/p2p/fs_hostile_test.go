package p2p

import (
	"context"
	"os"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"time"

	"github.com/shx-dow/lantern-go/internal/protocol"
)

func TestHostileGarbageFrame(t *testing.T) {
	root := t.TempDir()
	provider := fsHost(t)
	requester := fsHost(t)
	provider.SetReadAccess(allowAllRoots(root))
	provider.RegisterFSHandler()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := requester.Host.Connect(ctx, peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	s, err := requester.Host.NewStream(ctx, provider.Host.ID(), FSProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// A frame header claiming a huge payload.
	if _, err := s.Write([]byte{0xff, 0xff, 0xff, 0xff}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var resp FSResponse
	_ = protocol.ReadMetadata(s, &resp)
	if resp.Error == "" {
		t.Error("an oversized frame length should be refused with an error")
	}
}

func TestHostileWriteWithHugeClaimedLength(t *testing.T) {
	root := t.TempDir()
	provider := fsHost(t)
	requester := fsHost(t)
	provider.SetReadAccess(allowAllRoots(root))
	provider.SetWriteAccess(grantEveryone(writePolicy(root, 1024)))
	provider.RegisterFSHandler()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := requester.Host.Connect(ctx, peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	s, err := requester.Host.NewStream(ctx, provider.Host.ID(), FSProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	req := FSRequest{Op: OpWrite, Path: root + "/x.txt", ContentLen: 1 << 62}
	if err := protocol.WriteMetadata(s, req); err != nil {
		t.Fatal(err)
	}
	var ack FSResponse
	if err := protocol.ReadMetadata(s, &ack); err != nil {
		t.Fatalf("the provider must answer before any body: %v", err)
	}
	if ack.Error == "" {
		t.Fatal("a claimed 4-exabyte write must be refused before reading a body")
	}
	// Critically: nothing may have been created.
	if entries, _ := readDirNames(root); len(entries) != 0 {
		t.Errorf("refused write left files behind: %v", entries)
	}
}

func TestHostileReadNegativeOffset(t *testing.T) {
	root := t.TempDir()
	provider := fsHost(t)
	requester := fsHost(t)
	provider.SetReadAccess(allowAllRoots(root))
	provider.RegisterFSHandler()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := requester.Host.Connect(ctx, peer.AddrInfo{ID: provider.Host.ID(), Addrs: provider.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	s, err := requester.Host.NewStream(ctx, provider.Host.ID(), FSProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := protocol.WriteMetadata(s, FSRequest{Op: OpRead, Path: root, Offset: -1}); err != nil {
		t.Fatal(err)
	}
	var resp FSResponse
	_ = protocol.ReadMetadata(s, &resp)
	if resp.Error == "" {
		t.Error("a negative offset must be refused")
	}
}

func readDirNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out, nil
}
