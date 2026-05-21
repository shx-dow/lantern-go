package p2p

import (
	"context"
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
)

type FileMeta struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Hash   string `json:"hash,omitempty"`
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
	handled  atomic.Bool
	progress chan TransferProgress
}

func (n *Node) RegisterShareHandler(code string, path string, progress chan TransferProgress) {
	state := &shareState{code: code, filePath: path, progress: progress}
	state.handled.Store(false)

	n.Host.SetStreamHandler(ProtocolID, func(s network.Stream) {
		defer s.Close()

		if !state.handled.CompareAndSwap(false, true) {
			return
		}

		dec := json.NewDecoder(s)
		var req struct {
			Code   string `json:"code"`
			Offset int64  `json:"offset,omitempty"`
		}
		if err := dec.Decode(&req); err != nil {
			progress <- TransferProgress{Err: fmt.Errorf("read request: %w", err)}
			return
		}
		if req.Code != state.code {
			progress <- TransferProgress{Err: fmt.Errorf("wrong code: expected %s, got %s", state.code, req.Code)}
			return
		}

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

		var startChunk int64
		if req.Offset > 0 {
			if _, err := file.Seek(req.Offset, io.SeekStart); err != nil {
				progress <- TransferProgress{Err: fmt.Errorf("seek: %w", err)}
				return
			}
			startChunk = req.Offset / int64(crypto.ChunkSize)
		}

		key, err := crypto.DeriveKey(state.code)
		if err != nil {
			progress <- TransferProgress{Err: fmt.Errorf("derive key: %w", err)}
			return
		}

		ew, err := crypto.NewEncryptedWriterAt(s, key, startChunk)
		if err != nil {
			progress <- TransferProgress{Err: fmt.Errorf("create encrypt: %w", err)}
			return
		}

		h := sha256.New()
		meta := FileMeta{
			Name: filepath.Base(state.filePath),
			Size: fi.Size(),
		}

		enc := json.NewEncoder(ew)
		if err := enc.Encode(meta); err != nil {
			progress <- TransferProgress{Err: fmt.Errorf("send meta: %w", err)}
			return
		}

		sent := req.Offset
		buf := make([]byte, 32*1024)
		for {
			n, err := file.Read(buf)
			if n > 0 {
				h.Write(buf[:n])
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

		meta.Hash = fmt.Sprintf("%x", h.Sum(nil))
		progress <- TransferProgress{
			FileName: meta.Name,
			Bytes:    sent,
			Total:    meta.Size,
			Done:     true,
		}
	})
}

func (n *Node) RegisterReceive(ctx context.Context, pi peer.AddrInfo, code string, outputDir string, progress chan TransferProgress) {
	go func() {
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

	outPath, offset, err := checkResume(outputDir, code)
	if err != nil {
		return fmt.Errorf("check resume: %w", err)
	}

	req := struct {
		Code   string `json:"code"`
		Offset int64  `json:"offset,omitempty"`
	}{Code: code, Offset: offset}
	enc := json.NewEncoder(s)
	if err := enc.Encode(req); err != nil {
		return fmt.Errorf("send request: %w", err)
	}

	key, err := crypto.DeriveKey(code)
	if err != nil {
		return fmt.Errorf("derive key: %w", err)
	}

	var startChunk int64
	if offset > 0 {
		startChunk = offset / int64(crypto.ChunkSize)
	}

	er, err := crypto.NewEncryptedReaderAt(s, key, startChunk)
	if err != nil {
		return fmt.Errorf("create decrypt: %w", err)
	}

	dec := json.NewDecoder(er)
	var meta FileMeta
	if err := dec.Decode(&meta); err != nil {
		return fmt.Errorf("read meta: %w", err)
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	out, err := os.OpenFile(outPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open output file: %w", err)
	}
	defer out.Close()

	if offset > 0 {
		if _, err := out.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("seek output: %w", err)
		}
	}

	h := sha256.New()
	received := offset

	if err := saveState(outputDir, code, meta.Name, meta.Size, received); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	buf := make([]byte, 32*1024)
	for received < meta.Size {
		select {
		case <-ctx.Done():
			saveState(outputDir, code, meta.Name, meta.Size, received)
			return ctx.Err()
		default:
		}

		n, err := er.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if _, werr := out.Write(buf[:n]); werr != nil {
				saveState(outputDir, code, meta.Name, meta.Size, received)
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
			saveState(outputDir, code, meta.Name, meta.Size, received)
			return fmt.Errorf("read stream: %w", err)
		}
	}

	if meta.Hash != "" {
		got := fmt.Sprintf("%x", h.Sum(nil))
		if got != meta.Hash {
			return fmt.Errorf("hash mismatch: expected %s, got %s", meta.Hash, got)
		}
	}

	clearState(outputDir, code)
	progress <- TransferProgress{
		FileName: meta.Name,
		Bytes:    received,
		Total:    meta.Size,
		Done:     true,
	}
	return nil
}

type resumeState struct {
	Code     string `json:"code"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	Offset   int64  `json:"offset"`
}

func statePath(outputDir, code string) string {
	return filepath.Join(outputDir, "."+code+".lantern-state")
}

func checkResume(outputDir, code string) (string, int64, error) {
	sp := statePath(outputDir, code)
	data, err := os.ReadFile(sp)
	if err != nil {
		return filepath.Join(outputDir, ""), 0, nil
	}

	var st resumeState
	if err := json.Unmarshal(data, &st); err != nil {
		return filepath.Join(outputDir, ""), 0, nil
	}

	if st.Code != code {
		os.Remove(sp)
		return filepath.Join(outputDir, ""), 0, nil
	}

	outPath := filepath.Join(outputDir, st.FileName)
	if _, err := os.Stat(outPath); err != nil {
		os.Remove(sp)
		return filepath.Join(outputDir, ""), 0, nil
	}

	return outPath, st.Offset, nil
}

func saveState(outputDir, code, fileName string, fileSize, offset int64) error {
	st := resumeState{
		Code:     code,
		FileName: fileName,
		FileSize: fileSize,
		Offset:   offset,
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(outputDir, code), data, 0644)
}

func clearState(outputDir, code string) {
	os.Remove(statePath(outputDir, code))
}
