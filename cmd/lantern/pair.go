package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// pairUsage is the help text for `lantern pair`, also shown by
// `lantern help pair`.
const pairUsage = `lantern pair [invitation] [--ttl DURATION]

Pairs two devices with a single paste, in both directions.

On one device, "lantern pair" prints an invitation. Send it to the other
device however you like, and run "lantern pair <invitation>" there. Both
devices end up paired at tier "read", each named after the other's device
name, so "lantern get <name>:<path>" works without a rename.

An invitation is single use and expires -- ten minutes by default, --ttl to
change it (at most 24 hours). It is held in memory only, so restarting the
daemon invalidates any outstanding invitation, and it cannot grant writes:
write access remains a start-time decision on the serving device
(--allow-writes, --writable-dirs).

Both devices must be able to find each other: on a LAN through mDNS, or
across networks through the DHT. If they cannot -- a firewall, or no route
to a DHT peer -- "lantern trust add" with a peer ID from "lantern status"
still works.

Flags:
  --ttl DURATION   how long the invitation stays valid (default 10m)
  --json           machine-readable JSONL on stdout
`

// runPair prints an invitation, or redeems one. The daemon holds the
// invitation and makes the pairing decision; this only moves the string.
func runPair(c *lanternclient.Client, opts cliOptions) {
	enc := json.NewEncoder(os.Stdout)
	if len(opts.positional) > 1 {
		fatal(opts.jsonOut, fmt.Errorf("usage: lantern pair [invitation]"))
	}

	if len(opts.positional) == 0 {
		ttl := int64(opts.pairTTL / time.Second)
		if opts.pairTTL < 0 || (opts.pairTTL > 0 && ttl == 0) {
			fatal(opts.jsonOut, fmt.Errorf("--ttl must be a positive duration, such as 10m"))
		}
		inv, err := c.PairInvite(ttl)
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(inv)
			return
		}
		fmt.Println("Send this invitation to the other device. There, run:")
		fmt.Println("  lantern pair <invitation>")
		fmt.Println()
		fmt.Println(inv.Invitation)
		fmt.Println()
		if t, err := time.Parse(time.RFC3339, inv.ExpiresAt); err == nil {
			fmt.Printf("Single use. Expires at %s.\n", t.Local().Format("15:04"))
		} else {
			fmt.Printf("Single use. Expires at %s.\n", inv.ExpiresAt)
		}
		return
	}

	res, err := c.PairRedeem(strings.TrimSpace(opts.positional[0]))
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	if opts.jsonOut {
		enc.Encode(res)
		return
	}
	name := res.DeviceName
	if name == "" {
		name = res.PeerID
	}
	fmt.Printf("paired with %s (%s) at tier %s\n", name, res.PeerID, res.Tier)
	fmt.Println("both devices are paired; try `lantern remote-files " + name + "`")
}
