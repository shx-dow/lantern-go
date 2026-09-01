package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
)

func (n *Node) peerFilePath(code string) string {
	dir := n.localDir
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "lantern-"+code+".peer")
}

func (n *Node) AdvertiseLocal(code string) error {
	info := peer.AddrInfo{
		ID:    n.Host.ID(),
		Addrs: n.Host.Addrs(),
	}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(os.TempDir(), "lantern-peer-*")
	if err != nil {
		return fmt.Errorf("create local advertisement: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect local advertisement: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write local advertisement: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close local advertisement: %w", err)
	}
	if err := os.Rename(tmpPath, n.peerFilePath(code)); err != nil {
		return fmt.Errorf("publish local advertisement: %w", err)
	}
	return nil
}

func (n *Node) DiscoverLocal(ctx context.Context, code string) (*peer.AddrInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	data, err := os.ReadFile(n.peerFilePath(code))
	if err != nil {
		return nil, err
	}
	var info peer.AddrInfo
	if err := json.Unmarshal(data, &info); err != nil {
		os.Remove(n.peerFilePath(code))
		return nil, err
	}
	return &info, nil
}

func (n *Node) ClearLocal(code string) {
	os.Remove(n.peerFilePath(code))
}

func (n *Node) Advertise(ctx context.Context, code string) error {
	c := codeToCID(code)

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		if err := n.DHT.Provide(ctx, c, true); err != nil {
			return fmt.Errorf("advertise: %w", err)
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (n *Node) Discover(ctx context.Context, code string) (peer.AddrInfo, error) {
	c := codeToCID(code)

	deadline, ok := ctx.Deadline()
	if !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		deadline, _ = ctx.Deadline()
	}

	for {
		if pi, err := n.DiscoverLocal(ctx, code); err == nil {
			go n.ClearLocal(code)
			return *pi, nil
		}

		dhtCtx, dhtCancel := context.WithTimeout(ctx, 3*time.Second)
		peers, err := n.DHT.FindProviders(dhtCtx, c)
		dhtCancel()
		if err == nil {
			for _, p := range peers {
				if p.ID == n.Host.ID() {
					continue
				}
				return p, nil
			}
		}

		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return peer.AddrInfo{}, fmt.Errorf("no peers found for code: %s (timeout)", code)
		}

		if time.Now().After(deadline) {
			return peer.AddrInfo{}, fmt.Errorf("no peers found for code: %s (timeout)", code)
		}
	}
}

func (n *Node) AddrInfo() peer.AddrInfo {
	return peer.AddrInfo{
		ID:    n.Host.ID(),
		Addrs: n.Host.Addrs(),
	}
}

func codeToCID(code string) cid.Cid {
	h, err := multihash.Sum([]byte(code), multihash.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, h)
}
