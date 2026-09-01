package p2p

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/multiformats/go-multiaddr"
)

const ProtocolID = "/lantern/transfer/2.0.0"

var DefaultBootstrapPeers = []string{
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
}

type Node struct {
	Host     host.Host
	DHT      *dht.IpfsDHT
	ctx      context.Context
	cancel   context.CancelFunc
	localDir string
}

func NewNode(port int, bootstrapPeers []string, dataDirs ...string) (*Node, error) {
	ctx, cancel := context.WithCancel(context.Background())
	localDir := os.TempDir()
	if len(dataDirs) > 0 && dataDirs[0] != "" {
		localDir = dataDirs[0]
	}
	if err := os.MkdirAll(localDir, 0700); err != nil {
		cancel()
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	tcpAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port)
	quicAddr := fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", port)
	if port == 0 {
		tcpAddr = "/ip4/0.0.0.0/tcp/0"
		quicAddr = "/ip4/0.0.0.0/udp/0/quic-v1"
	}

	h, err := libp2p.New(
		libp2p.ListenAddrStrings(tcpAddr, quicAddr),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
		libp2p.NATPortMap(),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create host: %w", err)
	}

	var dhtOpts []dht.Option
	dhtOpts = append(dhtOpts, dht.Mode(dht.ModeServer))

	if len(bootstrapPeers) == 0 {
		bootstrapPeers = DefaultBootstrapPeers
	}

	for _, bp := range bootstrapPeers {
		ma, err := multiaddr.NewMultiaddr(bp)
		if err != nil {
			continue
		}
		ai, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			continue
		}
		dhtOpts = append(dhtOpts, dht.BootstrapPeers(*ai))
	}

	d, err := dht.New(ctx, h, dhtOpts...)
	if err != nil {
		h.Close()
		cancel()
		return nil, fmt.Errorf("create dht: %w", err)
	}

	if err := d.Bootstrap(ctx); err != nil {
		h.Close()
		cancel()
		return nil, fmt.Errorf("bootstrap dht: %w", err)
	}

	node := &Node{
		Host:     h,
		DHT:      d,
		ctx:      ctx,
		cancel:   cancel,
		localDir: localDir,
	}

	node.setupMDNS()
	return node, nil
}

func (n *Node) setupMDNS() {
	s := mdns.NewMdnsService(n.Host, "lantern", &mdnsDiscovery{node: n})
	if err := s.Start(); err != nil {
		fmt.Printf("mDNS warning: %v\n", err)
	}
}

type mdnsDiscovery struct {
	node *Node
}

func (m *mdnsDiscovery) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == m.node.Host.ID() {
		return
	}
	go func() {
		if err := m.node.Host.Connect(m.node.ctx, pi); err != nil {
			return
		}
	}()
}

func (n *Node) Close() error {
	n.cancel()
	var dhtErr, hostErr error
	if n.DHT != nil {
		dhtErr = n.DHT.Close()
	}
	if n.Host != nil {
		hostErr = n.Host.Close()
	}
	return errors.Join(dhtErr, hostErr)
}

func GenerateCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	return hex.EncodeToString(b), nil
}
