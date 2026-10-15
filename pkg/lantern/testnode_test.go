package lantern

import (
	"path/filepath"
	"testing"
	"time"
)

// testDialTimeout is what every node in these tests is given for one dial.
//
// Zero leaves go-libp2p's own limits: 15 seconds, and 5 for a private address.
// A test host dials over loopback and over whatever interface the runner has,
// and under -race on a loaded Windows runner the security handshake can outlast
// the 5s cap on a private address. The dial then fails with "all dials failed"
// on a connection that was never wrong.
//
// This is the same fix as the dial timeout added for internal/daemon's digest
// tests, applied where it had been missed: the option exists on Config, and
// these tests were the ones still taking the default.
const testDialTimeout = 60 * time.Second

// newTestNode brings up a Lantern for a test that will dial another one.
//
// The discovery settings are here for the same reason as the timeout: a test
// that addresses its peer directly has no use for the public DHT, and joining
// it means every node in the suite competes with every other node on the
// internet. LAN-only plus no announcement keeps a test host off the network it
// happens to be running on.
func newTestNode(t *testing.T, dir, name string) *Lantern {
	t.Helper()
	ln, err := New(Config{
		DataDir:     filepath.Join(dir, name),
		Bootstrap:   []string{"none"},
		NoMDNS:      true,
		DialTimeout: testDialTimeout,
	})
	if err != nil {
		t.Fatalf("bringing up the %s device: %v", name, err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}
