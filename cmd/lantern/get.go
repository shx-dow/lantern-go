package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/shx-dow/lantern-go/internal/download"
)

// getUsage is the one-line form repeated in errors, so a user who got the
// syntax wrong is shown the shape that works rather than only the failure.
const getUsage = "usage: lantern get <device>:<path> [dest] (for example: lantern get laptop:notes.md)"

// parseGetTarget splits a `device:path` argument at the first colon.
//
// Nothing else is trimmed or normalized: an alias is resolved by the daemon,
// and the path is resolved by the device that owns the file. Splitting at the
// first colon leaves a Windows path on the far side intact.
func parseGetTarget(arg string) (device, path string, err error) {
	i := strings.Index(arg, ":")
	if i < 0 {
		return "", "", fmt.Errorf("no device in %q; %s", arg, getUsage)
	}
	device, path = arg[:i], arg[i+1:]
	if strings.TrimSpace(device) == "" {
		return "", "", fmt.Errorf("no device before the colon; %s", getUsage)
	}
	if strings.TrimSpace(path) == "" {
		return "", "", fmt.Errorf("no path after the device name; %s", getUsage)
	}
	return device, path, nil
}

// runGet copies one file from a paired device to local disk. It is the read
// path with a destination: the daemon dials the peer, the CLI verifies and
// places what arrives.
func runGet(ctx context.Context, c *daemonClient, opts cliOptions) {
	if len(opts.positional) < 1 || len(opts.positional) > 2 {
		fatal(opts.jsonOut, fmt.Errorf("%s", getUsage))
	}
	device, path, err := parseGetTarget(opts.positional[0])
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	dest := ""
	if len(opts.positional) == 2 {
		dest = opts.positional[1]
	}

	res, err := download.File(ctx, c, device, path, dest)
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	if opts.jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"type":   "file",
			"device": device,
			"path":   path,
			"dest":   res.Path,
			"bytes":  res.Bytes,
		})
		return
	}
	fmt.Printf("wrote %s (%d bytes) from %s\n", res.Path, res.Bytes, device)
}
