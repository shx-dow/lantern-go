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

Prints this device and the peers it can see.
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
	case "help":
		return `lantern help [command]

Shows the top-level help, or the help for one command.
`
	}
	return ""
}
