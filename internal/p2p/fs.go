package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	// OpWrite stores content at a path. Unlike the other ops, the content
	// travels request-first: the request frame is followed by ContentLen
	// raw bytes on the stream.
	OpWrite Op = "write"
	// OpResolveShare asks which local path, if any, is advertised under a
	// share code. It is how a fetch from a paired device becomes a read
	// instead of a transfer: the receiver holds the code, and only the sender
	// knows the path.
	OpResolveShare Op = "resolve-share"
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
//
// For OpWrite, ContentLen bytes of raw content follow the request frame and
// Overwrite says whether replacing an existing file is permitted. The caller
// cannot widen scope and cannot force a replace it was not granted.
type FSRequest struct {
	Op         Op     `json:"op"`
	Path       string `json:"path"`
	Offset     int64  `json:"offset,omitempty"`
	Length     int64  `json:"length,omitempty"`
	Overwrite  bool   `json:"overwrite,omitempty"`
	ContentLen int64  `json:"content_len,omitempty"`
	// Code is the share code for OpResolveShare; the other ops ignore it.
	Code string `json:"code,omitempty"`
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
	// SHA256 is the lowercase hex digest of what was stored, returned on a
	// successful OpWrite so the sender can confirm what landed.
	SHA256 string `json:"sha256,omitempty"`
	// Ready is set in the pre-content acknowledgement of an OpWrite, once
	// policy, overwrite, and the size cap have all been satisfied. The
	// sender waits for it before sending any content.
	Ready bool `json:"ready,omitempty"`
	// Path is set by OpResolveShare when the code resolves to a file this peer
	// will serve over the read path. An empty Path with no Error means the
	// code is not resolvable that way, which is the receiver's cue to use the
	// transfer path instead.
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
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

	// Reads are bounded per peer as well as per device: an empty root list
	// means this peer may read nothing, which is what a device paired at
	// TierNone gets. Refusing here rather than in the HTTP layer matters,
	// because this stream handler is reachable by any paired peer, not only
	// by a local API caller.
	roots := n.rootsFor(remote)
	if len(roots) == 0 {
		_ = protocol.WriteMetadata(s, fail("not paired for read access"))
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
	case OpResolveShare:
		n.fsResolveShare(s, remote, req)
	case OpWrite:
		n.fsWrite(s, remote, req)
	default:
		_ = protocol.WriteMetadata(s, fail(fmt.Sprintf("unknown op %q", req.Op)))
	}
}

// SharedFile is what a peer reports when a share code resolves to a file it
// is willing to serve over the read path. SHA256 is the hash the sender
// computed when the share was registered, so the receiver can verify the bytes
// it reads against the sender's own record of them.
type SharedFile struct {
	Path   string
	Name   string
	Size   int64
	SHA256 string
}

// fsResolveShare answers "which path is this code?" for a paired peer.
//
// The answer is withheld unless the file sits inside the shared roots this
// peer is allowed to read. That matters beyond tidiness: a path outside those
// roots would otherwise disclose the existence and location of a file the peer
// has no right to know about, and would tell a stranger which of their guessed
// codes are live.
//
// Silence is not an error. A code that is unadvertised, already consumed, or
// outside the roots all produce an empty answer, which tells the receiver to
// fall back to the transfer path — still correct for unpaired peers.
func (n *Node) fsResolveShare(s network.Stream, remote string, req FSRequest) {
	roots := n.rootsFor(remote)
	if len(roots) == 0 {
		_ = protocol.WriteMetadata(s, fail("not paired for read access"))
		return
	}
	info := n.resolveShare(req.Code, roots)
	if info == nil {
		_ = protocol.WriteMetadata(s, FSResponse{})
		return
	}
	_ = protocol.WriteMetadata(s, FSResponse{
		Path:   info.Path,
		Entry:  &FSEntry{Name: info.Name, Size: info.Size},
		SHA256: info.SHA256,
	})
}

// resolveShare maps code to a path inside roots, or nil. Containment is decided
// by resolveWithinRoots, the same resolver the read path uses, so a share
// cannot be resolved to somewhere a read would refuse.
func (n *Node) resolveShare(code string, roots []string) *SharedFile {
	n.mu.Lock()
	state := n.shares[code]
	n.mu.Unlock()
	if state == nil {
		return nil
	}
	resolved, err := resolveWithinRoots(roots, state.filePath)
	if err != nil {
		return nil
	}
	fi, err := os.Stat(resolved)
	if err != nil || fi.IsDir() {
		return nil
	}
	return &SharedFile{Path: resolved, Name: fi.Name(), Size: fi.Size(), SHA256: state.fileHash}
}

