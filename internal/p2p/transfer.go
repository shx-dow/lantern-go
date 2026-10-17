package p2p

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/crypto"
	"github.com/shx-dow/lantern-go/internal/protocol"
	"github.com/shx-dow/lantern-go/internal/storage"
)

// cleanupPartial removes the staged partial file and its resume checkpoint after
// a transfer was refused, so a failed attempt leaves nothing behind that the
// next attempt would resume into.
//
// It deliberately does not take the caller's *os.File. The caller owns that
// handle and tracks it with its own `closed` flag; a helper that closed it
// behind the caller's back would leave the two disagreeing about whether the
// file is open, and the next use would be a use-after-close. Callers close
// immediately before calling this, which is also where the Windows requirement
// lives: unlinking an open file works on Unix but fails on Windows, and leaving
// the file behind there would defeat the point.
//
// Callers must hold the download lock for partialPath (see claimDownload). The
// partial path is derived from the code and file name, so it is shared between
// two downloads of the same code into the same directory, and without exclusive
// ownership one download's refusal deletes the other's in-flight work.
func cleanupPartial(partialPath, outputDir, code string) error {
	var errs []error
	if partialPath != "" {
		if err := os.Remove(partialPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("remove partial %s: %w", partialPath, err))
		}
	}
	if err := storage.ClearResume(outputDir, code); err != nil {
		errs = append(errs, fmt.Errorf("clear resume state: %w", err))
	}
	return errors.Join(errs...)
}

// claimDownload takes exclusive ownership of the download identified by
// downloadKey, and returns the function that releases it.
//
// The partial file's path is derived from the output directory, the code, and
// the file name, so two downloads that agree on all three agree on the file. A
// caller that cleans up on failure — which cleanupPartial does — would then
// delete the partial the other download is still writing, and that download
// would fail its final rename having done nothing wrong. Ownership is therefore
// per download key rather than per process: two fetches of the same code into
// the same directory cannot overlap, and neither can a fetch and a resume of it.
//
// Releasing is deferred by the caller, so a download that panics does not wedge
// the key permanently.
func (n *Node) claimDownload(key string) (release func(), err error) {
	key = filepath.Clean(key)

	n.downloadsMu.Lock()
	defer n.downloadsMu.Unlock()

	if n.downloads == nil {
		n.downloads = make(map[string]struct{})
	}
	if _, busy := n.downloads[key]; busy {
		return nil, fmt.Errorf("another download of %s into this directory is already in progress", filepath.Base(key))
	}
	n.downloads[key] = struct{}{}

	var once sync.Once
	return func() {
		once.Do(func() {
			n.downloadsMu.Lock()
			delete(n.downloads, key)
			n.downloadsMu.Unlock()
		})
	}, nil
}

// destinationExists reports whether anything already sits at path.
//
// The rename that publishes a download will silently replace a regular file, so
// a download has to ask before it lands rather than after. The push path has
// always had this check, gated on the sender setting overwrite; the download
// routes had none, so receiving a share named notes.md destroyed whatever
// notes.md was already in the directory.
//
// A symlink counts as occupied. Replacing it would either clobber the file it
// points at or write through it to somewhere outside the output directory,
// depending on the platform.
func destinationExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, fmt.Errorf("check destination %s: %w", path, err)
	}
}

// errDestinationExists is the refusal both routes return when the destination is
// occupied. It names the path so the operator can see what is in the way, and
// says what to do about it, because "file exists" on its own sends people
// looking for a flag that — at the moment — does not exist.
func errDestinationExists(path string) error {
	return fmt.Errorf("%s already exists; remove or rename it, or fetch into a different directory", path)
}

// linkUnsupported reports whether err means "this filesystem cannot make hard
// links" rather than a real failure.
//
// FAT, some network mounts, and Windows without the SeCreateHardlink privilege
// refuse link(2). For those the caller falls back to a check-then-rename, which
// is racy but still better than replacing unconditionally.
func linkUnsupported(err error) bool {
	return errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.EXDEV)
}

