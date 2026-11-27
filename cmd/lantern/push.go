package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// pushUsage is the one-line form repeated in errors, so a user who got the
// syntax wrong is shown the shape that works rather than only the failure.
const pushUsage = "usage: lantern push <device>:<path> <local> [--overwrite] (for example: lantern push laptop:notes.md ./notes.md)"

// runPush copies one local file or directory to a paired device. The daemon
// does the archiving, dialing, verification, and placement; the CLI only maps
// the arguments onto one request.
func runPush(c *daemonClient, opts cliOptions) {
	if len(opts.positional) != 2 {
		fatal(opts.jsonOut, fmt.Errorf("%s", pushUsage))
	}
	device, remotePath, err := parseDevicePath(opts.positional[0], pushUsage)
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	local := opts.positional[1]

	res, err := c.Push(device, local, remotePath, opts.overwrite)
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	if opts.jsonOut {
		out := map[string]any{
			"type":         "push",
			"device":       res.Device,
			"remote_path":  res.RemotePath,
			"bytes":        res.Bytes,
			"sha256":       res.SHA256,
			"local_sha256": res.LocalSHA256,
		}
		if len(res.Entries) > 0 {
			out["entries"] = len(res.Entries)
		}
		_ = json.NewEncoder(os.Stdout).Encode(out)
		return
	}
	if len(res.Entries) > 0 {
		fmt.Printf("pushed %s to %s:%s (%d entries, %d bytes sent)\n",
			local, device, res.RemotePath, len(res.Entries), res.Bytes)
		return
	}
	fmt.Printf("pushed %s to %s:%s (%d bytes)\n", local, device, res.RemotePath, res.Bytes)
}