// ResolveShare asks pi which path, if any, code names on that peer. ok is false
// when the code does not resolve over the read path, which is not an error: it
// is the signal to use the transfer path.
func (n *Node) ResolveShare(ctx context.Context, pi peer.AddrInfo, code string) (info *SharedFile, err error) {
	ctx, cancel := context.WithTimeout(ctx, fsiOTimeout)
	defer cancel()

	if err := n.Host.Connect(ctx, pi); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	s, err := n.Host.NewStream(ctx, pi.ID, FSProtocolID)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	defer s.Close()
	if err := s.SetDeadline(time.Now().Add(fsiOTimeout)); err != nil {
		return nil, err
	}
	if err := protocol.WriteMetadata(s, FSRequest{Op: OpResolveShare, Code: code}); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	var resp FSResponse
	if err := protocol.ReadMetadata(s, &resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.Error != "" {
		// An older peer does not know this op. That is not a failure of the
		// fetch, only of this shortcut, so it is reported as "not resolvable".
		return nil, nil
	}
	if resp.Path == "" {
		return nil, nil
	}
	out := &SharedFile{
		Path:   resp.Path,
		SHA256: resp.SHA256,
		Name:   filepath.Base(resp.Path),
	}
	// The size matters: the fetch uses it to detect a truncated read and to
	// write valid resume state, and a zero here would silently invalidate
	// every checkpoint.
	if resp.Entry != nil {
		out.Name = resp.Entry.Name
		out.Size = resp.Entry.Size
	}
	if out.Name == "" {
		out.Name = filepath.Base(resp.Path)
	}
	return out, nil
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

// resolveWritePath resolves a write destination under a write policy. It is
// deliberately stricter than the read resolver, and it differs in one
// important way: a write target usually does not exist yet, so it cannot be
// symlink-resolved whole. Instead the parent directory is resolved in full
// (which both proves containment and collapses any symlinked ancestor) and
// the base name is joined onto it. An existing symlink at the destination is
// refused outright rather than followed.
func resolveWritePath(pol WritePolicy, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path must not be empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	// Check the mode before anything else, so a device that has writes
	// switched off says so rather than blaming its roots.
	if pol.Mode == WriteDenied || pol.Mode == "" {
		return "", errors.New("writes are not permitted on this device; it must be started with write access enabled")
	}
	if pol.Mode != WriteSharedRoots {
		return "", fmt.Errorf("unknown write mode %q", pol.Mode)
	}

	// A relative destination is interpreted by the receiving device, not
	// against the sender's working directory: a bare filename lands in the
	// first writable root, which is what a caller passing "report.txt"
	// means. Absolute paths are honoured as given.
	if !filepath.IsAbs(path) {
		if len(pol.Roots) == 0 {
			return "", errors.New("no writable roots are configured on this device")
		}
		root, err := filepath.Abs(pol.Roots[0])
		if err != nil {
			return "", fmt.Errorf("resolve writable root: %w", err)
		}
		abs = filepath.Join(root, path)
	}
	base := filepath.Base(abs)
	if base == "." || base == ".." || base == string(os.PathSeparator) {
		return "", fmt.Errorf("path %q must name a file", path)
	}
	parent := filepath.Dir(abs)

	roots := pol.Roots
	if len(roots) == 0 {
		// No roots configured means no writable location. Falling back to
		// the read roots would silently widen the blast radius.
		return "", errors.New("no writable roots are configured on this device")
	}

	resolvedParent, err := resolveNewParent(roots, parent)
	if err != nil {
		// Name the roots, because with per-peer narrowing they are usually a
		// subset and the generic "outside shared dirs" reads like the device
		// is misconfigured rather than the peer being confined.
		return "", fmt.Errorf("%w; this peer may write only in: %s", err, strings.Join(roots, ", "))
	}
	dst := filepath.Join(resolvedParent, base)
	if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to write through a symlink: %q", path)
	}
	return dst, nil
}

// writePerm holds the modes a push creates: one for missing parent
// directories, one for the stored file.
type writePerm struct {
	dir  os.FileMode
	file os.FileMode
}

// inheritedPerm derives the modes for a push destination from the nearest
// existing ancestor directory of dir.
//
// A push never widens access. If the shared root is 0700, created directories
// are 0700 and the stored file is 0600; if the root is 0755, they are 0755 and
// 0644. The file mode is the directory mode without the execute bits, which is
// the usual relationship between a directory and the files inside it.
//
// Nothing is forced on. If the nearest ancestor is not owner-writable, the
// derived directory is not writable either, so the push fails at the create
// with an ordinary permission error rather than succeeding by widening rights
// the operator deliberately removed. Owner-read is the only floor, so a file is
// never created that its own owner cannot read.
func inheritedPerm(dir string) (writePerm, error) {
	fi, err := nearestExistingDir(dir)
	if err != nil {
		return writePerm{}, err
	}
	base := fi.Mode().Perm()
	return writePerm{
		dir:  base,
		file: base&^0o111 | 0o400,
	}, nil
}

// nearestExistingDir walks up from dir to the closest ancestor that exists, and
// returns its FileInfo. It mirrors resolveNewParent's walk, so the permissions
// come from the same directory that proved containment.
func nearestExistingDir(dir string) (os.FileInfo, error) {
	probe := dir
	for {
		fi, err := os.Stat(probe)
		if err == nil {
			if !fi.IsDir() {
				return nil, fmt.Errorf("%s is not a directory", probe)
			}
			return fi, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("resolve path: %w", err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return nil, fmt.Errorf("no existing ancestor for %q", dir)
		}
		probe = parent
	}
}

// resolveNewParent resolves a destination directory that may not exist yet.
// It anchors on the nearest existing ancestor, proves that ancestor sits
// inside roots, and rejoins the remaining plain names. Callers must
// re-check the result after creating the missing directories, since an
// ancestor could be replaced with a symlink in between.
func resolveNewParent(roots []string, parent string) (string, error) {
	missing := 0
	probe := parent
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		next := filepath.Dir(probe)
		if next == probe {
			return "", fmt.Errorf("no existing ancestor for %q", parent)
		}
		probe = next
		missing++
		if missing > 64 {
			return "", fmt.Errorf("path %q is nested too deeply", parent)
		}
	}
	resolved, err := resolveWithinRoots(roots, probe)
	if err != nil {
		return "", err
	}
	if missing == 0 {
		return resolved, nil
	}
	rel, err := filepath.Rel(probe, parent)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	return filepath.Join(resolved, rel), nil
}

// fsWrite stores content that follows the request frame. The file is
// written to a sibling temp file and renamed into place, so a reader never
// observes a half-written destination and a failed transfer leaves the
// previous contents intact.
func (n *Node) fsWrite(s network.Stream, remote string, req FSRequest) {
	pol, err := n.grantWrite(remote)
	if err != nil {
		_ = protocol.WriteMetadata(s, fail(err.Error()))
		return
	}
	// Refuse before reading any content, and tell the sender so it does not
	// push a payload that was never going to be stored.
	reject := func(msg string) {
		_ = protocol.WriteMetadata(s, fail(msg))
	}

	resolved, err := resolveWritePath(*pol, req.Path)
	if err != nil {
		reject(err.Error())
		return
	}

	max := pol.MaxBytes
	if max <= 0 {
		max = DefaultMaxWriteBytes
	}
	if req.ContentLen < 0 {
		reject("content_len must not be negative")
		return
	}
	if req.ContentLen > max {
		reject(fmt.Sprintf("content_len %d exceeds the %d byte limit for this device", req.ContentLen, max))
		return
	}

	if fi, err := os.Lstat(resolved); err == nil {
		if fi.IsDir() {
			reject("path is a directory")
			return
		}
		if !req.Overwrite {
			reject("destination exists; set overwrite to replace it")
			return
		}
	} else if !os.IsNotExist(err) {
		reject(err.Error())
		return
	}

	dir := filepath.Dir(resolved)
	// A push takes its permissions from the directory it lands in, rather than
	// hardcoding a mode. An operator who chose 0700 for a shared root must not
	// get 0755 directories and 0644 files from a paired peer: the root stays
	// private today, and every push-created path becomes readable the moment
	// someone relaxes it. MkdirAll only applies the mode to directories it
	// actually creates, so an existing parent keeps its own.
	perm, err := inheritedPerm(dir)
	if err != nil {
		reject(err.Error())
		return
	}
	if err := os.MkdirAll(dir, perm.dir); err != nil {
		reject(err.Error())
		return
	}
	// Re-verify containment now that the parents exist: creating them was a
	// window in which an ancestor could have been swapped for a symlink.
	if recheck, err := resolveWritePath(*pol, req.Path); err != nil || recheck != resolved {
		reject(fmt.Sprintf("destination changed underneath the write; refusing (%v)", err))
		return
	}

	tmp, err := os.CreateTemp(dir, ".lantern-push-*")
	if err != nil {
		reject(err.Error())
		return
	}
	tmpPath := tmp.Name()
	// Any failure past this point must not leave debris or a partial file.
	committed := false
	defer func() {
		tmp.Close()
		if !committed {
			os.Remove(tmpPath)
		}
	}()

	// Agree to receive before a single content byte moves, so a sender is
	// never asked to push a payload that was going to be refused anyway.
	if err := protocol.WriteMetadata(s, FSResponse{Ready: true}); err != nil {
		return
	}

	hasher := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(tmp, hasher), s, req.ContentLen)
	if copyErr != nil {
		reject(fmt.Sprintf("receive content: %v", copyErr))
		return
	}
	if written != req.ContentLen {
		reject(fmt.Sprintf("short content: got %d of %d bytes", written, req.ContentLen))
		return
	}
	if err := tmp.Sync(); err != nil {
		_ = protocol.WriteMetadata(s, fail(err.Error()))
		return
	}
	if err := tmp.Close(); err != nil {
		_ = protocol.WriteMetadata(s, fail(err.Error()))
		return
	}
	if err := os.Chmod(tmpPath, perm.file); err != nil {
		_ = protocol.WriteMetadata(s, fail(err.Error()))
		return
	}
	if err := os.Rename(tmpPath, resolved); err != nil {
		_ = protocol.WriteMetadata(s, fail(err.Error()))
		return
	}
	committed = true

	fi, err := os.Stat(resolved)
	if err != nil {
		_ = protocol.WriteMetadata(s, fail(err.Error()))
		return
	}
	_ = protocol.WriteMetadata(s, FSResponse{Entry: entryFor(fi), Bytes: written, EOF: true, SHA256: hex.EncodeToString(hasher.Sum(nil))})
}