// publishIfAbsent moves src to dst only if dst does not exist.
//
// os.Rename replaces dst unconditionally, so checking first and renaming second
// leaves a window: anything that creates dst between the two is silently
// overwritten. That is not hypothetical here. The per-download claim is keyed on
// the code, so two downloads with *different* codes into one directory may
// overlap — and two peers sharing a file called report.txt both target
// outputDir/report.txt. Both pass the check, both rename, and the second replaces
// the first.
//
// A hard link is the portable atomic form of "create if absent": link(2) either
// creates dst or fails with EEXIST, with no gap between. The staged file keeps
// its own name until the link has succeeded, so a reader never sees a partial —
// dst appears pointing at complete content, or not at all.
func publishIfAbsent(src, dst string) error {
	err := os.Link(src, dst)
	if err == nil {
		// dst now names the same inode as src. Dropping src leaves exactly one
		// name for the content, which is what a rename would have produced.
		if rmErr := os.Remove(src); rmErr != nil {
			return fmt.Errorf("file landed at %s but the staged copy %s could not be removed: %w", dst, src, rmErr)
		}
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return errDestinationExists(dst)
	}
	if !linkUnsupported(err) {
		return fmt.Errorf("publish %s: %w", dst, err)
	}

	// No hard links on this filesystem. Fall back, and accept that the check and
	// the rename are no longer atomic against each other.
	occupied, cerr := destinationExists(dst)
	if cerr != nil {
		return cerr
	}
	if occupied {
		return errDestinationExists(dst)
	}
	if rerr := os.Rename(src, dst); rerr != nil {
		return fmt.Errorf("finalize output file: %w", rerr)
	}
	return nil
}

// noDigestRefusal is the error both download routes return when a sender offers
// no digest. It is one function so the two routes cannot drift into describing
// the same break differently, which is the sort of drift that makes a support
// question unanswerable.
func noDigestRefusal() error {
	return fmt.Errorf("sender offered no digest; refusing to place unverified content (this device requires fs protocol v%d, so upgrade the sending device)", FSProtocolVersion)
}

// FileMeta is the plaintext header sent before file bytes: base name,
// total size, and a hex SHA-256 of the source for end-to-end verification.
type FileMeta struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Hash string `json:"hash,omitempty"`
}

// CodeBytes is the entropy of a generated share code.
const CodeBytes = 16

// ioBufSize is the file read/write chunk size for streaming transfers.
const ioBufSize = 32 * 1024

// TransferProgress is a sender-side progress report consumed by
// Lantern.forwardProgress; Done marks the final message before close.
type TransferProgress struct {
	FileName string
	Bytes    int64
	Total    int64
	Done     bool
	Err      error
}

