package daemon

// A dial limit that is configured but invisible on an idle machine. Every
// fixture dial here completes in milliseconds when the runner is quiet, so a
// fixture that forgot to raise the swarm's limit looks exactly like one that
// set it — until a loaded runner takes longer than go-libp2p's 5-second
// private-address cap and a healthy connection is reported as a failure.
//
// This test makes the difference observable without a loaded runner. It dials a
// listener that accepts the connection and then says nothing, which parks the
// dial in the security handshake: the stage the Windows failures reached, and
// the stage the 5-second cap actually bounds.

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/shx-dow/lantern-go/internal/p2p"
)

func TestFixtureDialsOutliveTheSwarmDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("measures a dial deadline; run without -short")
	}

	root := t.TempDir()
	p := tierPair(t, root, []string{root}, false)
	defer p.cleanup()

	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			// Read and discard so the connection stays open without ever
			// completing a handshake.
			go func() { _, _ = io.Copy(io.Discard, c) }()
		}
	}()

	// A peer ID nothing is listening for, so the dial cannot succeed and the
	// only question is how long the fixture is willing to wait.
	stranger, err := p2p.NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer stranger.Close()

	addr, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/" + strconv.Itoa(silent.Addr().(*net.TCPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}

	// Longer than the 5-second default, shorter than the fixture's own limit.
	// The fixture's limit is what the dial should run into; the caller's
	// deadline ends the test.
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	start := time.Now()
	err = p.client.Node().Host.Connect(ctx, peer.AddrInfo{ID: stranger.Host.ID(), Addrs: []multiaddr.Multiaddr{addr}})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("the dial to a silent peer succeeded; the test cannot measure a limit it never reached")
	}
	if elapsed < 6*time.Second {
		t.Fatalf("the fixture gave up on a dial after %s, about go-libp2p's 5-second private-address default; "+
			"the fixture's DialTimeout is not reaching the swarm, so a loaded runner will fail a healthy handshake",
			elapsed.Round(100*time.Millisecond))
	}
	t.Logf("the fixture dialed for %s before the caller's deadline, past the 5-second default", elapsed.Round(100*time.Millisecond))
}
