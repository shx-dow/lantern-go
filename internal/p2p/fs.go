package p2p

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/protocol"
)

// FSProtocolID is the libp2p stream protocol for remote filesystem reads.
// It is the read-oriented sibling of ListProtocolID: listings answer "what
// is there", this answers "give me the bytes" without a share code, a
// staging copy, or a transfer session.
//
// Security note: confidentiality and peer authentication come from the
// libp2p transport, which is already noise/TLS encrypted and verifies the
// remote peer's identity key. Pairing (isTrusted) is the authorization
// layer. Unlike a transfer there is no code-derived key here, because
// there is no code; a per-pair application key is future work.
const FSProtocolID = "/lantern/fs/1.0.0"

// Op selects the filesystem operation.
type Op string

const (
	// OpStat returns metadata for one path.
	OpStat Op = "stat"
	// OpList returns one directory level.
	OpList Op = "list"
	// OpRead returns a byte range of one file.
	OpRead Op = "read"
)

// Read length policy. A zero length means "to the end of file, capped";
// anything above MaxReadLength is rejected rather than silently clamped,
// so a caller never believes it received more than it did.
const (
	DefaultReadLength = 256 * 1024
	MaxReadLength     = 8 * 1024 * 1024
	fsiOTimeout       = 30 * time.Second
)

// FSRequest is one filesystem operation. Path is always resolved by the
// serving side inside its shared roots; the caller cannot widen scope.
type FSRequest struct {
	Op     Op     `json:"op"`
	Path   string `json:"path"`
	Offset int64  `json:"offset,omitempty"`
	Length int64  `json:"length,omitempty"`
}

// FSEntry describes one path: a file or a directory.
type FSEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
	IsDir   bool   `json:"is_dir"`
}

// FSResponse is the reply header, written as one length-delimited JSON
// frame. When Op was OpRead and Err is empty, exactly Bytes of raw file
// content follow the frame on the stream.
type FSResponse struct {
	Entries []FSEntry `json:"entries,omitempty"`
	Entry   *FSEntry  `json:"entry,omitempty"`
	Bytes   int64     `json:"bytes,omitempty"`
	EOF     bool      `json:"eof,omitempty"`
	Error   string    `json:"error,omitempty"`
}

func fail(msg string) FSResponse { return FSResponse{Error: msg} }

// RegisterFSHandler installs the filesystem stream handler (idempotent).
// Call SetListAccess first: the fs protocol is gated by the same roots and
// the same pairing predicate as listings.
func (n *Node) RegisterFSHandler() {
	n.fsOnce.Do(func() {
		n.Host.SetStreamHandler(FSProtocolID, n.serveFS)
	})
}

func (n *Node) serveFS(s network.Stream) {
	defer s.Close()
	remote := s.Conn().RemotePeer().String()

	n.mu.Lock()
	roots := append([]string(nil), n.listRoots...)
	check := n.listTrusted
	n.mu.Unlock()
	if check == nil || !check(remote) {
		_ = protocol.WriteMetadata(s, fail("not paired"))
		return
	}

	if err := s.SetDeadline(time.Now().Add(fsiOTimeout)); err != nil {
		return
	}
	var req FSRequest
	if err := protocol.ReadMetadata(s, &req); err != nil {
		_ = protocol.WriteMetadata(s, fail("bad request"))
		return
	}

	switch req.Op {
	case OpStat:
		_ = protocol.WriteMetadata(s, n.fsStat(roots, req.Path))
	case OpList:
		_ = protocol.WriteMetadata(s, n.fsList(roots, req.Path))
	case OpRead:
		n.fsRead(s, roots, req)
	default:
		_ = protocol.WriteMetadata(s, fail(fmt.Sprintf("unknown op %q", req.Op)))
	}
}

func entryFor(fi os.FileInfo) *FSEntry {
	return &FSEntry{
		Name:    fi.Name(),
		Size:    fi.Size(),
		ModTime: fi.ModTime().UTC().Format(time.RFC3339),
		IsDir:   fi.IsDir(),
	}
}

