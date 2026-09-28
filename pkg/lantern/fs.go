package lantern

import (
	"context"
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shx-dow/lantern-go/internal/p2p"
)

// Entry is one file or directory reported by a remote filesystem call.
type Entry = p2p.FSEntry

// ReadResult is one byte range pulled from a paired device.
type ReadResult struct {
	Entry  Entry
	Data   []byte
	Offset int64
	EOF    bool
}

// resolvePeer turns a peer reference into a dialable address. It prefers a
// live connection, then the peerstore, so a warm call is instant and a cold
// one still dials instead of failing outright. Callers that need a strict
// "must already be connected" check should look at Peers instead.
func (l *Lantern) resolvePeer(ref string) (peer.AddrInfo, error) {
	if l.node == nil || l.node.Host == nil {
		return peer.AddrInfo{}, fmt.Errorf("node not ready")
	}
	id, err := peer.Decode(strings.TrimSpace(ref))
	if err != nil {
		return peer.AddrInfo{}, fmt.Errorf("invalid peer ID: %w", err)
	}
	host := l.node.Host
	for _, p := range host.Network().Peers() {
		if p == id {
			return peer.AddrInfo{ID: p, Addrs: host.Peerstore().Addrs(p)}, nil
		}
	}
	if addrs := host.Peerstore().Addrs(id); len(addrs) > 0 {
		return peer.AddrInfo{ID: id, Addrs: addrs}, nil
	}
	return peer.AddrInfo{}, fmt.Errorf("peer %s is not known; pair it or bring it online first", id)
}

// ReadRemote pulls offset..offset+length from path on a paired device. A
// zero length reads to the end of file, capped by the protocol's default.
// Fetch is this call plus a write to disk.
func (l *Lantern) ReadRemote(ctx context.Context, ref, path string, offset, length int64) (ReadResult, error) {
	pi, err := l.resolvePeer(ref)
	if err != nil {
		return ReadResult{}, err
	}
	_, entry, data, eof, err := l.node.ReadFS(ctx, pi, p2p.FSRequest{
		Op:     p2p.OpRead,
		Path:   path,
		Offset: offset,
		Length: length,
	})
	if err != nil {
		return ReadResult{}, err
	}
	var head Entry
	if entry != nil {
		head = *entry
	}
	return ReadResult{Entry: head, Data: data, Offset: offset, EOF: eof}, nil
}

// StatRemote returns metadata for one path on a paired device.
func (l *Lantern) StatRemote(ctx context.Context, ref, path string) (Entry, error) {
	pi, err := l.resolvePeer(ref)
	if err != nil {
		return Entry{}, err
	}
	_, entry, _, _, err := l.node.ReadFS(ctx, pi, p2p.FSRequest{Op: p2p.OpStat, Path: path})
	if err != nil {
		return Entry{}, err
	}
	if entry == nil {
		return Entry{}, fmt.Errorf("remote: no entry for %q", path)
	}
	return *entry, nil
}

// PushResult describes what a remote stored.
type PushResult struct {
	Entry  Entry
	Bytes  int64
	SHA256 string
}

// PushRemote writes content to path on a paired device. The sending device
// dials the receiver and writes directly, so an agent holding a file can
// place it without the receiver having to ask for it.
//
// Whether the write lands is entirely the remote's decision: its write
// policy, its overwrite setting, and its shared roots all apply, and a
// refusal comes back before any content is sent.
func (l *Lantern) PushRemote(ctx context.Context, ref, path string, content []byte, overwrite bool) (PushResult, error) {
	pi, err := l.resolvePeer(ref)
	if err != nil {
		return PushResult{}, err
	}
	entry, sum, err := l.node.WriteFS(ctx, pi, path, content, overwrite)
	if err != nil {
		return PushResult{}, err
	}
	return PushResult{Entry: entry, Bytes: entry.Size, SHA256: sum}, nil
}

// ListRemoteEntries lists one directory level on a paired device. Empty dir
// lists the remote's shared roots.
func (l *Lantern) ListRemoteEntries(ctx context.Context, ref, dir string) ([]Entry, error) {
	pi, err := l.resolvePeer(ref)
	if err != nil {
		return nil, err
	}
	entries, _, _, _, err := l.node.ReadFS(ctx, pi, p2p.FSRequest{Op: p2p.OpList, Path: dir})
	if err != nil {
		return nil, err
	}
	return entries, nil
}
