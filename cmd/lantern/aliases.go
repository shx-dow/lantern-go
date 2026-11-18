package main

import "errors"

// applyAliases rewrites the short names people reach for to the commands that
// implement them. It runs at the end of parseArgs so help, daemon routing, and
// the command implementations all see one canonical name.
//
// "devices" is not merely a spelling of "trust list": pairing a device and
// listing the devices already paired are the two things people do most, and
// "trust list" is neither word they would guess. The alias takes no arguments,
// because "devices add ..." would be a half-renamed command with its own
// grammar to learn.
func applyAliases(opts *cliOptions) error {
	switch opts.command {
	case "ls":
		opts.command = "files"
	case "devices":
		if len(opts.positional) > 0 {
			return errors.New("usage: lantern devices")
		}
		opts.command = "trust"
		opts.positional = []string{"list"}
	}
	return nil
}
