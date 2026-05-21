package p2p

import (
	"context"
	"fmt"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
)

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
		peers, err := n.DHT.FindProviders(ctx, c)
		if err != nil {
			return peer.AddrInfo{}, fmt.Errorf("find providers: %w", err)
		}
		for _, p := range peers {
			if p.ID == n.Host.ID() {
				continue
			}
			return p, nil
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
