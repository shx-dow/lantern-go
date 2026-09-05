package p2p

import (
	"bytes"
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
	partial, err := storage.PartialPath(outputDir, code, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, want[:prefix], 0600); err != nil {
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

func TestTwoSharesOnOneHostDispatchIndependently(t *testing.T) {
	sourceDir := t.TempDir()
	outA := t.TempDir()
	outB := t.TempDir()
	pathA := filepath.Join(sourceDir, "a.bin")
	pathB := filepath.Join(sourceDir, "b.bin")
	wantA := []byte("alpha-payload-12345")
	wantB := []byte("beta-payload-67890-longer")
	if err := os.WriteFile(pathA, wantA, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, wantB, 0600); err != nil {
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
	codeA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	codeB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := sender.RegisterShareHandler(codeA, pathA, make(chan TransferProgress, 32)); err != nil {
		t.Fatal(err)
	}
	if err := sender.RegisterShareHandler(codeB, pathB, make(chan TransferProgress, 32)); err != nil {
		t.Fatal(err)
	}

	pi := peer.AddrInfo{ID: senderHost.ID(), Addrs: senderHost.Addrs()}
	if err := receiver.receiveFile(ctx, pi, codeA, outA, make(chan TransferProgress, 32)); err != nil {
		t.Fatal(err)
	}
	if err := receiver.receiveFile(ctx, pi, codeB, outB, make(chan TransferProgress, 32)); err != nil {
		t.Fatal(err)
	}

	gotA, err := os.ReadFile(filepath.Join(outA, "a.bin"))
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := os.ReadFile(filepath.Join(outB, "b.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotA) != string(wantA) || string(gotB) != string(wantB) {
		t.Fatal("multi-share dispatch returned wrong files")
	}
}

func TestTransferWrongCodeIsRejected(t *testing.T) {
	sourceDir := t.TempDir()
	outputDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "secret.bin")
	if err := os.WriteFile(sourcePath, []byte("top secret"), 0600); err != nil {
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
	if err := sender.RegisterShareHandler("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", sourcePath, make(chan TransferProgress, 32)); err != nil {
		t.Fatal(err)
	}

	pi := peer.AddrInfo{ID: senderHost.ID(), Addrs: senderHost.Addrs()}
	transferCtx, transferCancel := context.WithTimeout(ctx, 10*time.Second)
	defer transferCancel()
	if err := receiver.receiveFile(transferCtx, pi, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", outputDir, make(chan TransferProgress, 32)); err == nil {
		t.Fatal("transfer with wrong code succeeded")
	}
}

func TestTransferCorruptPrefixFailsHashCheck(t *testing.T) {
	sourceDir := t.TempDir()
	outputDir := t.TempDir()
	name := "corrupt.bin"
	sourcePath := filepath.Join(sourceDir, name)
	want := make([]byte, 2*crypto.ChunkSize+17)
	for i := range want {
		want[i] = byte((i * 7) % 251)
	}
	if err := os.WriteFile(sourcePath, want, 0600); err != nil {
		t.Fatal(err)
	}
	code := "1234567890abcdef1234567890abcdef"
	prefix := crypto.ChunkSize + 31
	corrupt := bytes.Clone(want[:prefix])
	corrupt[prefix-1] ^= 1
	partial, err := storage.PartialPath(outputDir, code, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, corrupt, 0600); err != nil {
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
	if err := sender.RegisterShareHandler(code, sourcePath, make(chan TransferProgress, 32)); err != nil {
		t.Fatal(err)
	}
	pi := peer.AddrInfo{ID: senderHost.ID(), Addrs: senderHost.Addrs()}
	if err := receiver.receiveFile(ctx, pi, code, outputDir, make(chan TransferProgress, 32)); err == nil {
		t.Fatal("corrupt resume prefix was accepted")
	}
}
