package p2p

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

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

type shareState struct {
	code     string
	filePath string
	fileName string
	fileSize int64
	fileHash string
	handled  atomic.Bool
	progress chan TransferProgress
	done     func()
}

// RegisterShareHandler advertises path under code to any receiver that
// proves knowledge of the code. The file is hashed up front so serving a
// stream never blocks on I/O. Each code serves at most one receiver.
func (n *Node) RegisterShareHandler(code string, path string, progress chan TransferProgress, done ...func()) error {
	if code == "" {
		return fmt.Errorf("share code must not be empty")
	}
	if progress == nil {
		return fmt.Errorf("progress channel must not be nil")
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

	state := &shareState{
		code:     code,
		filePath: path,
		fileName: filepath.Base(path),
		fileSize: fi.Size(),
		fileHash: fmt.Sprintf("%x", fullHash),
		progress: progress,
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
		progress <- TransferProgress{Err: fmt.Errorf("open file: %w", err)}
		return
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		progress <- TransferProgress{Err: fmt.Errorf("stat file: %w", err)}
		return
	}
	if fi.Size() != state.fileSize {
		progress <- TransferProgress{Err: fmt.Errorf("file changed during share: was %d bytes, now %d", state.fileSize, fi.Size())}
		return
	}

	if req.Offset > fi.Size() {
		progress <- TransferProgress{Err: fmt.Errorf("resume offset %d exceeds file size %d", req.Offset, fi.Size())}
		return
	}
	if _, err := file.Seek(req.Offset, io.SeekStart); err != nil {
		progress <- TransferProgress{Err: fmt.Errorf("seek: %w", err)}
		return
	}

	key, err := crypto.DeriveKey(state.code)
	if err != nil {
		progress <- TransferProgress{Err: fmt.Errorf("derive key: %w", err)}
		return
	}

	ew, err := crypto.NewEncryptedWriter(s, key)
	if err != nil {
		progress <- TransferProgress{Err: fmt.Errorf("create encrypt: %w", err)}
		return
	}

	meta := FileMeta{
		Name: state.fileName,
		Size: state.fileSize,
		Hash: state.fileHash,
	}

	if err := protocol.WriteMetadata(ew, meta); err != nil {
		progress <- TransferProgress{Err: fmt.Errorf("send meta: %w", err)}
		return
	}

	sent := req.Offset
	buf := make([]byte, ioBufSize)
	for {
		n, err := file.Read(buf)
		if n > 0 {
			if _, werr := ew.Write(buf[:n]); werr != nil {
				progress <- TransferProgress{Err: fmt.Errorf("write stream: %w", werr)}
				return
			}
			sent += int64(n)
			progress <- TransferProgress{
				FileName: meta.Name,
				Bytes:    sent,
				Total:    meta.Size,
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			progress <- TransferProgress{Err: fmt.Errorf("read file: %w", err)}
			return
		}
	}

	if err := ew.Close(); err != nil {
		progress <- TransferProgress{Err: fmt.Errorf("close encrypt: %w", err)}
		return
	}

	progress <- TransferProgress{
		FileName: meta.Name,
		Bytes:    sent,
		Total:    meta.Size,
		Done:     true,
	}
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
			progress <- TransferProgress{Err: err}
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

	if err := os.MkdirAll(outputDir, storage.PublicDirPerm); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	flags := os.O_RDWR | os.O_CREATE
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(outPath, flags, storage.PublicFilePerm)
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
			progress <- TransferProgress{
				FileName: meta.Name,
				Bytes:    received,
				Total:    meta.Size,
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
	finalPath := filepath.Join(outputDir, filepath.Base(meta.Name))
	if err := os.Rename(outPath, finalPath); err != nil {
		return fmt.Errorf("finalize output file: %w", err)
	}

	if err := storage.ClearResume(outputDir, code); err != nil {
		return fmt.Errorf("clear resume state: %w", err)
	}
	progress <- TransferProgress{
		FileName: meta.Name,
		Bytes:    received,
		Total:    meta.Size,
		Done:     true,
	}
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

	if err := os.MkdirAll(outputDir, storage.PublicDirPerm); err != nil {
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
	out, err := os.OpenFile(outPath, flags, storage.PublicFilePerm)
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
		_, entry, data, eof, err := n.ReadFS(ctx, pi, FSRequest{
			Op: OpRead, Path: info.Path, Offset: received, Length: fetchChunkBytes,
		})
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
		progress <- TransferProgress{FileName: info.Name, Bytes: received, Total: sizeOf(entry, received)}

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
	if err := os.Rename(outPath, filepath.Join(outputDir, info.Name)); err != nil {
		return fmt.Errorf("finalize output file: %w", err)
	}
	if err := storage.ClearResume(outputDir, code); err != nil {
		return fmt.Errorf("clear resume state: %w", err)
	}
	progress <- TransferProgress{FileName: info.Name, Bytes: received, Total: received, Done: true}
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
