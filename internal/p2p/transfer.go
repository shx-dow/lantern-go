package p2p

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

type FileMeta struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
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
			Code string `json:"code"`
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

		meta := FileMeta{
			Name: filepath.Base(state.filePath),
			Size: fi.Size(),
		}

		enc := json.NewEncoder(s)
		if err := enc.Encode(meta); err != nil {
			progress <- TransferProgress{Err: fmt.Errorf("send meta: %w", err)}
			return
		}

		buf := make([]byte, 32*1024)
		sent := int64(0)
		for {
			n, err := file.Read(buf)
			if n > 0 {
				if _, werr := s.Write(buf[:n]); werr != nil {
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

	req := struct {
		Code string `json:"code"`
	}{Code: code}
	enc := json.NewEncoder(s)
	if err := enc.Encode(req); err != nil {
		return fmt.Errorf("send request: %w", err)
	}

	dec := json.NewDecoder(s)
	var meta FileMeta
	if err := dec.Decode(&meta); err != nil {
		return fmt.Errorf("read meta: %w", err)
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	outPath := filepath.Join(outputDir, meta.Name)
	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer out.Close()

	received := int64(0)
	reader := bufio.NewReaderSize(s, 32*1024)
	buf := make([]byte, 32*1024)

	for received < meta.Size {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := reader.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
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
			return fmt.Errorf("read stream: %w", err)
		}
	}

	progress <- TransferProgress{
		FileName: meta.Name,
		Bytes:    received,
		Total:    meta.Size,
		Done:     true,
	}
	return nil
}
