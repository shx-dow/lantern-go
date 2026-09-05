package p2p

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/crypto"
	"github.com/shx-dow/lantern-go/internal/protocol"
	"github.com/shx-dow/lantern-go/internal/storage"
)

type FileMeta struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Hash string `json:"hash,omitempty"`
}

type TransferProgress struct {
	PeerID   string
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
	fi, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("stat file: %w", err)
	}
	if fi.IsDir() {
		file.Close()
		return fmt.Errorf("path is a directory: %s", path)
	}
	fullHash, err := hashFile(file)
	file.Close()
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
	if len(req.Challenge) != 32 || len(req.Proof) == 0 {
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
	defer close(progress)
	defer state.done()
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
		buf := make([]byte, 32*1024)
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
	defer n.mu.Unlock()
	for _, state := range n.shares {
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

	challenge := make([]byte, 32)
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

	if outPath == "" {
		outPath, err = storage.PartialPath(outputDir, code, meta.Name)
		if err != nil {
			return fmt.Errorf("output path: %w", err)
		}
	} else if filepath.Base(meta.Name) != filepath.Base(resume.FileName) {
		return fmt.Errorf("resume file name changed from %s to %s", resume.FileName, meta.Name)
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	flags := os.O_RDWR | os.O_CREATE
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(outPath, flags, 0644)
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

	if err := storage.SaveResume(outputDir, storage.ResumeState{Code: code, FileName: meta.Name, FileSize: meta.Size, Offset: received}); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	buf := make([]byte, 32*1024)
	for received < meta.Size {
		select {
		case <-ctx.Done():
			storage.SaveResume(outputDir, storage.ResumeState{Code: code, FileName: meta.Name, FileSize: meta.Size, Offset: received})
			return ctx.Err()
		default:
		}

		n, err := er.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if werr := protocol.WriteFull(out, buf[:n]); werr != nil {
				storage.SaveResume(outputDir, storage.ResumeState{Code: code, FileName: meta.Name, FileSize: meta.Size, Offset: received})
				return fmt.Errorf("write file: %w", werr)
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
			storage.SaveResume(outputDir, storage.ResumeState{Code: code, FileName: meta.Name, FileSize: meta.Size, Offset: received})
			return fmt.Errorf("read stream: %w", err)
		}
	}

	if received != meta.Size {
		storage.SaveResume(outputDir, storage.ResumeState{Code: code, FileName: meta.Name, FileSize: meta.Size, Offset: received})
		return fmt.Errorf("unexpected end of transfer at %d of %d bytes", received, meta.Size)
	}

	if meta.Hash != "" {
		got := fmt.Sprintf("%x", h.Sum(nil))
		if got != meta.Hash {
			return fmt.Errorf("hash mismatch: expected %s, got %s", meta.Hash, got)
		}
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
