package main

import (
	"fmt"
	"io"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// discoverView is what `lantern discover` has to say: this device, the devices
// it is paired with, and any connected peer that is not paired.
//
// The paired list is the part that used to be missing. A pairing creates no
// connection, and nothing dials until a command needs the device, so discover
// on a freshly paired machine printed "no connected peers" and looked like the
// pairing had failed. The paired devices are what the person just set up;
// connections are the detail underneath.
type discoverView struct {
	Self    lanternclient.Status
	Devices []lanternclient.Device
	Peers   []lanternclient.PeerInfo
	// PairingKnown is false when the daemon has no trust store, so advice to
	// pair a device would be advice that cannot be followed.
	PairingKnown bool
}

// fetchDevices asks the daemon for its paired devices. A daemon without a
// trust store answers 503 and an older daemon 404; both mean the same thing
// to this listing, and neither should make discover fail after it already has
// the connected peers.
func fetchDevices(c *lanternclient.Client) ([]lanternclient.Device, bool, error) {
	devices, err := c.Devices(false)
	if err == nil {
		return devices, true, nil
	}
	if lanternclient.IsUnavailable(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func renderDiscover(w io.Writer, v discoverView) {
	fmt.Fprintf(w, "self: %s (%s)\n", v.Self.DeviceName, v.Self.PeerID)

	paired := make(map[string]bool, len(v.Devices))
	if len(v.Devices) > 0 {
		fmt.Fprintln(w, "paired devices:")
		for _, d := range v.Devices {
			label := d.PeerID
			if d.Alias != "" {
				label = fmt.Sprintf("%s (%s)", d.Alias, d.PeerID)
			}
			fmt.Fprintf(w, "  %s  %s  %s\n", label, d.Tier, deviceState(d))
		}
	}
	for _, d := range v.Devices {
		paired[d.PeerID] = true
	}

	var unpaired []lanternclient.PeerInfo
	for _, p := range v.Peers {
		if !paired[p.ID] {
			unpaired = append(unpaired, p)
		}
	}
	if len(unpaired) > 0 {
		fmt.Fprintln(w, "connected, not paired:")
		for _, p := range unpaired {
			fmt.Fprintf(w, "  %s\n", p.ID)
			for _, a := range p.Addrs {
				fmt.Fprintf(w, "    %s\n", a)
			}
		}
	}

	if len(v.Devices) == 0 && len(unpaired) == 0 {
		if !v.PairingKnown {
			fmt.Fprintln(w, "no connected peers")
			return
		}
		fmt.Fprintln(w, "no paired devices or connected peers")
		fmt.Fprintln(w, "run `lantern pair` here and paste the invitation on the other device:")
		fmt.Fprintln(w, "  lantern pair")
		fmt.Fprintln(w, "or pair by peer ID: `lantern status` there, then `lantern trust add <peer-id> [alias]`")
	}
}

// deviceState says whether the daemon holds a live connection, and if not,
// whether it holds an address to dial. A paired device with no known address
// has never been seen on this network, which is a different problem from one
// that is merely idle.
func deviceState(d lanternclient.Device) string {
	if d.Online {
		return "connected"
	}
	switch d.KnownAddresses {
	case 0:
		return "not connected, no known address"
	case 1:
		return "not connected, 1 known address"
	default:
		return fmt.Sprintf("not connected, %d known addresses", d.KnownAddresses)
	}
}
