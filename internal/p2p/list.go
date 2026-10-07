package p2p

import (
	"context"
	"errors"
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

// ReadRoots resolves the shared roots one peer may read. It is consulted on
// every read and every listing, so what a peer can reach may differ per peer
// and change without a restart.
//
// Returning an empty list refuses the peer. That is how a device paired but
// granted no read access is expressed, and it is why this returns roots rather
// than a bool: "may read" and "may read these" are not the same question.
//
// A nil resolver denies everyone.
func (n *Node) SetReadAccess(resolve ReadRoots) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.readRoots = resolve
}

// ReadRoots resolves the shared roots one peer may read.
type ReadRoots func(peerID string) []string

// rootsFor is the single read gate for both the fs and the list handlers.
func (n *Node) rootsFor(peerID string) []string {
	n.mu.Lock()
	resolve := n.readRoots
	n.mu.Unlock()
	if resolve == nil {
		return nil
	}
	return resolve(peerID)
}

// WritePolicy decides whether a paired peer may write, and where.
//
// Mode is one of the Write* constants. Roots bounds the destinations: a
// write must land inside one of them, exactly as reads are bounded. A nil
// policy denies every write, which is the default for a node that never
// calls SetWritePolicy.
type WritePolicy struct {
	// Mode is WriteDenied or WriteSharedRoots. There is deliberately no mode
	// that widens a destination beyond Roots: a paired device's write standing
	// is always the operator's Roots, narrowed per peer, never something more.
	Mode string
	// Roots bounds destination paths.
	Roots []string
	// MaxBytes caps one write. Zero means DefaultMaxWriteBytes.
	MaxBytes int64
}

// Write modes for SetWriteAccess.
const (
	// WriteDenied refuses every write. The default.
	WriteDenied = "denied"
	// WriteSharedRoots allows writes confined to Roots.
	WriteSharedRoots = "shared-roots"
)

// DefaultMaxWriteBytes caps one push when a policy does not set its own.
const DefaultMaxWriteBytes = 512 * 1024 * 1024

// WriteGrant resolves what one peer may write on this device. It is consulted
// on every OpWrite, so the answer may differ per peer and may change without a
// restart.
//
// Returning an error refuses the write and reports that message to the sender.
// Returning a nil policy also refuses, so a grant that only handles the peers
// it knows about denies everyone else.
type WriteGrant func(peerID string) (*WritePolicy, error)

// SetWriteAccess installs the per-peer write decision.
//
// A node that never calls this serves no writes at all, which is the default
// and the only safe one. There is deliberately no setter for a single
// node-wide policy that grants everyone: that shape is what made one careless
// pairing as good as a deliberate one.
func (n *Node) SetWriteAccess(grant WriteGrant) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.writeGrant = grant
}

// grantWrite resolves the write policy for peerID. The default refuses every
// write, so a node without an explicit grant is read-only.
func (n *Node) grantWrite(peerID string) (*WritePolicy, error) {
	n.mu.Lock()
	grant := n.writeGrant
	n.mu.Unlock()
	if grant == nil {
		return nil, ErrWritesNotPermitted
	}
	pol, err := grant(peerID)
	if err != nil {
		return nil, err
	}
	if pol == nil {
		return nil, ErrWritesNotPermitted
	}
	return pol, nil
}

// ErrWritesNotPermitted is the refusal for a device that has not enabled
// writes at all. Agents and tests match on this wording, so it is stable.
var ErrWritesNotPermitted = errors.New("writes are not permitted on this device; it must be started with write access enabled")

// RegisterListHandler installs the list stream handler (idempotent).
func (n *Node) RegisterListHandler() {
	n.listOnce.Do(func() {
		n.Host.SetStreamHandler(ListProtocolID, n.serveList)
	})
}

func (n *Node) serveList(s network.Stream) {
	defer s.Close()
	remote := s.Conn().RemotePeer().String()

	roots := n.rootsFor(remote)
	if len(roots) == 0 {
		_ = protocol.WriteMetadata(s, ListResponse{Error: "not paired for read access"})
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
	resolved, err := resolveWithinRoots(roots, clean)
	if err != nil {
		return nil, err
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

// resolveWithinRoots canonicalizes path (absolute + symlinks evaluated)
// and confirms it sits inside one of roots. Every filesystem operation the
// node serves goes through here, so a shared root is a hard boundary that
// neither a listing nor a read can step outside of.
func resolveWithinRoots(roots []string, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	for _, r := range roots {
		rabs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		rres, err := filepath.EvalSymlinks(rabs)
		if err != nil {
			// Unresolvable root (missing dir): compare against abs path.
			rres = rabs
		}
		if pathWithin(resolved, rres) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path %q is outside shared dirs", path)
}

// pathWithin reports whether path is root itself or sits beneath it.
//
// The comparison is case-insensitive on Windows and macOS, because their
// filesystems are. Relying on EvalSymlinks to normalise the casing of both
// sides works today, but EvalSymlinks is documented as unreliable on Windows
// and this is a security boundary: it must hold on its own terms rather than
// because of a side effect somewhere else.
func pathWithin(path, root string) bool {
	if equalPath(path, root) {
		return true
	}
	sep := string(os.PathSeparator)
	// A filesystem root already ends in the separator. Appending another
	// would build "//", which no path begins with, and an operator who
	// deliberately shares "/" would find nothing readable at all.
	if strings.HasSuffix(root, sep) {
		return len(path) > len(root) && equalPath(path[:len(root)], root)
	}
	if len(path) <= len(root)+len(sep) {
		return false
	}
	// Compare the root plus a separator, so /srv/rooted cannot match the
	// root /srv/root just because they share a prefix.
	return equalPath(path[:len(root)+len(sep)], root+sep)
}

// equalPath compares two paths under the rules of the host filesystem.
func equalPath(a, b string) bool {
	if caseInsensitiveFS {
		return strings.EqualFold(a, b)
	}
	return a == b
}
