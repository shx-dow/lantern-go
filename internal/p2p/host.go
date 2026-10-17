package p2p

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/multiformats/go-multiaddr"
	"github.com/shx-dow/lantern-go/internal/paths"
	"github.com/shx-dow/lantern-go/internal/storage"
)

// ProtocolID is the libp2p stream protocol for transfers; bump it for
// any wire-incompatible handshake or framing change.
const ProtocolID = "/lantern/transfer/2.0.0"

// DefaultBootstrapPeers are the public libp2p DHT bootstraps used when
// the caller supplies none.
var DefaultBootstrapPeers = []string{
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
}

// Node is a libp2p host with DHT discovery and transfer handlers.
// Construct via NewNode; Host and DHT stay exported for dialing and
// providing until the transport seam is extracted.
type Node struct {
	Host     host.Host
	DHT      *dht.IpfsDHT
	ctx      context.Context
	cancel   context.CancelFunc
	localDir string

	mu          sync.Mutex
	shares      map[string]*shareState
	shareSeq    uint64
	handlerOnce sync.Once

	listOnce  sync.Once
	readRoots func(peerID string) []string

	fsOnce     sync.Once
	writeGrant WriteGrant

	// downloads holds the in-flight download keys, so two downloads that resolve
	// to the same partial file cannot overlap. See claimDownload.
	downloadsMu sync.Mutex
	downloads   map[string]struct{}
}

// NodeOption configures a Node at construction.
type NodeOption func(*nodeOptions)

type nodeOptions struct {
	dialTimeout time.Duration
	noMDNS      bool
}

// WithoutMDNS brings the node up with no multicast announcement.
//
// A node normally announces itself on the LAN so other devices can find it,
// which is what mDNS is for. That is wrong for a host a test brings up: it
// advertises a throwaway identity on whatever network the machine running the
// tests happens to be on, and it competes for the same multicast traffic as
// every other test process. Two consequences, both observed -- tests that pass
// alone failing in a loop, and a laptop running the suite visible to the rest
// of the office.
//
// Nothing that does not already announce can find this node, so a caller using
// this is expected to address the node directly, by a known address or by a
// share code.
func WithoutMDNS() NodeOption {
	return func(o *nodeOptions) { o.noMDNS = true }
}

// WithDialTimeout sets how long a single dial to one address may take.
//
// Zero, the default, leaves go-libp2p's own limits alone: 15 seconds, and 5
// for a private address. Those limits apply inside the swarm and no context the
// caller passes can raise them, because every FS operation already caps its
// own context at fsiOTimeout — and context.WithTimeout with a later deadline
// than its parent does nothing. So a caller that needs longer has to change
// the option, not the context.
func WithDialTimeout(d time.Duration) NodeOption {
	return func(o *nodeOptions) { o.dialTimeout = d }
}

// NewNode brings up a TCP+QUIC host with relay, hole punching, DHT in
// server mode, and mDNS. key is the host identity: pass a persisted key
// (LoadOrCreatePrivKey) for a stable peer ID, or nil for an ephemeral
// in-memory identity. dataDir holds local advertisements.
func NewNode(port int, bootstrapPeers []string, key crypto.PrivKey, dataDir string, opts ...NodeOption) (*Node, error) {
	var o nodeOptions
	for _, fn := range opts {
		fn(&o)
	}
	ctx, cancel := context.WithCancel(context.Background())
	localDir := dataDir
	if localDir == "" {
		localDir = paths.Data()
	}
	if err := os.MkdirAll(localDir, storage.PrivateDirPerm); err != nil {
		cancel()
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	tcpAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port)
	quicAddr := fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", port)
	if port == 0 {
		tcpAddr = "/ip4/0.0.0.0/tcp/0"
		quicAddr = "/ip4/0.0.0.0/udp/0/quic-v1"
	}

	priv := key
	if priv == nil {
		var err error
		priv, _, err = crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("generate ephemeral identity: %w", err)
		}
	}

	hostOpts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(tcpAddr, quicAddr),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
		libp2p.NATPortMap(),
	}
	if o.dialTimeout > 0 {
		// Both, not just the general timeout: the swarm picks the shorter of
		// the two for a private address, and every device on the same LAN —
		// which is the case Lantern is built for — has only private addresses.
		hostOpts = append(hostOpts, libp2p.SwarmOpts(
			swarm.WithDialTimeout(o.dialTimeout),
			swarm.WithDialTimeoutLocal(o.dialTimeout),
		))
	}

	h, err := libp2p.New(hostOpts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create host: %w", err)
	}

	var dhtOpts []dht.Option
	dhtOpts = append(dhtOpts, dht.Mode(dht.ModeServer))

	// The sentinel BootstrapPeers{"none"} selects LAN-only mode: no public
	// bootstraps, mDNS plus local advertisements only. An empty slice
	// keeps the historical default of the public bootstraps.
	lanOnly := len(bootstrapPeers) == 1 && bootstrapPeers[0] == "none"
	if lanOnly {
		bootstrapPeers = nil
	} else if len(bootstrapPeers) == 0 {
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
		cancel()
		return nil, errors.Join(fmt.Errorf("create dht: %w", err), h.Close())
	}

	if err := d.Bootstrap(ctx); err != nil {
		cancel()
		return nil, errors.Join(fmt.Errorf("bootstrap dht: %w", err), h.Close())
	}

	node := &Node{
		Host:     h,
		DHT:      d,
		ctx:      ctx,
		cancel:   cancel,
		localDir: localDir,
	}

	if !o.noMDNS {
		startMDNS(node)
	}
	return node, nil
}

