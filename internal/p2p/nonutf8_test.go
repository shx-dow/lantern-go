package p2p

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/shx-dow/lantern-go/internal/protocol"
)

// A filename on Linux is a byte string, and it may carry bytes that are not
// valid UTF-8. encoding/json cannot hold those: it replaces every invalid
// byte with U+FFFD, without erroring. A file named report\xff.txt therefore
// used to arrive as report�.txt, and be stored under the replacement
// character rather than the byte that was sent.
//
// These tests probe whether the platform can hold such a name at all, and skip
// where it cannot. Only Linux can, so only Linux runs them.
//
// The damage is worse than one wrong character. Two source files whose names
// differ only in an invalid byte both collapse onto the same U+FFFD, so they
// land on one destination and the second silently replaces or duplicates the
// first.

// canHoldNonUTF8Name reports whether this platform can create a file whose
// name is not valid UTF-8 and then read that exact name back.
//
// Only Linux can, and it is the only platform where this bug exists. macOS
// returns EILSEQ from the syscall layer, so the file cannot be created at all.
// Windows stores names as UTF-16 and Go substitutes U+FFFD for the invalid
// byte, so the name is already altered before the file exists, and no change
// to what goes on the wire can put the byte back.
//
// Probing beats testing the string. A name that is not valid UTF-8 is a
// compile-time fact, so a test that only checks that runs everywhere and fails
// on two of the three platforms.
func canHoldNonUTF8Name(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	const name = "report\xff.txt"
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != name {
		return false
	}
	return true
}

// nonUTF8Name returns a filename this platform can actually store.
func nonUTF8Name(t *testing.T) string {
	t.Helper()
	if !canHoldNonUTF8Name(t) {
		t.Skip("this platform cannot store a name that is not valid UTF-8")
	}
	return "report\xff.txt"
}

// TestWriteFSStoresTheBytesOfANameJSONCannotHold is the whole point: the file
// that lands on the receiving device has the name that was sent.
func TestWriteFSStoresTheBytesOfANameJSONCannotHold(t *testing.T) {
	name := nonUTF8Name(t)

	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	dst := filepath.Join(root, name)
	entry, _, err := requester.WriteFS(fsCtx(t), pi, dst, []byte("bytes"), false)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	// Compare exactly. A check that the reply is valid UTF-8 would accept the
	// replacement character, which is the whole defect: the reply has to
	// report the bytes that were stored, not a valid-looking version of them.
	if entry.Name != name {
		t.Errorf("the write reply reports %q, want %q", entry.Name, name)
	}

	// What actually landed on disk, not what the reply claimed.
	names := namesIn(t, root)
	if len(names) != 1 {
		t.Fatalf("expected one file, got %q", names)
	}
	if names[0] != name {
		t.Fatalf("stored as %q, sent as %q", names[0], name)
	}
}

// TestReadFSFindsAFileWhoseNameJSONCannotHold is the other direction. Without
// this the fix would only stop files being stored wrong, while a read of an
// existing such file would still fail to find it.
func TestReadFSFindsAFileWhoseNameJSONCannotHold(t *testing.T) {
	name := nonUTF8Name(t)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, nil)

	req := FSRequest{Op: OpRead, Path: filepath.Join(root, name)}
	req.setPath(req.Path)
	_, entry, data, eof, err := requester.ReadFS(fsCtx(t), pi, req)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !eof {
		t.Error("expected the whole file in one call")
	}
	if !bytes.Equal(data, []byte("bytes")) {
		t.Errorf("read %q, want %q", data, "bytes")
	}
	if entry == nil {
		t.Fatal("no entry returned")
	}
	if entry.Name != name {
		t.Errorf("entry name is %q, want %q", entry.Name, name)
	}
}

// TestListRFSNamesSurviveTheListing checks the step in between. An agent
// lists a directory, takes a name from the listing, and asks for that name. A
// listing that returned U+FFFD would send it back for a file that does not
// exist, so this covers a path that never touches a write.
func TestListRFSNamesSurviveTheListing(t *testing.T) {
	name := nonUTF8Name(t)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, nil)

	entries, _, _, _, err := requester.ReadFS(fsCtx(t), pi, ListRequestFor(root))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(entries))
	}
	if entries[0].Name != name {
		t.Fatalf("listed as %q, on disk as %q", entries[0].Name, name)
	}
}