func (n *Node) fsStat(roots []string, path string) FSResponse {
	resolved, err := resolveWithinRoots(roots, path)
	if err != nil {
		return fail(err.Error())
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return fail(err.Error())
	}
	return FSResponse{Entry: entryFor(fi)}
}

func (n *Node) fsList(roots []string, path string) FSResponse {
	files, err := listRootsLocal(roots, path)
	if err != nil {
		return fail(err.Error())
	}
	entries := make([]FSEntry, 0, len(files))
	for _, f := range files {
		entries = append(entries, FSEntry{
			Name:    filepath.Base(f.Name),
			Size:    f.Size,
			ModTime: f.ModTime,
			IsDir:   f.IsDir,
		})
	}
	return FSResponse{Entries: entries}
}

func (n *Node) fsRead(s network.Stream, roots []string, req FSRequest) {
	if req.Offset < 0 {
		_ = protocol.WriteMetadata(s, fail("offset must not be negative"))
		return
	}
	resolved, err := resolveWithinRoots(roots, req.Path)
	if err != nil {
		_ = protocol.WriteMetadata(s, fail(err.Error()))
		return
	}

	f, err := os.Open(resolved)
	if err != nil {
		_ = protocol.WriteMetadata(s, fail(err.Error()))
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		_ = protocol.WriteMetadata(s, fail(err.Error()))
		return
	}
	if fi.IsDir() {
		_ = protocol.WriteMetadata(s, fail("path is a directory"))
		return
	}

	length := req.Length
	if length <= 0 {
		length = DefaultReadLength
	}
	if length > MaxReadLength {
		_ = protocol.WriteMetadata(s, fail(fmt.Sprintf("length %d exceeds max %d", length, MaxReadLength)))
		return
	}

	// Clamp to what actually exists so the header never over-promises.
	remaining := fi.Size() - req.Offset
	if remaining < 0 {
		remaining = 0
	}
	eof := remaining <= length
	if remaining < length {
		length = remaining
	}

	if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
		_ = protocol.WriteMetadata(s, fail(err.Error()))
		return
	}
	if err := protocol.WriteMetadata(s, FSResponse{Entry: entryFor(fi), Bytes: length, EOF: eof}); err != nil {
		return
	}
	if length == 0 {
		return
	}
	if _, err := io.CopyN(s, f, length); err != nil {
		// The header is already sent; there is no second channel to
		// report on, so the receiver surfaces a short read instead.
		return
	}
}

// ReadFS runs one filesystem op against pi. It is the client half and the
// only entry point callers outside this package need.
func (n *Node) ReadFS(ctx context.Context, pi peer.AddrInfo, req FSRequest) ([]FSEntry, *FSEntry, []byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, fsiOTimeout)
	defer cancel()

	if err := n.Host.Connect(ctx, pi); err != nil {
		return nil, nil, nil, false, fmt.Errorf("connect: %w", err)
	}
	s, err := n.Host.NewStream(ctx, pi.ID, FSProtocolID)
	if err != nil {
		return nil, nil, nil, false, fmt.Errorf("open stream: %w", err)
	}
	defer s.Close()
	if err := s.SetDeadline(time.Now().Add(fsiOTimeout)); err != nil {
		return nil, nil, nil, false, err
	}
	if err := protocol.WriteMetadata(s, req); err != nil {
		return nil, nil, nil, false, fmt.Errorf("send request: %w", err)
	}
	var resp FSResponse
	if err := protocol.ReadMetadata(s, &resp); err != nil {
		return nil, nil, nil, false, fmt.Errorf("read response: %w", err)
	}
	if resp.Error != "" {
		return nil, nil, nil, false, fmt.Errorf("remote: %s", resp.Error)
	}
	if resp.Bytes == 0 {
		return resp.Entries, resp.Entry, nil, resp.EOF, nil
	}
	data := make([]byte, resp.Bytes)
	if _, err := io.ReadFull(s, data); err != nil {
		return nil, nil, nil, false, fmt.Errorf("read content: %w", err)
	}
	return resp.Entries, resp.Entry, data, resp.EOF, nil
}