// reportProgress delivers msg to the transfer's consumer, and reports false if
// the consumer has gone.
//
// A bare `progress <- msg` blocks forever once the consumer stops reading, which
// is what happens the moment a caller cancels: the daemon's forwardProgress
// selects on its context and returns, and the CLI's reader exits. The transfer
// goroutine then sits on a full channel holding an open file and a libp2p
// stream, for the lifetime of the process. On a long-running daemon those
// accumulate silently.
//
// Callers return when this returns false, so a cancelled transfer stops writing
// instead of blocking on its way out.
func reportProgress(ctx context.Context, progress chan<- TransferProgress, msg TransferProgress) bool {
	select {
	case progress <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

type shareState struct {
	code     string
	filePath string
	fileName string
	fileSize int64
	fileHash string
	handled  atomic.Bool
	progress chan TransferProgress
	// readPeer is the remote that resolved this code over the read path, and
	// so the only peer whose reads count as fetching this share. Empty until
	// the code is resolved, and reserved once it is: a later resolve of the
	// same code does not take it. Guarded by n.mu, not by its own lock,
	// because the map holding it is guarded by that.
	readPeer string
	// seq orders shares by registration, so that a read which could match
	// several shares completes a predictable one. Map order is randomised on
	// every iteration, and "an arbitrary share" is not a decision.
	seq uint64
	// ctx governs the share's lifetime. It is the same context the advertisement
	// runs under, so cancelling it means nobody is reporting progress any more.
	ctx  context.Context
	done func()
}

// RegisterShareHandler advertises path under code to any receiver that
// proves knowledge of the code. The file is hashed up front so serving a
// stream never blocks on I/O. Each code serves at most one receiver.
//
// ctx bounds the share: cancelling it stops an in-flight transfer that is
// blocked reporting to a consumer which has stopped reading. Pass the same
// context that runs the advertisement.
func (n *Node) RegisterShareHandler(ctx context.Context, code string, path string, progress chan TransferProgress, done ...func()) error {
	if code == "" {
		return fmt.Errorf("share code must not be empty")
	}
	if progress == nil {
		return fmt.Errorf("progress channel must not be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stop := func() {}
	if len(done) > 0 && done[0] != nil {
		stop = done[0]
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}
	if fi.IsDir() {
		return fmt.Errorf("path is a directory: %s", path)
	}
	fullHash, err := hashFile(file)
	if err != nil {
		return fmt.Errorf("hash file: %w", err)
	}

	n.mu.Lock()
	n.shareSeq++
	seq := n.shareSeq
	n.mu.Unlock()

	state := &shareState{
		code:     code,
		seq:      seq,
		filePath: path,
		fileName: filepath.Base(path),
		fileSize: fi.Size(),
		fileHash: fmt.Sprintf("%x", fullHash),
		progress: progress,
		ctx:      ctx,
		done:     stop,
	}

	n.mu.Lock()
	if n.shares == nil {
		n.shares = make(map[string]*shareState)
	}
	n.shares[code] = state
	n.mu.Unlock()

	n.handlerOnce.Do(func() {
		n.Host.SetStreamHandler(ProtocolID, n.serveShare)
	})
	return nil
}

func (n *Node) serveShare(s network.Stream) {
	defer s.Close()

	dec := json.NewDecoder(s)
	var req protocol.TransferRequest
	if err := dec.Decode(&req); err != nil {
		return
	}
	if req.Offset < 0 {
		return
	}
	if len(req.Challenge) != crypto.ChallengeBytes || len(req.Proof) == 0 {
		return
	}

	state := n.matchShare(req)
	if state == nil {
		return
	}
	if !state.handled.CompareAndSwap(false, true) {
		return
	}
	progress := state.progress
	ctx := state.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// Teardown order matters (defers run LIFO): the progress channel must
	// close before state.done() cancels the advertisement context.
	// forwardProgress selects on both, so cancelling first lets it observe
	// the cancelled context and drop the buffered final message, leaving
	// the sender stuck in running with the receiver already done.
	defer state.done()
	defer close(progress)
	defer n.ClearLocal(state.code)
	defer n.forgetShare(state.code)

	file, err := os.Open(state.filePath)
	if err != nil {
		_ = reportProgress(ctx, progress, TransferProgress{Err: fmt.Errorf("open file: %w", err)})
		return
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		_ = reportProgress(ctx, progress, TransferProgress{Err: fmt.Errorf("stat file: %w", err)})
		return
	}
	if fi.Size() != state.fileSize {
		_ = reportProgress(ctx, progress, TransferProgress{Err: fmt.Errorf("file changed during share: was %d bytes, now %d", state.fileSize, fi.Size())})
		return
	}

	if req.Offset > fi.Size() {
		_ = reportProgress(ctx, progress, TransferProgress{Err: fmt.Errorf("resume offset %d exceeds file size %d", req.Offset, fi.Size())})
		return
	}
	if _, err := file.Seek(req.Offset, io.SeekStart); err != nil {
		_ = reportProgress(ctx, progress, TransferProgress{Err: fmt.Errorf("seek: %w", err)})
		return
	}

	key, err := crypto.DeriveKey(state.code)
	if err != nil {
		_ = reportProgress(ctx, progress, TransferProgress{Err: fmt.Errorf("derive key: %w", err)})
		return
	}

	ew, err := crypto.NewEncryptedWriter(s, key)
	if err != nil {
		_ = reportProgress(ctx, progress, TransferProgress{Err: fmt.Errorf("create encrypt: %w", err)})
		return
	}

	meta := FileMeta{
		Name: state.fileName,
		Size: state.fileSize,
		Hash: state.fileHash,
	}

	if err := protocol.WriteMetadata(ew, meta); err != nil {
		_ = reportProgress(ctx, progress, TransferProgress{Err: fmt.Errorf("send meta: %w", err)})
		return
	}

	sent := req.Offset
	buf := make([]byte, ioBufSize)
	for {
		n, err := file.Read(buf)
		if n > 0 {
			if _, werr := ew.Write(buf[:n]); werr != nil {
				_ = reportProgress(ctx, progress, TransferProgress{Err: fmt.Errorf("write stream: %w", werr)})
				return
			}
			sent += int64(n)
			if !reportProgress(ctx, progress, TransferProgress{
				FileName: meta.Name,
				Bytes:    sent,
				Total:    meta.Size,
			}) {
				return
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = reportProgress(ctx, progress, TransferProgress{Err: fmt.Errorf("read file: %w", err)})
			return
		}
	}

	if err := ew.Close(); err != nil {
		_ = reportProgress(ctx, progress, TransferProgress{Err: fmt.Errorf("close encrypt: %w", err)})
		return
	}

	reportProgress(ctx, progress, TransferProgress{
		FileName: meta.Name,
		Bytes:    sent,
		Total:    meta.Size,
		Done:     true,
	})
}

func (n *Node) matchShare(req protocol.TransferRequest) *shareState {
	n.mu.Lock()
	codes := make([]string, 0, len(n.shares))
	for code := range n.shares {
		codes = append(codes, code)
	}
	n.mu.Unlock()
	for _, code := range codes {
		n.mu.Lock()
		state, ok := n.shares[code]
		n.mu.Unlock()
		if !ok {
			continue
		}
		if crypto.VerifyAuthProof(state.code, req.Challenge, req.Offset, req.Proof) {
			return state
		}
	}
	return nil
}

// sameFile reports whether two paths are the same file on disk.
//
// By identity, never as strings. A share holds the path the caller passed to
// ShareSession, while every path reaching the correlation has been through
// resolveWithinRoots, which calls EvalSymlinks. Those differ wherever the
// caller's path is not already canonical -- macOS temp directories live under
// /var, which is a symlink to /private/var -- and a string comparison then
// matches nothing at all. Linux agrees, which is how that passed there and
// failed on two other platforms.
func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// noteShareResolved reserves the share held under code for remote, so a later
// read of that file by that peer is recognised as fetching this share and not
// some other share of the same file.
//
// Keyed by the code the receiver presented, never by path. Two ShareSession
// calls on one file produce two codes, and marking both by path meant
// completing either one satisfied the other: the receiver could be handed a
// code that was never resolved, and the share it did resolve could be left
// waiting.
//
// Reserved, not overwritten. The first resolve claims the share. A second peer
// that resolves the same code and then stops before reading used to take the
// reservation with it, so the peer that was actually downloading finished with
// no match left, no completion, and the original hang -- this time because a
// bystander had been in the way.
func (n *Node) noteShareResolved(code string, remote string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	state := n.shares[code]
	if state == nil || state.readPeer != "" {
		return
	}
	state.readPeer = remote
}

// completeShareRead reports that remote has finished reading path, when that
// read was the fetch of a share remote resolved.
//
// A share is served once. serveShare already refuses a second transfer of one
// code, and the read path had no such guard, so two receivers holding the same
// code could each complete it. This matches the transfer path: the first
// fetcher gets the file, and a second one is told the code is spent.
//
// The share is forgotten afterwards, as serveShare forgets it. Nothing else
// does on this path, so without that the share would stay advertised for the
// life of the process.
//
// The advertisement context is deliberately not consulted. It is cancelled
// routinely, the moment DHT advertising returns, which says nothing about
// whether the share was served; treating that as teardown would refuse exactly
// the fetches that arrive soon after advertising finishes.
func (n *Node) completeShareRead(remote string, path string) {
	n.mu.Lock()
	var found *shareState
	for _, state := range n.shares {
		if state.readPeer != remote || !sameFile(state.filePath, path) {
			continue
		}
		// remote resolved more than one code for this file. They are the same
		// fetch either way, so the oldest share wins -- chosen by registration
		// order, because ranging a map would pick an arbitrary one and the
		// receiver would be told a code was spent that it never presented.
		if found == nil || state.seq < found.seq {
			found = state
		}
	}
	if found != nil {
		delete(n.shares, found.code)
	}
	n.mu.Unlock()

	if found == nil || !found.handled.CompareAndSwap(false, true) {
		return
	}
	reportProgress(context.Background(), found.progress, TransferProgress{
		FileName: found.fileName,
		Bytes:    found.fileSize,
		Total:    found.fileSize,
		Done:     true,
	})

	// Teardown order matters, and it is the order serveShare already uses.
	// forwardProgress selects on both the progress channel and this share's
	// context, so cancelling first lets it observe the cancelled context and
	// drop the buffered final message -- leaving the sender waiting on an
	// event that was queued but never delivered. The receiver succeeded; the
	// sender hangs anyway. Closing the channel is what guarantees the queued
	// message is taken: forwardProgress drains it, emits the event, then sees
	// the close and returns.
	close(found.progress)
	found.done()
}

func (n *Node) forgetShare(code string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.shares, code)
}

// RegisterReceive pulls the file behind code from pi into outputDir in
// the background, resuming any partial download. Progress closes when done.
func (n *Node) RegisterReceive(ctx context.Context, pi peer.AddrInfo, code string, outputDir string, progress chan TransferProgress) {
	go func() {
		defer close(progress)
		err := n.receiveFile(ctx, pi, code, outputDir, progress)
		if err != nil {
			// Best effort: if the caller cancelled, it is no longer reading, and
			// blocking here would strand this goroutine and the files it holds.
			reportProgress(ctx, progress, TransferProgress{Err: err})
		}
	}()
}

func (n *Node) receiveFile(ctx context.Context, pi peer.AddrInfo, code string, outputDir string, progress chan<- TransferProgress) error {
	// Claim before touching the resume state or the partial file. The file name
	// is not known until the sender's header arrives, but the checkpoint is keyed
	// by code alone and the partial by code and name, so the code is the
	// narrowest key available here and is the one that matters for cleanup.
	release, err := n.claimDownload(filepath.Join(outputDir, code))
	if err != nil {
		return err
	}
	defer release()

	if err := n.Host.Connect(ctx, pi); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	s, err := n.Host.NewStream(ctx, pi.ID, ProtocolID)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer s.Close()

	resume, hasResume, err := storage.LoadResume(outputDir, code)
	if err != nil {
		return fmt.Errorf("check resume: %w", err)
	}
	var outPath string
	var offset int64
	if hasResume {
		outPath, err = storage.PartialPath(outputDir, code, resume.FileName)
		if err != nil {
			return fmt.Errorf("resume path: %w", err)
		}
		offset = resume.Offset
	}

	challenge := make([]byte, crypto.ChallengeBytes)
	if _, err := crand.Read(challenge); err != nil {
		return fmt.Errorf("generate authentication challenge: %w", err)
	}
	proof, err := crypto.AuthProof(code, challenge, offset)
	if err != nil {
		return fmt.Errorf("create authentication proof: %w", err)
	}
	req := protocol.TransferRequest{Offset: offset, Challenge: challenge, Proof: proof}
	enc := json.NewEncoder(s)
	if err := enc.Encode(req); err != nil {
		return fmt.Errorf("send request: %w", err)
	}

	key, err := crypto.DeriveKey(code)
	if err != nil {
		return fmt.Errorf("derive key: %w", err)
	}

	er, err := crypto.NewEncryptedReader(s, key)
	if err != nil {
		return fmt.Errorf("create decrypt: %w", err)
	}

	var meta FileMeta
	if err := protocol.ReadMetadata(er, &meta); err != nil {
		return fmt.Errorf("read meta: %w", err)
	}
	if err := storage.CheckFileName(meta.Name); err != nil {
		return fmt.Errorf("peer sent invalid file name: %w", err)
	}
	if meta.Size < 0 {
		return fmt.Errorf("peer sent invalid file size %d", meta.Size)
	}
	// The digest arrives in the same header as the size, so there is no reason to
	// pull a body we already know cannot be placed. Refusing here also means no
	// partial is created for a transfer that was never going to finish, which is
	// what used to leave the next attempt resuming into bytes from a peer that
	// had already been told no.
	if meta.Hash == "" {
		if err := storage.ClearResume(outputDir, code); err != nil {
			return fmt.Errorf("clear resume state: %w", err)
		}
		return noDigestRefusal()
	}

	if outPath == "" {
		outPath, err = storage.PartialPath(outputDir, code, meta.Name)
		if err != nil {
			return fmt.Errorf("output path: %w", err)
		}
	} else if filepath.Base(meta.Name) != filepath.Base(resume.FileName) {
		return fmt.Errorf("resume file name changed from %s to %s", resume.FileName, meta.Name)
	}

	// The destination name is known from the header, so refuse before pulling a
	// body that would only be discarded at the rename.
	finalPath := filepath.Join(outputDir, filepath.Base(meta.Name))
	occupied, err := destinationExists(finalPath)
	if err != nil {
		return err
	}
	if occupied {
		return errDestinationExists(finalPath)
	}

	// Permissions are inherited from the destination rather than hardcoded, for
	// the same reason a push inherits them: an operator who chose 0700 must not
	// get world-readable files and directories because a constant won. Derived
	// before MkdirAll so a directory the download has to create is born with the
	// mode its parent implies rather than 0755.
	perm, err := inheritedPerm(outputDir)
	if err != nil {
		return fmt.Errorf("derive permissions for %s: %w", outputDir, err)
	}
	if err := os.MkdirAll(outputDir, perm.dir); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	flags := os.O_RDWR | os.O_CREATE
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(outPath, flags, perm.file)
	if err != nil {
		return fmt.Errorf("open output file: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
		}
	}()

	if offset > 0 {
		info, err := out.Stat()
		if err != nil {
			return fmt.Errorf("stat output file: %w", err)
		}
		if offset > meta.Size || info.Size() < offset {
			return fmt.Errorf("resume offset %d is invalid for output size %d and source size %d", offset, info.Size(), meta.Size)
		}
		if err := out.Truncate(offset); err != nil {
			return fmt.Errorf("truncate partial output: %w", err)
		}
		if _, err := out.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("seek output: %w", err)
		}
	}

	h := sha256.New()
	received := offset
	// saveCheckpoint persists resume state; failures are joined with the
	// error being returned so a lost checkpoint never masks the cause.
	saveCheckpoint := func() error {
		return storage.SaveResume(outputDir, storage.ResumeState{Code: code, FileName: meta.Name, FileSize: meta.Size, Offset: received})
	}
	if offset > 0 {
		if _, err := out.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seek output for hash: %w", err)
		}
		if _, err := io.CopyN(h, out, offset); err != nil {
			return fmt.Errorf("hash resumed output: %w", err)
		}
		if _, err := out.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("restore output position: %w", err)
		}
	}

	if err := saveCheckpoint(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	buf := make([]byte, ioBufSize)
	for received < meta.Size {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, saveCheckpoint())
		}

		n, err := er.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
			if werr := protocol.WriteFull(out, buf[:n]); werr != nil {
				return errors.Join(fmt.Errorf("write file: %w", werr), saveCheckpoint())
			}
			received += int64(n)
			// The checkpoint is saved on the way out even when the consumer is
			// gone: a cancelled transfer has still made progress worth resuming.
			if !reportProgress(ctx, progress, TransferProgress{
				FileName: meta.Name,
				Bytes:    received,
				Total:    meta.Size,
			}) {
				return errors.Join(ctx.Err(), saveCheckpoint())
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.Join(fmt.Errorf("read stream: %w", err), saveCheckpoint())
		}
	}

	if received != meta.Size {
		return errors.Join(fmt.Errorf("unexpected end of transfer at %d of %d bytes", received, meta.Size), saveCheckpoint())
	}

	// The body has arrived; all that is left is whether these particular bytes are
	// the ones the sender promised. An absent digest was already refused before
	// any of this was read.
	//
	// saveCheckpoint runs before cleanupPartial on purpose: errors.Join evaluates
	// its arguments left to right, so the checkpoint is written and then cleared.
	// The other order would clear it and immediately write it back, leaving the
	// stale resume state this refuses to leave.
	closed = true
	if got := fmt.Sprintf("%x", h.Sum(nil)); !strings.EqualFold(got, meta.Hash) {
		_ = out.Close()
		return errors.Join(
			fmt.Errorf("hash mismatch: expected %s, got %s; nothing was placed", meta.Hash, got),
			saveCheckpoint(), cleanupPartial(outPath, outputDir, code))
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close output file: %w", err)
	}
	closed = true
	// The mode passed to OpenFile only applies on creation and is masked by the
	// process umask, so it is set explicitly here. This is the same reason the
	// push path chmods its staging file: a download that "inherits" 0600 and
	// lands 0644 because of a umask is not inheriting anything.
	if err := os.Chmod(outPath, perm.file); err != nil {
		return errors.Join(fmt.Errorf("set permissions on %s: %w", outPath, err), cleanupPartial(outPath, outputDir, code))
	}
	// Publishing is atomic and refuses an occupied destination. The check above
	// saved the download; this is what decides, because a check-then-rename
	// leaves a window a concurrent download or another process can slip into.
	if err := publishIfAbsent(outPath, finalPath); err != nil {
		return errors.Join(err, cleanupPartial(outPath, outputDir, code))
	}

	if err := storage.ClearResume(outputDir, code); err != nil {
		return fmt.Errorf("clear resume state: %w", err)
	}
	// The file has already landed, so a consumer that has gone away costs
	// nothing to ignore here.
	reportProgress(ctx, progress, TransferProgress{
		FileName: meta.Name,
		Bytes:    received,
		Total:    meta.Size,
		Done:     true,
	})
	return nil
}