// Two names differing only in an invalid byte must stay two names. Before the
// fix both collapsed onto the same U+FFFD, so the second write either replaced
// the first or refused against it.
func TestDistinctNonUTF8NamesDoNotCollide(t *testing.T) {
	// The names differ only in the invalid byte. That is the case that
	// collapses: anything else still leaves a distinct plain prefix.
	if !canHoldNonUTF8Name(t) {
		t.Skip("this platform cannot store a name that is not valid UTF-8")
	}
	a, b := "report\xff.txt", "report\xfe.txt"

	root := t.TempDir()
	requester, pi := fsPairWith(t, root, writePolicy(root, 0))

	if _, _, err := requester.WriteFS(fsCtx(t), pi, filepath.Join(root, a), []byte("A"), false); err != nil {
		t.Fatalf("write %q: %v", a, err)
	}
	if _, _, err := requester.WriteFS(fsCtx(t), pi, filepath.Join(root, b), []byte("B"), false); err != nil {
		t.Fatalf("write %q: %v", b, err)
	}

	got := namesIn(t, root)
	if len(got) != 2 {
		t.Fatalf("two names differing only in an invalid byte collapsed onto %q", got)
	}
	if got[0] == got[1] {
		t.Fatalf("both writes reported the same destination %q", got[0])
	}
}

func namesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// ListRequestFor builds the request an agent makes to list a directory,
// choosing the field that can hold the name.
func ListRequestFor(dir string) FSRequest {
	r := FSRequest{Op: OpList}
	r.setPath(dir)
	return r
}

// TestReadFSFillsTheCompanionForACallerThatDidNot is the same guarantee from
// the other side. Callers in pkg/lantern set Path and cannot reach the
// unexported setter, so ReadFS has to fill it at its own boundary. A test that
// calls setPath itself proves nothing about that.
//
// This test was added because the control for it stayed green: the earlier
// test set the companion by hand, so removing the boundary fill changed
// nothing it could see.
func TestReadFSFillsTheCompanionForACallerThatDidNot(t *testing.T) {
	name := nonUTF8Name(t)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	requester, pi := fsPairWith(t, root, nil)

	// Exactly what pkg/lantern builds: a plain Path and nothing else.
	req := FSRequest{Op: OpRead, Path: filepath.Join(root, name)}
	_, _, data, _, err := requester.ReadFS(fsCtx(t), pi, req)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(data, []byte("bytes")) {
		t.Errorf("read %q, want %q", data, "bytes")
	}
}

// TestListRemoteCarriesNonUTF8Names exercises the separate list protocol.
// TestListRFSNamesSurviveTheListing goes through the fs handler instead, so it
// never reached listRootsLocal, and the control for that fix was also green.
func TestListRemoteCarriesNonUTF8Names(t *testing.T) {
	name := nonUTF8Name(t)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := testListNode(t, root, nil)
	requester, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requester.Close() })
	provider.SetReadAccess(allowRootsFor([]string{root}, requester.Host.ID().String()))

	files, err := requester.ListRemote(fsCtx(t), peerAddr(provider), root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one entry, got %d", len(files))
	}
	if files[0].Name != name {
		t.Fatalf("listed as %q, on disk as %q", files[0].Name, name)
	}
}

// TestResolveShareCarriesANonUTF8Path covers the share code path, where the
// path crosses a second time before any read is built from it. Without the
// companion on the reply, a fetch asks for a file that does not exist.
func TestResolveShareCarriesANonUTF8Path(t *testing.T) {
	name := nonUTF8Name(t)

	root := t.TempDir()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("shared bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	sender := fsHost(t)
	sender.SetReadAccess(allowAllRoots(root))
	sender.RegisterFSHandler()
	receiver := fsHost(t)

	const code = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	registerShare(t, sender, code, path)

	info, err := receiver.ResolveShare(fsCtx(t), peerAddr(sender), code)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if info == nil {
		t.Fatal("a share inside the roots must resolve")
	}
	if info.Path != path {
		t.Fatalf("resolved to %q, want %q", info.Path, path)
	}
	if info.Name != name {
		t.Errorf("resolved name is %q, want %q", info.Name, name)
	}
}

// A companion that will not decode is reported rather than ignored. Falling
// back to the plain field answers a different question: for a directory it
// means listing the shared roots instead of the one asked for.
func TestMalformedCompanionIsReportedNotIgnored(t *testing.T) {
	root := t.TempDir()
	provider := testListNode(t, root, map[string]bool{})
	requester, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requester.Close() })
	provider.SetReadAccess(allowRootsFor([]string{root}, requester.Host.ID().String()))

	if err := requester.Host.Connect(fsCtx(t), peerAddr(provider)); err != nil {
		t.Fatal(err)
	}
	s, err := requester.Host.NewStream(fsCtx(t), provider.Host.ID(), ListProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := protocol.WriteMetadata(s, ListRequest{DirB64: "!"}); err != nil {
		t.Fatal(err)
	}
	var resp ListResponse
	if err := protocol.ReadMetadata(s, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == "" {
		t.Fatal("a malformed DirB64 was ignored and the shared roots listed instead")
	}
	if len(resp.Files) != 0 {
		t.Errorf("files came back alongside the error: %+v", resp.Files)
	}
}
