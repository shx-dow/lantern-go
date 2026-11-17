package main

// versionUsage is the help text for `lantern version`.
const versionUsage = `lantern version

Prints the version, commit, build date, Go toolchain, and platform of this
binary.
`

// commandUsage returns the help text for a command the CLI parses itself, or
// "" for a name it does not know. Every text names its command on the first
// line, so `<command> --help` says which command it describes.
func commandUsage(name string) string {
	switch name {
	case "send":
		return `lantern send <path> [flags]

Shares a file or directory and prints a one-time code for the receiver. A
directory is sent as <name>.zip.

Flags:
  --json           machine-readable JSONL on stdout
  --daemon[=URL]   use lanternd instead of starting an in-process node
  --port N         in-process listen port (0 = random)
  --data-dir DIR   in-process local advertisement directory
`
	case "receive":
		return `lantern receive <code> [output-dir] [flags]

Fetches the file behind a share code. The output directory defaults to the
current directory, or to --out DIR.

Flags:
  --out DIR        output directory (default ".")
  --json           machine-readable JSONL on stdout
  --daemon[=URL]   use lanternd instead of starting an in-process node
  --port N         in-process listen port (0 = random)
  --data-dir DIR   in-process local advertisement directory
`
	case "status":
		return `lantern status [--json]

Prints the daemon's status. Always talks to lanternd.
`
	case "list":
		return `lantern list [transfers|history] [--json]

Lists the daemon's transfers, or its recent history. Always talks to lanternd.
`
	case "peers":
		return `lantern peers [--json]

Lists the devices currently connected to the daemon.
`
	case "discover":
		return `lantern discover [--json]

Prints this device, the devices paired with it, and the peers it is connected
to.

A paired device is listed even when it is not connected: pairing does not open
a connection, and Lantern dials when a command needs the device. "not
connected, no known address" means this device has never seen it on the
network -- check that both are on the same LAN, or pass --peer on the daemon.
`
	case "trust":
		return `lantern trust <command> [args] [flags]

Manages paired devices.

Commands:
  list                             paired devices
  add <peer-id> [alias]            pair a device, at --tier (default read)
  tier <peer-id|alias> T           change what a paired device may do
  roots <peer-id|alias> [dirs...]  confine a device's writes (omit dirs to clear)
  remove <peer-id|alias>           unpair a device

<peer-id> is the other device's ID, printed by "lantern status" there. A value
that is not a peer ID is refused rather than paired: an unreachable pairing
looks like a working one and is the harder problem to diagnose.

Flags:
  --tier T                access tier for trust add
  --writable-roots DIRS   comma-separated directories a paired device may write
`
	case "files":
		return `lantern files [dir] [--json]

Lists files in the daemon's shared directories.
`
	case "remote-files":
		return `lantern remote-files <peer-id|alias> [dir] [--json]

Lists files a connected peer shares.
`
	case "get":
		return `lantern get <device>:<path> [dest] [--json]

Copies one file from a paired device. <device> is a paired alias or a peer ID;
<path> is resolved by that device inside its shared dirs, so a name a listing
printed works as typed.

[dest] may be a file name, an existing directory (the remote name is used
inside it), or omitted to write into the current directory. An existing file is
never replaced; the download refuses instead. The destination directory must
already exist, and the new file inherits its permissions.

Every chunk is checked against the digest the sending device computed before
anything is placed, so a corrupted or truncated copy leaves nothing behind.

Flags:
  --json           machine-readable JSONL on stdout
  --daemon-url URL daemon to talk to (default http://127.0.0.1:43782)
`
	case "help":
		return `lantern help [command]

Shows the top-level help, or the help for one command.
`
	}
	return ""
}