// startMDNS announces a node on the LAN. It is a variable so a test can see
// whether the option reached it.
//
// The first version of this asserted on a field the starter set, which broke on
// any runner where mDNS fails to start: the announcement never began, the field
// stayed false, and the test failed even though WithoutMDNS had done its job.
// Asking whether the starter was called does not depend on multicast working
// where the test runs.
var startMDNS = func(n *Node) {
	s := mdns.NewMdnsService(n.Host, "lantern", &mdnsDiscovery{node: n})
	if err := s.Start(); err != nil {
		fmt.Printf("mDNS warning: %v\n", err)
	}
}

// SeenAddressTTL bounds how long an address learned from a network
// announcement is trusted without being seen again. It is short on
// purpose: a sighting is cheap to repeat, and a stale address wastes a dial
// that then has to time out.
const SeenAddressTTL = 10 * time.Minute

type mdnsDiscovery struct {
	node *Node
}

// HandlePeerFound records where a peer is; it does not connect to it.
//
// Connecting to every peer that announces itself made the number of
// connections grow with the square of the number of Lantern devices on the
// network — about 3 MB each, so hundreds of megabytes at twenty devices.
// It also meant a stranger running Lantern on the same Wi-Fi got a
// connection from us whether or not we had ever paired with them.
//
// Instead the address is cached for SeenAddressTTL, which is enough for
// resolvePeer to dial on demand, and the connection only happens when
// something is actually asked for. Recurring announcements keep the entry
// fresh for as long as the device is on the network.
func (m *mdnsDiscovery) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == m.node.Host.ID() {
		return
	}
	if len(pi.Addrs) == 0 {
		return
	}
	m.node.Host.Peerstore().AddAddrs(pi.ID, pi.Addrs, SeenAddressTTL)
}

// Close cancels discovery, then closes the DHT and host, joining errors.
// A zero-value Node (as built in tests) is safe to close.
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

// GenerateCode mints a CodeBytes-entropy hex share code.
func GenerateCode() (string, error) {
	b := make([]byte, CodeBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// AddStaticPeers records operator-configured peer addresses (the --peer
// flag) in the peerstore with a permanent TTL, and returns the ones that
// parsed. Each entry must be a full multiaddr ending in /p2p/<peer-id>;
// anything else is skipped.
//
// Unlike mDNS sightings, these come from the operator and are never expired,
// so resolvePeer can dial them on demand even on networks where discovery
// cannot cross (WSL2 NAT, Docker, corporate Wi-Fi that blocks multicast).
// Recording never connects: like HandlePeerFound, the connection happens
// only when something is actually asked for, or via the daemon's dial retry.
func (n *Node) AddStaticPeers(addrs []string) []peer.AddrInfo {
	var out []peer.AddrInfo
	for _, a := range addrs {
		ma, err := multiaddr.NewMultiaddr(strings.TrimSpace(a))
		if err != nil {
			continue
		}
		ai, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			continue
		}
		n.Host.Peerstore().AddAddrs(ai.ID, ai.Addrs, peerstore.PermanentAddrTTL)
		out = append(out, *ai)
	}
	return out
}

// ConnectStaticRelays dials relay/bootstrap multiaddrs so nodes behind NAT
// can reserve circuits. Invalid entries are skipped; a nil/empty list is a
// no-op. Callers should pass a timeout context.
func (n *Node) ConnectStaticRelays(ctx context.Context, addrs []string) {
	for _, a := range addrs {
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			continue
		}
		ai, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			continue
		}
		_ = n.Host.Connect(ctx, *ai)
	}
}
