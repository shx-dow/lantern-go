package p2p

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/crypto"
	"github.com/shx-dow/lantern-go/internal/storage"
)

func TestTransferRoundTripAcrossTwoHosts(t *testing.T) {
	sourceDir := t.TempDir()
	outputDir := t.TempDir()
	name := "résumé.bin"
	sourcePath := filepath.Join(sourceDir, name)
	want := make([]byte, 3*crypto.ChunkSize+123)
	for i := range want {
		want[i] = byte(i % 251)
	}
	if err := os.WriteFile(sourcePath, want, 0600); err != nil {
		t.Fatal(err)
	}

	senderHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer senderHost.Close()
	receiverHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer receiverHost.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := &Node{Host: senderHost, ctx: ctx}
	receiver := &Node{Host: receiverHost, ctx: ctx}
	code := "0123456789abcdef0123456789abcdef"
	progress := make(chan TransferProgress, 32)
	sender.RegisterShareHandler(code, sourcePath, progress)

	pi := peer.AddrInfo{ID: senderHost.ID(), Addrs: senderHost.Addrs()}
	transferCtx, transferCancel := context.WithTimeout(ctx, 10*time.Second)
	defer transferCancel()
	if err := receiver.receiveFile(transferCtx, pi, code, outputDir, make(chan TransferProgress, 32)); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(outputDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("received file differs from source")
	}
}

func TestTransferResumesAndVerifiesExistingPrefix(t *testing.T) {
	sourceDir := t.TempDir()
	outputDir := t.TempDir()
	name := "resume.bin"
	sourcePath := filepath.Join(sourceDir, name)
	want := make([]byte, 2*crypto.ChunkSize+17)
	for i := range want {
		want[i] = byte((i * 7) % 251)
	}
	if err := os.WriteFile(sourcePath, want, 0600); err != nil {
		t.Fatal(err)
	}
	code := "abcdef0123456789abcdef0123456789"
	prefix := crypto.ChunkSize + 31
	if err := os.WriteFile(storage.PartialPath(outputDir, code, name), want[:prefix], 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveResume(outputDir, storage.ResumeState{
		Code: code, FileName: name, FileSize: int64(len(want)), Offset: int64(prefix),
	}); err != nil {
		t.Fatal(err)
	}

	senderHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer senderHost.Close()
	receiverHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer receiverHost.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sender := &Node{Host: senderHost, ctx: ctx}
	receiver := &Node{Host: receiverHost, ctx: ctx}
	sender.RegisterShareHandler(code, sourcePath, make(chan TransferProgress, 32))
	pi := peer.AddrInfo{ID: senderHost.ID(), Addrs: senderHost.Addrs()}
	if err := receiver.receiveFile(ctx, pi, code, outputDir, make(chan TransferProgress, 32)); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(outputDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("resumed file differs from source")
	}
}
