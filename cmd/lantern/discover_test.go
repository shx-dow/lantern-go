package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// A pairing creates no connection. discover that showed only connections said
// "no connected peers" on the machine a person had just paired, which reads as
// a failed pairing. The paired device is the thing they set up, so it is what
// the listing has to contain.
func TestDiscoverListsAPairedDeviceThatIsNotConnected(t *testing.T) {
	var buf bytes.Buffer
	renderDiscover(&buf, discoverView{
		Self: lanternclient.Status{DeviceName: "box", PeerID: "12D3self"},
		Devices: []lanternclient.Device{
			{PeerID: "12D3laptop", Alias: "laptop", Tier: "read"},
		},
		PairingKnown: true,
	})
	out := buf.String()

	for _, want := range []string{
		"self: box (12D3self)",
		"paired devices:",
		"laptop (12D3laptop)",
		"read",
		"not connected, no known address",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no paired devices") {
		t.Errorf("a paired device was reported as no pairing:\n%s", out)
	}
	if strings.Contains(out, "no connected peers") {
		t.Errorf("the empty-peers message survived a paired device:\n%s", out)
	}
}

// A paired device with an address in the peerstore is a different situation
// from one this device has never seen, and the count says which.
func TestDiscoverSaysHowManyAddressesItHolds(t *testing.T) {
	cases := []struct {
		name     string
		online   bool
		addrs    int
		contains string
	}{
		{"connected", true, 3, "connected"},
		{"one address", false, 1, "not connected, 1 known address"},
		{"several addresses", false, 2, "not connected, 2 known addresses"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			renderDiscover(&buf, discoverView{
				Self:         lanternclient.Status{DeviceName: "box", PeerID: "12D3self"},
				Devices:      []lanternclient.Device{{PeerID: "12D3laptop", Alias: "laptop", Tier: "read", Online: c.online, KnownAddresses: c.addrs}},
				PairingKnown: true,
			})
			if !strings.Contains(buf.String(), c.contains) {
				t.Errorf("output does not say %q:\n%s", c.contains, buf.String())
			}
		})
	}
}

// A peer that is connected without a pairing still has to be visible, and a
// paired device must not appear twice.
func TestDiscoverSeparatesConnectedPeersFromPairedDevices(t *testing.T) {
	var buf bytes.Buffer
	renderDiscover(&buf, discoverView{
		Self: lanternclient.Status{DeviceName: "box", PeerID: "12D3self"},
		Devices: []lanternclient.Device{
			{PeerID: "12D3laptop", Alias: "laptop", Tier: "read", Online: true},
		},
		Peers: []lanternclient.PeerInfo{
			{ID: "12D3laptop", Addrs: []string{"/ip4/10.0.0.2/tcp/4001"}},
			{ID: "12D3stranger", Addrs: []string{"/ip4/10.0.0.3/tcp/4001"}},
		},
		PairingKnown: true,
	})
	out := buf.String()

	if !strings.Contains(out, "connected, not paired:") || !strings.Contains(out, "12D3stranger") {
		t.Errorf("an unpaired connection is not listed:\n%s", out)
	}
	if strings.Count(out, "12D3laptop") != 1 {
		t.Errorf("the paired device was listed %d times:\n%s", strings.Count(out, "12D3laptop"), out)
	}
	if strings.Contains(out, "/ip4/10.0.0.2/tcp/4001") {
		t.Errorf("the paired device's addresses were printed under connected peers:\n%s", out)
	}
}

// The first run is the one a person sees with nothing set up, so it has to
// name the next command.
func TestDiscoverWithNothingPairedSaysHowToPair(t *testing.T) {
	var buf bytes.Buffer
	renderDiscover(&buf, discoverView{
		Self:         lanternclient.Status{DeviceName: "box", PeerID: "12D3self"},
		PairingKnown: true,
	})
	out := buf.String()
	for _, want := range []string{"no paired devices or connected peers", "lantern pair", "lantern trust add <peer-id> [alias]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

// A daemon with no trust store cannot pair anything, so discover must not
// recommend a command that would fail.
func TestDiscoverWithoutATrustStoreDoesNotSuggestPairing(t *testing.T) {
	var buf bytes.Buffer
	renderDiscover(&buf, discoverView{
		Self:         lanternclient.Status{DeviceName: "box", PeerID: "12D3self"},
		PairingKnown: false,
	})
	out := buf.String()
	if strings.Contains(out, "lantern trust add") {
		t.Errorf("discover suggests pairing on a daemon that cannot pair:\n%s", out)
	}
	if !strings.Contains(out, "no connected peers") {
		t.Errorf("output does not say there is nothing connected:\n%s", out)
	}
}

// The command has to ask for the paired devices; the renderer above proves
// only what happens once it has them.
func TestDiscoverAsksTheDaemonForPairedDevices(t *testing.T) {
	d := newFakeDaemon(t)
	stdout, stderr, code := drive(t, d, "discover")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "nas (12D3kooONE)") {
		t.Errorf("discover did not show the paired device:\n%s", stdout)
	}
	called := false
	for _, r := range d.called() {
		if r == "GET /v1/devices" {
			called = true
		}
	}
	if !called {
		t.Errorf("discover never asked for /v1/devices: %v", d.called())
	}
}

// And the JSON form carries the same devices, in the form an agent reads.
func TestDiscoverJSONCarriesPairedDevices(t *testing.T) {
	d := newFakeDaemon(t)
	stdout, stderr, code := drive(t, d, "--json", "discover")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var decoded struct {
		Self    map[string]any   `json:"self"`
		Devices []map[string]any `json:"devices"`
		Peers   []map[string]any `json:"peers"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("--json discover is not JSON: %v\n%s", err, stdout)
	}
	if len(decoded.Devices) != 1 || decoded.Devices[0]["peer_id"] != "12D3kooONE" {
		t.Errorf("--json discover lost the paired devices: %v", decoded.Devices)
	}
	if len(decoded.Peers) != 2 {
		t.Errorf("--json discover changed the peers: %v", decoded.Peers)
	}
}
