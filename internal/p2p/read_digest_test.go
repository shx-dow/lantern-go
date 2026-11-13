package p2p

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/protocol"
)

// CONTEXT.md requires that nothing is placed until the digest of what arrived
// has been verified, and a download is a landing route like any other. A read
// response used to carry no digest at all, so a download could only check the
// size it was told by the same peer it was checking. These tests pin the
// digest onto the read response itself, covering exactly the bytes that were
// sent, so a caller can verify a chunk before writing it.

// readRaw performs one OpRead over a bare stream and returns the response
// header with the content that followed it. It exists so the wire shape can be
// asserted directly, rather than through the client that decodes it.
func readRaw(t *testing.T, requester *Node, pi peer.AddrInfo, req FSRequest) (FSResponse, []byte) {
	t.Helper()
	ctx := fsCtx(t)
	if err := requester.Host.Connect(ctx, pi); err != nil {
		t.Fatal(err)
	}
	s, err := requester.Host.NewStream(ctx, pi.ID, FSProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := protocol.WriteMetadata(s, req); err != nil {
		t.Fatal(err)
	}
	var resp FSResponse
	if err := protocol.ReadMetadata(s, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" {
		t.Fatalf("remote: %s", resp.Error)
	}
	data := make([]byte, resp.Bytes)
	if _, err := io.ReadFull(s, data); err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func digestOfBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestFSReadResponseCarriesDigest(t *testing.T) {
	root := t.TempDir()
	const content = "hello world"
	writeFile(t, filepath.Join(root, "notes.md"), content)
	requester, pi := fsPair(t, root)

	resp, data := readRaw(t, requester, pi, FSRequest{Op: OpRead, Path: filepath.Join(root, "notes.md")})
	if string(data) != content {
		t.Fatalf("got %q", data)
	}
	if resp.SHA256 == "" {
		t.Fatal("a read response must carry a digest; none was sent")
	}
	if want := digestOfBytes([]byte(content)); resp.SHA256 != want {
		t.Fatalf("digest = %s, want %s", resp.SHA256, want)
	}
	if resp.Version != FSProtocolVersion {
		t.Fatalf("read response reports fs revision %d, want %d", resp.Version, FSProtocolVersion)
	}
}

// A range read is verified against the bytes it returns, not the whole file.
func TestFSReadDigestCoversOnlyTheRange(t *testing.T) {
	root := t.TempDir()
	const content = "hello world"
	writeFile(t, filepath.Join(root, "notes.md"), content)
	requester, pi := fsPair(t, root)

	resp, data := readRaw(t, requester, pi, FSRequest{Op: OpRead, Path: filepath.Join(root, "notes.md"), Offset: 6, Length: 5})
	if string(data) != "world" {
		t.Fatalf("got %q", data)
	}
	if want := digestOfBytes([]byte("world")); resp.SHA256 != want {
		t.Fatalf("digest = %s, want %s (the range, not the file)", resp.SHA256, want)
	}
	if resp.SHA256 == digestOfBytes([]byte(content)) {
		t.Fatal("digest covers the whole file rather than the returned range")
	}
}

// An empty file still needs a digest: a caller must be able to tell a verified
// empty file from a response that carried no verification at all.
func TestFSReadDigestOnEmptyFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "empty.txt"), "")
	requester, pi := fsPair(t, root)

	resp, data := readRaw(t, requester, pi, FSRequest{Op: OpRead, Path: filepath.Join(root, "empty.txt")})
	if len(data) != 0 {
		t.Fatalf("got %d bytes", len(data))
	}
	if resp.SHA256 != digestOfBytes(nil) {
		t.Fatalf("digest = %q, want the digest of no bytes %q", resp.SHA256, digestOfBytes(nil))
	}
	if !resp.EOF {
		t.Fatal("an empty file is at eof on the first read")
	}
}

// The client half has to keep the digest; ReadFS drops it, ReadFSResult is how
// a download gets hold of it.
func TestFSReadResultKeepsDigest(t *testing.T) {
	root := t.TempDir()
	const content = "verified bytes"
	writeFile(t, filepath.Join(root, "notes.md"), content)
	requester, pi := fsPair(t, root)

	res, err := requester.ReadFSResult(fsCtx(t), pi, FSRequest{Op: OpRead, Path: filepath.Join(root, "notes.md")})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != content {
		t.Fatalf("got %q", res.Data)
	}
	if want := digestOfBytes([]byte(content)); res.SHA256 != want {
		t.Fatalf("digest = %s, want %s", res.SHA256, want)
	}
	if !res.EOF || res.Entry == nil || res.Entry.Size != int64(len(content)) {
		t.Fatalf("bad result: %+v", res)
	}
}

// Stat and listing serve no content, so they must not look like something that
// was verified. An empty digest is the honest answer there.
func TestFSStatAndListCarryNoDigest(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	requester, pi := fsPair(t, root)

	stat, err := requester.ReadFSResult(fsCtx(t), pi, FSRequest{Op: OpStat, Path: filepath.Join(root, "a.txt")})
	if err != nil {
		t.Fatal(err)
	}
	if stat.SHA256 != "" {
		t.Fatalf("stat reported digest %q; only content responses carry one", stat.SHA256)
	}
	list, err := requester.ReadFSResult(fsCtx(t), pi, FSRequest{Op: OpList, Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if list.SHA256 != "" {
		t.Fatalf("listing reported digest %q; only content responses carry one", list.SHA256)
	}
}