// WriteFS stores content at path on pi. It is the client half of OpWrite and
// is the only way a peer can change another device's filesystem, so the
// remote's policy has the final say on whether it lands.
func (n *Node) WriteFS(ctx context.Context, pi peer.AddrInfo, path string, content []byte, overwrite bool) (FSEntry, string, error) {
	ctx, cancel := context.WithTimeout(ctx, fsiOTimeout)
	defer cancel()

	if err := n.Host.Connect(ctx, pi); err != nil {
		return FSEntry{}, "", fmt.Errorf("connect: %w", err)
	}
	s, err := n.Host.NewStream(ctx, pi.ID, FSProtocolID)
	if err != nil {
		return FSEntry{}, "", fmt.Errorf("open stream: %w", err)
	}
	defer s.Close()
	if err := s.SetDeadline(time.Now().Add(fsiOTimeout)); err != nil {
		return FSEntry{}, "", err
	}

	req := FSRequest{Op: OpWrite, Path: path, ContentLen: int64(len(content)), Overwrite: overwrite}
	if err := protocol.WriteMetadata(s, req); err != nil {
		return FSEntry{}, "", fmt.Errorf("send request: %w", err)
	}
	// The remote refuses before reading content when policy, overwrite, or
	// the size cap says no, so send the body only once it has agreed.
	var ack FSResponse
	if err := protocol.ReadMetadata(s, &ack); err != nil {
		return FSEntry{}, "", fmt.Errorf("read write acknowledgement: %w", err)
	}
	if ack.Error != "" {
		return FSEntry{}, "", fmt.Errorf("remote: %s", ack.Error)
	}
	if !ack.Ready {
		return FSEntry{}, "", errors.New("remote did not accept the write")
	}

	if err := protocol.WriteFull(s, content); err != nil {
		return FSEntry{}, "", fmt.Errorf("send content: %w", err)
	}

	var resp FSResponse
	if err := protocol.ReadMetadata(s, &resp); err != nil {
		return FSEntry{}, "", fmt.Errorf("read response: %w", err)
	}
	if resp.Error != "" {
		return FSEntry{}, "", fmt.Errorf("remote: %s", resp.Error)
	}
	var entry FSEntry
	if resp.Entry != nil {
		entry = *resp.Entry
	}
	return entry, resp.SHA256, nil
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