// fetchChunkBytes is how much of a file one read-path call pulls. It sits well
// under the read cap: a read opens a stream per call, so a larger chunk means
// fewer streams, but too large a chunk stalls an interactive reader behind a
// big transfer.
const fetchChunkBytes = 4 * 1024 * 1024

// FetchFS pulls the file behind code from a paired peer over the read path,
// resuming any partial download and verifying the result against the hash the
// sender computed when it registered the share.
//
// This is the work receiveFile does, sourced from the fs protocol instead of the
// transfer protocol, so a fetch from a paired device uses one protocol rather
// than two. Resume, progress, and the final full-file hash all carry over,
// because losing them would be a regression the caller never asked for.
func (n *Node) FetchFS(ctx context.Context, pi peer.AddrInfo, code string, outputDir string, progress chan<- TransferProgress) error {
	info, err := n.ResolveShare(ctx, pi, code)
	if err != nil {
		return fmt.Errorf("resolve share: %w", err)
	}
	if info == nil {
		return fmt.Errorf("share %s is not served over the read path", code)
	}
	if err := storage.CheckFileName(info.Name); err != nil {
		return fmt.Errorf("peer sent invalid file name: %w", err)
	}

	// The file name is known here, so the claim can be keyed on the partial path
	// itself. That keeps two fetches of *different* codes into one directory
	// independent while still serialising the ones that would collide.
	release, err := n.claimDownload(filepath.Join(outputDir, code, info.Name))
	if err != nil {
		return err
	}
	defer release()

	// The digest comes from the resolve, so it is known before a single content
	// byte is requested. Refusing here means no read stream is opened and no
	// partial is staged for a transfer that cannot land.
	if info.SHA256 == "" {
		if err := storage.ClearResume(outputDir, code); err != nil {
			return fmt.Errorf("clear resume state: %w", err)
		}
		return noDigestRefusal()
	}

	// The name is known from the resolve, so an occupied destination can be
	// refused before the read stream is even opened.
	finalPath := filepath.Join(outputDir, info.Name)
	occupied, err := destinationExists(finalPath)
	if err != nil {
		return err
	}
	if occupied {
		return errDestinationExists(finalPath)
	}

	// A resume whose file name no longer matches is a different file sharing
	// the same code, so it is discarded rather than mixed in.
	resume, hasResume, err := storage.LoadResume(outputDir, code)
	if err != nil {
		return fmt.Errorf("check resume: %w", err)
	}
	var outPath string
	var offset int64
	if hasResume && filepath.Base(resume.FileName) == info.Name {
		outPath, err = storage.PartialPath(outputDir, code, resume.FileName)
		if err != nil {
			return fmt.Errorf("resume path: %w", err)
		}
		offset = resume.Offset
	}

	// Same reasoning as the share route above: derive the modes from the
	// destination so a created directory and the file inside it inherit rather
	// than defaulting to world-readable.
	perm, err := inheritedPerm(outputDir)
	if err != nil {
		return fmt.Errorf("derive permissions for %s: %w", outputDir, err)
	}
	if err := os.MkdirAll(outputDir, perm.dir); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if outPath == "" {
		outPath, err = storage.PartialPath(outputDir, code, info.Name)
		if err != nil {
			return fmt.Errorf("output path: %w", err)
		}
	}

	flags := os.O_RDWR | os.O_CREATE
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(outPath, flags, perm.file)
	if err != nil {
		return fmt.Errorf("open output file: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
		}
	}()

	// Hash what a previous attempt already wrote, so the final digest covers
	// the whole file rather than only this run's bytes.
	h := sha256.New()
	var received int64
	if offset > 0 {
		fi, err := out.Stat()
		if err != nil {
			return fmt.Errorf("stat output file: %w", err)
		}
		if offset > fi.Size() {
			return fmt.Errorf("resume offset %d is past the end of the output (%d bytes)", offset, fi.Size())
		}
		if err := out.Truncate(offset); err != nil {
			return fmt.Errorf("truncate partial output: %w", err)
		}
		if _, err := out.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seek output for hash: %w", err)
		}
		if _, err := io.CopyN(h, out, offset); err != nil {
			return fmt.Errorf("hash resumed output: %w", err)
		}
		received = offset
		if _, err := out.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("restore output position: %w", err)
		}
	}

	saveCheckpoint := func() error {
		return storage.SaveResume(outputDir, storage.ResumeState{
			Code: code, FileName: info.Name, FileSize: info.Size, Offset: received,
		})
	}
	if err := saveCheckpoint(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, saveCheckpoint())
		}
		readReq := FSRequest{Op: OpRead, Offset: received, Length: fetchChunkBytes}
		// setPath rather than a plain field: the path can hold bytes JSON
		// cannot, and the asking device still has them here.
		readReq.setPath(info.Path)
		_, entry, data, eof, err := n.ReadFS(ctx, pi, readReq)
		if err != nil {
			return errors.Join(fmt.Errorf("read from peer: %w", err), saveCheckpoint())
		}
		if len(data) == 0 {
			break
		}
		if _, err := h.Write(data); err != nil {
			return errors.Join(fmt.Errorf("hash: %w", err), saveCheckpoint())
		}
		if err := protocol.WriteFull(out, data); err != nil {
			return errors.Join(fmt.Errorf("write file: %w", err), saveCheckpoint())
		}
		received += int64(len(data))
		// As in receiveFile: a cancelled fetch still made progress worth
		// resuming, so the checkpoint is saved on the way out.
		if !reportProgress(ctx, progress, TransferProgress{FileName: info.Name, Bytes: received, Total: sizeOf(entry, received)}) {
			return errors.Join(ctx.Err(), saveCheckpoint())
		}

		// The peer's own size is authoritative, not the chunk length: the read
		// caps what it returns, so a short read does not mean the end.
		if eof {
			break
		}
	}

	if info.Size > 0 && received != info.Size {
		return errors.Join(
			fmt.Errorf("unexpected end of fetch at %d of %d bytes", received, info.Size),
			saveCheckpoint())
	}

	// Verify against the sender's own hash, so a corrupted read is reported as
	// a failure rather than landing on disk as a completed file. An absent
	// digest was already refused before the content was requested.
	closed = true
	if got := fmt.Sprintf("%x", h.Sum(nil)); !strings.EqualFold(got, info.SHA256) {
		_ = out.Close()
		return errors.Join(
			fmt.Errorf("hash mismatch: expected %s, got %s; nothing was placed", info.SHA256, got),
			saveCheckpoint(), cleanupPartial(outPath, outputDir, code))
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close output file: %w", err)
	}
	closed = true
	// Explicit, for the same reason as the share route: the mode given to
	// OpenFile applies only on creation and is masked by the process umask.
	if err := os.Chmod(outPath, perm.file); err != nil {
		return errors.Join(fmt.Errorf("set permissions on %s: %w", outPath, err), cleanupPartial(outPath, outputDir, code))
	}
	// Atomic publish, for the same reason as the share route: the check above
	// saved the download, this decides.
	if err := publishIfAbsent(outPath, finalPath); err != nil {
		return errors.Join(err, cleanupPartial(outPath, outputDir, code))
	}
	if err := storage.ClearResume(outputDir, code); err != nil {
		return fmt.Errorf("clear resume state: %w", err)
	}
	// The file has already landed; a consumer that has gone away costs nothing.
	reportProgress(ctx, progress, TransferProgress{FileName: info.Name, Bytes: received, Total: received, Done: true})
	return nil
}

// sizeOf reports the peer's idea of the total, falling back to the bytes already
// received when the entry is missing.
func sizeOf(entry *FSEntry, received int64) int64 {
	if entry == nil || entry.Size <= 0 {
		return received
	}
	return entry.Size
}

func hashFile(file *os.File) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
