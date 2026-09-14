package p2p

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/protocol"
)

// ListProtocolID is the libp2p stream protocol for remote file listings.
// It is separate from the transfer protocol so listings stay small,
// read-only, and gated by pairing without touching share codes.
const ListProtocolID = "/lantern/list/1.0.0"

// ListEntry describes one file or directory in a listing.
type ListEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
	IsDir   bool   `json:"is_dir"`
}

// ListRequest asks for one level of dir ("": list the shared roots).
type ListRequest struct {
	Dir string `json:"dir,omitempty"`
}

// ListResponse carries the listing or an error string.
type ListResponse struct {
	Files []ListEntry `json:"files,omitempty"`
	Error string      `json:"error,omitempty"`
}

const (
	maxListEntries = 1000
	listIOTimeout  = 15 * time.Second
)

// SetListAccess configures the read-only roots served by the list handler
// and the pairing gate. A nil isTrusted denies everyone.
func (n *Node) SetListAccess(roots []string, isTrusted func(peerID string) bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.listRoots = append([]string(nil), roots...)
	n.listTrusted = isTrusted
}

// RegisterListHandler installs the list stream handler (idempotent).
func (n *Node) RegisterListHandler() {
	n.listOnce.Do(func() {
		n.Host.SetStreamHandler(ListProtocolID, n.serveList)
	})
}

func (n *Node) serveList(s network.Stream) {
	defer s.Close()
	remote := s.Conn().RemotePeer().String()

	n.mu.Lock()
	roots := append([]string(nil), n.listRoots...)
	check := n.listTrusted
	n.mu.Unlock()
	if check == nil || !check(remote) {
		_ = protocol.WriteMetadata(s, ListResponse{Error: "not paired"})
		return
	}

	if err := s.SetDeadline(time.Now().Add(listIOTimeout)); err != nil {
		return
	}
	var req ListRequest
	if err := protocol.ReadMetadata(s, &req); err != nil {
		_ = protocol.WriteMetadata(s, ListResponse{Error: "bad request"})
		return
	}
	files, err := listRootsLocal(roots, req.Dir)
	if err != nil {
		_ = protocol.WriteMetadata(s, ListResponse{Error: err.Error()})
		return
	}
	_ = protocol.WriteMetadata(s, ListResponse{Files: files})
}

// ListRemote dials pi and returns one level of dir ("": remote roots).
func (n *Node) ListRemote(ctx context.Context, pi peer.AddrInfo, dir string) ([]ListEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, listIOTimeout)
	defer cancel()
	if err := n.Host.Connect(ctx, pi); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	s, err := n.Host.NewStream(ctx, pi.ID, ListProtocolID)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	defer s.Close()
	if err := s.SetDeadline(time.Now().Add(listIOTimeout)); err != nil {
		return nil, err
	}
	if err := protocol.WriteMetadata(s, ListRequest{Dir: dir}); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	var resp ListResponse
	if err := protocol.ReadMetadata(s, &resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("remote: %s", resp.Error)
	}
	return resp.Files, nil
}

func listRootsLocal(roots []string, dir string) ([]ListEntry, error) {
	clean := filepath.Clean(strings.TrimSpace(dir))
	if clean == "." || clean == "" {
		out := make([]ListEntry, 0, len(roots))
		for _, r := range roots {
			abs, err := filepath.Abs(r)
			if err != nil {
				continue
			}
			fi, err := os.Stat(abs)
			if err != nil {
				continue
			}
			out = append(out, ListEntry{Name: abs, ModTime: fi.ModTime().UTC().Format(time.RFC3339), IsDir: fi.IsDir()})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out, nil
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return nil, fmt.Errorf("resolve dir: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve dir: %w", err)
	}
	allowed := false
	for _, r := range roots {
		rabs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		rres, err := filepath.EvalSymlinks(rabs)
		if err != nil {
			rres = rabs
		}
		if resolved == rres || strings.HasPrefix(resolved, rres+string(os.PathSeparator)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("dir %q is outside shared dirs", dir)
	}
	ents, err := os.ReadDir(resolved)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	out := make([]ListEntry, 0, len(ents))
	for _, e := range ents {
		if len(out) >= maxListEntries {
			break
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, ListEntry{Name: e.Name(), Size: fi.Size(), ModTime: fi.ModTime().UTC().Format(time.RFC3339), IsDir: e.IsDir()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
