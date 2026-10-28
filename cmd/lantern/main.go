package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/shx-dow/lantern-go/internal/daemonapp"
	"github.com/shx-dow/lantern-go/internal/format"
	"github.com/shx-dow/lantern-go/internal/mcpserver"
	"github.com/shx-dow/lantern-go/internal/version"
	"github.com/shx-dow/lantern-go/pkg/lantern"
	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

type mdnsFilter struct{}

func (mdnsFilter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("[WARN] mdns:")) {
		return len(p), nil
	}
	return os.Stderr.Write(p)
}

func init() {
	log.SetOutput(mdnsFilter{})
}

const defaultDaemonURL = "http://127.0.0.1:43782"

// ownedSubcommands are handled here rather than by parseArgs, because each
// carries its own flags and its own output contract.
var ownedSubcommands = map[string]bool{
	"daemon":  true,
	"doctor":  true,
	"mcp":     true,
	"version": true,
}

// globalFlagsWithValues are the CLI flags that consume the argument after
// them. The subcommand scan needs this so `--out daemon send x` is read as a
// directory called daemon, not as the daemon command.
var globalFlagsWithValues = map[string]bool{
	"port":           true,
	"data-dir":       true,
	"out":            true,
	"tier":           true,
	"writable-roots": true,
	"roots":          true,
	"daemon-url":     true,
	"daemon-addr":    true,
	"daemon-token":   true,
	"token-file":     true,
}

// globalBoolFlags are the CLI flags that stand alone. Anything else before a
// subcommand is left for parseArgs to reject, rather than being skipped and
// forgotten.
var globalBoolFlags = map[string]bool{
	"json":        true,
	"daemon":      true,
	"probe-peers": true,
}

// ownedSubcommandAfterFlags finds a subcommand that owns its own argument
// grammar even when global flags come first, so `lantern --json daemon --help`
// reaches the daemon instead of being read as an unknown command.
//
// Global flags before the name are accepted and belong to the CLI; a
// subcommand's own flags follow its name, as in `lantern daemon --data-dir X`.
// Doctor is deliberately not matched here: it also reads the CLI's daemon
// flags, so parseArgs routes it.
func ownedSubcommandAfterFlags(args []string) (name string, rest []string, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return "", nil, false
		}
		if a == "-h" || a == "--help" {
			// Help before a command belongs to the top level, where parseArgs
			// turns it into `help <command>`.
			return "", nil, false
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			f := strings.TrimLeft(a, "-")
			f, _, hasValue := strings.Cut(f, "=")
			switch {
			case globalBoolFlags[f]:
				// Stands alone; `--daemon=URL` carries its value in the same
				// token and needs no skip either.
			case globalFlagsWithValues[f] && !hasValue:
				i++
			default:
				return "", nil, false
			}
			continue
		}
		if !ownedSubcommands[a] || a == "doctor" {
			return "", nil, false
		}
		return a, args[i+1:], true
	}
	return "", nil, false
}

// wantsHelp reports whether args ask for help before a `--` terminator.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// usageFor returns the help text for a command, or "" when it is unknown.
func usageFor(name string) string {
	switch name {
	case "daemon":
		return daemonapp.Usage
	case "doctor":
		return doctorUsage
	case "mcp":
		return mcpserver.Usage
	case "version":
		return versionUsage
	}
	return commandUsage(name)
}

// runOrExit exits non-zero on failure, keeping the exit codes in one place.
func runOrExit(err error) {
	if err == nil {
		return
	}
	// A cancelled context is the operator pressing Ctrl-C, not a fault.
	if errors.Is(err, context.Canceled) {
		os.Exit(130)
	}
	// flag.ErrHelp means the command's own flag set printed its usage. Asking
	// for help is not a failure.
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	// A command that already printed its own diagnosis exits non-zero
	// without a second message saying "error: ".
	if errors.Is(err, errSilent) {
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

type cliOptions struct {
	jsonOut       bool
	port          int
	dataDir       string
	outDir        string
	daemon        bool
	daemonURL     string
	daemonToken   string
	tokenFile     string
	probePeers    bool
	tier          string
	writableRoots []string
	command       string
	positional    []string
	// helpForCommand is set when -h/--help follows a command. The command
	// owns that help, so it is shown instead of being treated as an argument.
	helpForCommand bool
}

// commandNeedsDaemon reports whether a command is served only by
// runDaemonCommand, and so goes to the daemon whether or not --daemon was
// passed.
//
// send and receive run either way — --daemon chooses between lanternd and a
// fresh in-process node for them — so they are deliberately absent.
func commandNeedsDaemon(command string) bool {
	switch command {
	case "status", "discover", "peers", "list", "trust", "files", "remote-files":
		return true
	}
	return false
}

func main() {
	args := os.Args[1:]

	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()

	// doctor owns its flags, but it also reads the CLI's daemon flags. When it
	// comes first, run it directly; when global flags precede it, parseArgs
	// routes it below.
	if len(args) > 0 && args[0] == "doctor" {
		runOrExit(runDoctor(ctx, args[1:], os.Stdout))
		return
	}

	// Subcommands that own their own argument grammar run before the CLI
	// parser sees them, so `lantern daemon --lan` does not have to be
	// spelled in a way parseArgs understands. Global flags may come first.
	if name, rest, ok := ownedSubcommandAfterFlags(args); ok {
		switch name {
		case "daemon":
			runOrExit(daemonapp.Run(ctx, rest, os.Stderr))
			return
		case "mcp":
			if wantsHelp(rest) {
				fmt.Fprint(os.Stderr, mcpserver.Usage)
				return
			}
			runOrExit(mcpserver.Serve(ctx, os.Stdin, os.Stdout))
			return
		case "version":
			if wantsHelp(rest) {
				fmt.Fprint(os.Stderr, versionUsage)
				return
			}
			fmt.Println(version.Get())
			return
		}
	}

	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage()
		os.Exit(2)
	}

	// `lantern help`, `lantern help trust`, and `lantern --help trust` all
	// land here with command "help" and the topic as the first positional.
	if opts.command == "" {
		usage()
		return
	}
	// A command named before --help owns that help, including send and
	// receive, which used to treat the flag as a path.
	if opts.helpForCommand {
		if text := usageFor(opts.command); text != "" {
			fmt.Fprint(os.Stderr, text)
			return
		}
		fmt.Fprintf(os.Stderr, "unknown command %q\n", opts.command)
		usage()
		os.Exit(2)
	}
	if opts.command == "help" {
		if len(opts.positional) > 0 {
			if text := usageFor(opts.positional[0]); text != "" {
				fmt.Fprint(os.Stderr, text)
				return
			}
			fmt.Fprintf(os.Stderr, "unknown command %q\n", opts.positional[0])
			usage()
			os.Exit(2)
		}
		usage()
		return
	}
	// doctor is a subcommand, but it also understands --daemon/--daemon-url
	// and --json, so it is routed here rather than in the switch above: the
	// flags are parsed by the CLI, and the run needs the parsed values.
	if opts.command == "doctor" {
		runOrExit(runDoctorFlags(ctx, opts, os.Stdout))
		return
	}

	// Commands the daemon serves go to the daemon, whether or not --daemon was
	// passed. Asking for one without it used to be an error naming the flag, which
	// made the user learn a flag to get the thing they had already asked for.
	//
	// --daemon is still meaningful for send and receive, where it chooses between
	// lanternd and a fresh in-process node. For the rest there is no local
	// alternative to choose, so the flag is implied.
	//
	// Routing here rather than after lantern.New also keeps the side effects out:
	// bringing up an in-process node writes an identity key and a data directory,
	// which a status query has no use for.
	if commandNeedsDaemon(opts.command) {
		opts.daemon = true
	}

	cliCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go handleSignal(cancel, opts.jsonOut)

	if opts.daemon {
		runDaemonCommand(cliCtx, opts)
		return
	}

	ln, err := lantern.New(lantern.Config{Port: opts.port, DataDir: opts.dataDir})
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	defer ln.Close()

	switch opts.command {
	case "send":
		runSend(cliCtx, ln, opts)
	case "receive":
		runReceive(cliCtx, ln, opts)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", opts.command)
		usage()
		os.Exit(2)
	}
}

// parseArgs turns a raw argument list into options.
//
// It uses flag.FlagSet for the parsing rather than a hand-rolled switch, so
// --flag=value, --flag value, and the error messages for a missing value all
// behave the same way without each flag having to spell them out. Two things
// the standard parser does not do are arranged around rather than fought:
//
//   - It stops at the first non-flag argument. The CLI accepts flags anywhere,
//     including after the command and its arguments (`lantern send a.txt
//     --json`), so the parse is looped over what is left each time.
//   - A bool cannot carry a value. --daemon is a flag that may also take a URL,
//     so it is registered through BoolFunc, which receives "true" when bare
//     and the text when written with "=".
//
// Every flag's behaviour is pinned in parse_contract_test.go.
func parseArgs(args []string) (cliOptions, error) {
	var opts cliOptions
	opts.outDir = "."
	opts.daemonURL = os.Getenv("LANTERND_URL")
	if opts.daemonURL == "" {
		opts.daemonURL = defaultDaemonURL
	}
	opts.daemonToken = os.Getenv("LANTERN_DAEMON_TOKEN")
	opts.tokenFile = os.Getenv(lanternclient.TokenFileEnv)

	// Errors are reported here and printed by the caller, so a parse failure
	// must not have written anything itself.
	fs := flag.NewFlagSet("lantern", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	setDaemonURL := func(v string) error {
		// Naming the flag is a request to talk to the daemon, so that part is
		// honoured even with no value. An empty value then means "not
		// overriding", which keeps $LANTERND_URL alive when a script passes the
		// flag through with nothing after the "=".
		opts.daemon = true
		if strings.TrimSpace(v) != "" {
			opts.daemonURL = normalizeBaseURL(v)
		}
		return nil
	}

	fs.BoolVar(&opts.jsonOut, "json", false, "machine-readable JSONL on stdout")
	// BoolFunc takes "true" when the flag is bare and the text when written as
	// --daemon=URL, which is exactly the shape this flag has.
	fs.BoolFunc("daemon", "talk to lanternd instead of an in-process node", func(v string) error {
		opts.daemon = true
		if v != "true" {
			return setDaemonURL(v)
		}
		return nil
	})
	fs.Func("daemon-url", "lanternd base URL", setDaemonURL)
	// A second spelling of --daemon-url, kept because scripts in the wild use
	// it. Only --daemon-url is advertised.
	fs.Func("daemon-addr", "lanternd base URL", setDaemonURL)
	fs.Func("daemon-token", "lanternd bearer token", func(v string) error {
		opts.daemonToken = v
		return nil
	})
	// An empty value falls back rather than clearing the environment, like
	// every other value flag here.
	fs.Func("token-file", "file holding the lanternd bearer token", func(v string) error {
		if strings.TrimSpace(v) != "" {
			opts.tokenFile = v
		}
		return nil
	})
	fs.StringVar(&opts.outDir, "out", opts.outDir, "receive output directory")
	// Deliberately not fs.IntVar. That parses base-prefixed and leading-zero
	// values, so `--port 04242` became 2210 and `--port 08080` was rejected as a
	// bad octal digit. A port is written in decimal by everything that produces
	// one, and quietly listening on a different port is worse than a bad value
	// being refused.
	fs.Func("port", "in-process listen port (0 = random)", func(v string) error {
		p, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid --port %q", v)
		}
		opts.port = p
		return nil
	})
	fs.StringVar(&opts.dataDir, "data-dir", opts.dataDir, "in-process local advertisement directory")
	fs.StringVar(&opts.tier, "tier", opts.tier, "access tier for trust add")
	setRoots := func(v string) error {
		opts.writableRoots = splitRoots(v)
		return nil
	}
	fs.Func("writable-roots", "directories a paired device may write", setRoots)
	// Short alias for --writable-roots.
	fs.Func("roots", "directories a paired device may write", setRoots)
	// Bool, but accepting "=false" for symmetry with the rest.
	fs.BoolFunc("probe-peers", "real reachability checks in doctor", func(v string) error {
		opts.probePeers = truthyFlag(v)
		return nil
	})
	var helpRequested bool
	fs.BoolVar(&helpRequested, "help", false, "show help")
	fs.BoolVar(&helpRequested, "h", false, "shorthand for --help")

	// The standard parser stops at the first non-flag argument, so parse what
	// is left again each time round, taking the first as the command and any
	// later ones as its arguments. This is what makes `send a.txt --json` work.
	//
	// A round never mixes the two: flag.FlagSet consumes only the leading flags
	// and hands the rest back, so every argument it has seen came before
	// whatever this round is about to take. That ordering is what --help needs,
	// and it is why helpFirst below can be set from whether a command existed
	// yet.
	// `--` ends flag parsing for the rest of the command line. The flag package
	// applies it only to the remainder of the round it is parsing, and this
	// parse is re-run on what is left, so handing it the whole list would let
	// `receive -- ABC12 --json` take --json as a flag. Split the list here
	// instead and never parse past it.
	var trailing []string
	parseable := args
	for i, a := range args {
		if a == "--" {
			parseable, trailing = args[:i], args[i+1:]
			break
		}
	}

	rest := parseable
	helpFirst := false
	for {
		hadCommand := opts.command != ""
		hadHelp := helpRequested
		if err := fs.Parse(rest); err != nil {
			return opts, err
		}
		switch {
		case helpRequested && !hadCommand:
			helpFirst = true
		case helpRequested && !hadHelp:
			// --help after a command belongs to it, wherever it was written.
			// Recording it as a flag keeps it out of the command's arguments:
			// `send --help a.txt` used to look for a file called --help.
			opts.helpForCommand = true
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		// A lone "-" is what a user means by stdin, not a misspelled flag.
		if !helpFirst && opts.command == "" && rest[0] != "-" {
			opts.command = rest[0]
		} else {
			opts.positional = append(opts.positional, rest[0])
		}
		rest = rest[1:]
	}

	for _, a := range trailing {
		if opts.command == "" && a != "-" && !helpFirst {
			opts.command = a
		} else {
			opts.positional = append(opts.positional, a)
		}
	}

	// --help before any command means help, with whatever followed it as a
	// topic. `lantern help daemon` keeps working because it is handled before
	// this is reached.
	if helpFirst {
		opts.command = "help"
	}
	return opts, nil
}

// truthyFlag reads a flag value written as text. Anything unrecognised is
// false, so a mistyped or machine-generated value never turns a probe on by
// accident — enabling it dials every paired device, which is not something to
// do because a flag was spelled unexpectedly.
func truthyFlag(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// splitRoots parses a comma-separated root list, so a shell never has to be
// trusted with quoting a single directory that contains a space.
func splitRoots(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// rootSummary renders a peer's writable roots for the list line.
func rootSummary(roots []string) string {
	if len(roots) == 0 {
		return "(this device's writable dirs)"
	}
	return strings.Join(roots, ",")
}

// orNoneList renders an empty list as "none".
func orNoneList(v []string) string {
	if len(v) == 0 {
		return "none (this device's writable dirs)"
	}
	return strings.Join(v, ", ")
}

func normalizeBaseURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return defaultDaemonURL
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return strings.TrimSuffix(v, "/")
	}
	if strings.HasPrefix(v, ":") {
		return "http://127.0.0.1" + v
	}
	return "http://" + strings.TrimSuffix(v, "/")
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: lantern [--json] [--port N] [--data-dir DIR] [--daemon[=URL]] <command> [args]

  commands:
    send <path>                  share a file or directory (dirs arrive as <name>.zip)
    receive <code> [output-dir]  fetch a file (or use --out DIR)
    status                       daemon status
    list [transfers|history]     daemon transfers
    peers                        connected peers
    discover                     self + connected peers
    trust list                   paired devices
    trust add <peer-id> [alias]  pair a device, at --tier (default read)
    trust tier <peer-id|alias> T change what a paired device may do
    trust roots <peer-id|alias> [dirs...]  confine a device's writes
    trust remove <peer-id>       unpair a device
    files [dir]                  list shared-dir files
    remote-files <peer-id> [dir] list files on a connected peer
    daemon                       run the background service (lantern daemon --help)
    mcp                          run the MCP stdio server for agents
    doctor                       diagnose a setup and print the fix for each problem
                                (lantern doctor --probe-peers for real reachability)
    version                      version, commit, build date, toolchain, platform
    help [command]               show help for a command

flags:
  -h, --help        show help (also <command> --help)
  --json            machine-readable JSONL on stdout (agents/MCP/GUI)
  --out DIR         receive output directory (default ".")
  --port N          in-process listen port (default 0 = random)
  --data-dir DIR    in-process local advertisement directory
  --daemon[=URL]    for send/receive: talk to lanternd instead of starting an
                    in-process node. Commands above that are served by lanternd
                    always use it, flag or not.
                    (default URL http://127.0.0.1:43782 or $LANTERND_URL)
  --daemon-url URL  same as --daemon=URL
  --daemon-token T  daemon bearer token (or $LANTERN_DAEMON_TOKEN); wins over
                    the token file
  --token-file FILE daemon bearer token file (default: the daemon's token in the
                    per-user data dir, or $LANTERN_DAEMON_TOKEN_FILE). Read
                    automatically only when the daemon URL is loopback.

examples:
  lantern send ./photo.jpg
  lantern send --json ./photo.jpg
  lantern --daemon send ./photo.jpg
  lantern --daemon receive <code> --out ./inbox
  lantern status`)
}

// daemonRecord aliases the shared client record for compactness.
type daemonRecord = lanternclient.Record

type daemonClient = lanternclient.Client

func newDaemonClient(base, token string) *daemonClient {
	return lanternclient.New(base, token)
}

// resolvedToken applies the flag > environment > token-file precedence once,
// at the point a daemon client is built.
func (o cliOptions) resolvedToken() (string, error) {
	return lanternclient.ResolveToken(o.daemonURL, o.daemonToken, o.tokenFile)
}

func runDaemonCommand(ctx context.Context, opts cliOptions) {
	token, err := opts.resolvedToken()
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	c := newDaemonClient(opts.daemonURL, token)
	enc := json.NewEncoder(os.Stdout)
	switch opts.command {
	case "send":
		if len(opts.positional) < 1 {
			fatal(opts.jsonOut, fmt.Errorf("usage: lantern send <path>"))
		}
		var rec daemonRecord
		rec, err := c.Share(opts.positional[0], 0)
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(map[string]any{"type": "share", "id": rec.ID, "code": rec.Code, "file_name": rec.FileName, "file_size": rec.FileSize})
		} else {
			fmt.Printf("share code: %s\n", rec.Code)
			fmt.Fprintln(os.Stderr, "waiting for receiver...")
		}
		if runDaemonTransfer(ctx, c, enc, opts.jsonOut, rec.ID, "sent", rec.FileName) {
			os.Exit(1)
		}
	case "receive":
		if len(opts.positional) < 1 {
			fatal(opts.jsonOut, fmt.Errorf("usage: lantern receive <code> [output-dir]"))
		}
		code := strings.TrimSpace(opts.positional[0])
		outputDir := opts.outDir
		if len(opts.positional) > 1 && opts.positional[1] != "" {
			outputDir = opts.positional[1]
		}
		var rec daemonRecord
		rec, err := c.Fetch(code, outputDir)
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(map[string]any{"type": "peer", "id": rec.ID, "peer_id": rec.PeerID, "code": rec.Code})
		} else {
			fmt.Fprintf(os.Stderr, "fetching %s...\n", rec.ID)
		}
		if runDaemonTransfer(ctx, c, enc, opts.jsonOut, rec.ID, "received:", "") {
			os.Exit(1)
		}
	case "status":
		st, err := c.Status()
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(st)
		} else {
			fmt.Printf("device: %s\npeer: %s\nlan_only: %v\naddrs:\n", st.DeviceName, st.PeerID, st.LANOnly)
			for _, a := range st.Addrs {
				fmt.Printf("  %s\n", a)
			}
		}
	case "discover":
		st, peers, err := c.Discover()
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(map[string]any{"self": st, "peers": peers})
		} else {
			fmt.Printf("self: %s (%s)\n", st.DeviceName, st.PeerID)
			if len(peers) == 0 {
				fmt.Println("no connected peers")
			} else {
				fmt.Println("peers:")
				for _, p := range peers {
					fmt.Printf("  %s\n", p.ID)
					for _, a := range p.Addrs {
						fmt.Printf("    %s\n", a)
					}
				}
			}
		}
	case "peers":
		peers, err := c.Peers()
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(map[string]any{"peers": peers})
		} else if len(peers) == 0 {
			fmt.Println("no connected peers")
		} else {
			for _, p := range peers {
				fmt.Printf("%s\n", p.ID)
				for _, a := range p.Addrs {
					fmt.Printf("  %s\n", a)
				}
			}
		}
	case "list":
		what := "transfers"
		if len(opts.positional) > 0 {
			what = opts.positional[0]
		}
		switch what {
		case "transfers", "shares":
			var transfers []daemonRecord
			var err error
			// /v1/shares returns {"shares":[...]}; normalize to transfers shape.
			if what == "shares" {
				transfers, err = c.Shares()
			} else {
				transfers, err = c.Transfers("")
			}
			if err != nil {
				fatal(opts.jsonOut, err)
			}
			if opts.jsonOut {
				enc.Encode(map[string][]daemonRecord{"transfers": transfers})
			} else if len(transfers) == 0 {
				fmt.Println("no transfers")
			} else {
				for _, r := range transfers {
					fmt.Printf("%s %s %s %d/%d %s\n", r.ID, r.Kind, r.FileName, r.Bytes, r.Total, r.State)
				}
			}
		case "history":
			history, err := c.History()
			if err != nil {
				fatal(opts.jsonOut, err)
			}
			if opts.jsonOut {
				enc.Encode(map[string][]daemonRecord{"history": history})
			} else if len(history) == 0 {
				fmt.Println("no history")
			} else {
				for _, r := range history {
					fmt.Printf("%s %s %s %s\n", r.ID, r.Kind, r.FileName, r.State)
				}
			}
		default:
			fatal(opts.jsonOut, fmt.Errorf("usage: lantern list [transfers|history]"))
		}
	case "trust":
		sub := ""
		if len(opts.positional) > 0 {
			sub = opts.positional[0]
		}
		switch sub {
		case "list", "":
			trusted, err := c.TrustList()
			if err != nil {
				fatal(opts.jsonOut, err)
			}
			if opts.jsonOut {
				enc.Encode(map[string]any{"trusted": trusted})
			} else if len(trusted) == 0 {
				fmt.Println("no paired devices")
			} else {
				for _, p := range trusted {
					name := p.Alias
					if name == "" {
						name = "(no alias)"
					}
					fmt.Printf("%s  %-24s  %-10s %s\n", p.PeerID, name, p.Tier, rootSummary(p.WritableRoots))
				}
			}
		case "add":
			if len(opts.positional) < 2 {
				fatal(opts.jsonOut, fmt.Errorf("usage: lantern trust add <peer-id> [alias] [--tier T] [--writable-roots DIRS]"))
			}
			alias := ""
			if len(opts.positional) > 2 {
				alias = opts.positional[2]
			}
			body := map[string]any{"peer_id": opts.positional[1], "alias": alias}
			if opts.tier != "" {
				body["tier"] = opts.tier
			}
			if len(opts.writableRoots) > 0 {
				body["writable_roots"] = opts.writableRoots
			}
			entry, err := c.TrustAddSpec(body)
			if err != nil {
				fatal(opts.jsonOut, err)
			}
			if opts.jsonOut {
				enc.Encode(entry)
			} else {
				fmt.Printf("paired %s at tier %s\n", entry.PeerID, entry.Tier)
				if len(entry.WritableRoots) > 0 {
					fmt.Printf("  may write only in: %s\n", strings.Join(entry.WritableRoots, ", "))
				}
			}
		case "tier":
			// Changing a tier must not re-add the pairing: that would reset
			// its alias and cached addresses.
			if len(opts.positional) < 3 {
				fatal(opts.jsonOut, fmt.Errorf("usage: lantern trust tier <peer-id|alias> <tier>"))
			}
			entry, err := c.TrustSetTier(opts.positional[1], opts.positional[2])
			if err != nil {
				fatal(opts.jsonOut, err)
			}
			if opts.jsonOut {
				enc.Encode(entry)
			} else {
				fmt.Printf("%s is now at tier %s\n", entry.PeerID, entry.Tier)
			}
		case "roots":
			if len(opts.positional) < 2 {
				fatal(opts.jsonOut, fmt.Errorf("usage: lantern trust roots <peer-id|alias> [dirs...] (omit dirs to clear)"))
			}
			entry, err := c.TrustSetRoots(opts.positional[1], opts.positional[2:])
			if err != nil {
				fatal(opts.jsonOut, err)
			}
			if opts.jsonOut {
				enc.Encode(entry)
			} else {
				fmt.Printf("%s writable roots: %s\n", entry.PeerID, orNoneList(entry.WritableRoots))
			}
		case "remove", "rm":
			if len(opts.positional) < 2 {
				fatal(opts.jsonOut, fmt.Errorf("usage: lantern trust remove <peer-id>"))
			}
			if err := c.TrustRemove(opts.positional[1]); err != nil {
				fatal(opts.jsonOut, err)
			}
			if opts.jsonOut {
				enc.Encode(map[string]any{"removed": opts.positional[1]})
			} else {
				fmt.Printf("removed %s\n", opts.positional[1])
			}
		default:
			fatal(opts.jsonOut, fmt.Errorf("usage: lantern trust [list|add|tier|roots|remove]"))
		}
	case "files":
		dir := ""
		if len(opts.positional) > 0 {
			dir = opts.positional[0]
		}
		files, err := c.Files(dir)
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(map[string]any{"files": files})
		} else if len(files) == 0 {
			fmt.Println("no files (configure --shared-dirs on lanternd)")
		} else {
			for _, f := range files {
				fmt.Printf("%s %d %s\n", f.Name, f.Size, f.ModTime)
			}
		}
	case "remote-files":
		if len(opts.positional) < 1 {
			fatal(opts.jsonOut, fmt.Errorf("usage: lantern remote-files <peer-id> [dir]"))
		}
		dir := ""
		if len(opts.positional) > 1 {
			dir = opts.positional[1]
		}
		files, err := c.RemoteFiles(opts.positional[0], dir)
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(map[string]any{"files": files})
		} else if len(files) == 0 {
			fmt.Println("no files")
		} else {
			for _, f := range files {
				fmt.Printf("%s %d %s\n", f.Name, f.Size, f.ModTime)
			}
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", opts.command)
		usage()
		os.Exit(2)
	}
}

type sseEvent struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	FileName string `json:"file_name"`
	Bytes    int64  `json:"bytes"`
	Total    int64  `json:"total"`
	Error    string `json:"error"`
}

// runDaemonTransfer streams SSE events for id until terminal, falling back
// to polling when the stream drops. It mirrors runTransfer output and
// reports whether the transfer failed.
func runDaemonTransfer(ctx context.Context, c *daemonClient, enc *json.Encoder, jsonOut bool, id, doneVerb, doneName string) bool {
	// Fast path: already terminal before SSE connects.
	if rec, err := daemonGet(c, id); err == nil && isTerminal(rec.State) {
		return emitDaemonRecord(enc, jsonOut, rec, doneVerb, doneName)
	}
	// A frame that says the transfer ended IS the ending. The stream reports
	// the outcome as it reads it, so the only case worth polling is a stream
	// that closed without ever saying.
	//
	// Treating an error or cancelled frame as a dropped stream used to send
	// this into the polling loop below, where a record the daemon no longer
	// has — it restarted, or history rolled past it — was an endless 404. The
	// command hung until its context was cancelled, and the caller was told
	// the transfer had been cancelled, overwriting the reason the daemon gave.
	if failed, err := streamDaemonEvents(ctx, c, enc, jsonOut, id, doneVerb, doneName); err == nil {
		return failed
	}
	// Stream closed without terminal: poll until terminal or ctx done.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if jsonOut {
				enc.Encode(map[string]any{"type": "cancelled", "id": id})
			} else {
				fmt.Fprintln(os.Stderr, "\ncancelled")
			}
			return true
		case <-ticker.C:
			rec, err := daemonGet(c, id)
			if err != nil {
				continue
			}
			emitDaemonProgress(enc, jsonOut, rec)
			if isTerminal(rec.State) {
				return emitDaemonRecord(enc, jsonOut, rec, doneVerb, doneName)
			}
		}
	}
}

func daemonGet(c *daemonClient, id string) (daemonRecord, error) {
	return c.Get(id)
}

func isTerminal(state string) bool {
	return state == "done" || state == "failed" || state == "canceled"
}

// streamDaemonEvents reads transfer events until one of them ends the
// transfer, reporting whether that ending was a failure.
//
// A nil error means a frame ended the transfer — done, error or cancelled — and
// the outcome has already been emitted. That is an answer, not a stream fault,
// and the caller must not fall back to polling on it. A non-nil error means the
// stream closed without ever saying, which is the only case polling improves on.
func streamDaemonEvents(ctx context.Context, c *daemonClient, enc *json.Encoder, jsonOut bool, id, doneVerb, doneName string) (failed bool, err error) {
	resp, err := c.OpenEventsStream(ctx)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var eventName, data string
	// flush reports (ended, failed). A frame that ends the transfer is an
	// answer already emitted above, so it returns a nil error and the caller
	// stops. Only the stream closing without an ending is a non-nil error.
	flush := func() (bool, bool) {
		defer func() { eventName, data = "", "" }()
		if data == "" {
			return false, false
		}
		var e sseEvent
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return false, false
		}
		if e.ID != "" && e.ID != id {
			return false, false
		}
		switch e.Type {
		case "progress":
			if jsonOut {
				enc.Encode(map[string]any{"type": "progress", "id": e.ID, "file_name": e.FileName, "bytes": e.Bytes, "total": e.Total})
			} else {
				pct := progressPercent(e.Bytes, e.Total)
				fmt.Fprintf(os.Stderr, "\rprogress: %.1f%% (%s / %s)", pct, format.Bytes(e.Bytes), format.Bytes(e.Total))
			}
			return false, false
		case "done":
			name := doneName
			if name == "" {
				name = e.FileName
			}
			if jsonOut {
				enc.Encode(map[string]any{"type": "done", "id": e.ID, "file_name": name, "bytes": e.Bytes, "total": e.Total})
			} else {
				fmt.Fprintf(os.Stderr, "\n%s %s\n", doneVerb, name)
			}
			return true, false
		case "error":
			if jsonOut {
				enc.Encode(map[string]any{"type": "error", "id": e.ID, "error": e.Error})
			} else {
				fmt.Fprintf(os.Stderr, "\nerror: %s\n", e.Error)
			}
			return true, true
		case "cancelled":
			if jsonOut {
				enc.Encode(map[string]any{"type": "cancelled", "id": e.ID})
			} else {
				fmt.Fprintln(os.Stderr, "\ncancelled")
			}
			return true, true
		default:
			_ = eventName
			return false, false
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if ended, failed := flush(); ended {
				return failed, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if v, ok := strings.CutPrefix(line, "event:"); ok {
			eventName = strings.TrimSpace(v)
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			v = strings.TrimSpace(v)
			if data != "" {
				data += "\n"
			}
			data += v
			continue
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return false, err
	}
	return false, fmt.Errorf("stream closed")
}

func emitDaemonProgress(enc *json.Encoder, jsonOut bool, rec daemonRecord) {
	if jsonOut {
		return // polling path stays quiet; SSE already emitted progress
	}
	if rec.State == "running" && rec.Total > 0 {
		pct := progressPercent(rec.Bytes, rec.Total)
		fmt.Fprintf(os.Stderr, "\rprogress: %.1f%% (%s / %s)", pct, format.Bytes(rec.Bytes), format.Bytes(rec.Total))
	}
}

func emitDaemonRecord(enc *json.Encoder, jsonOut bool, rec daemonRecord, doneVerb, doneName string) bool {
	switch rec.State {
	case "done":
		name := doneName
		if name == "" {
			name = rec.FileName
		}
		if jsonOut {
			enc.Encode(map[string]any{"type": "done", "id": rec.ID, "file_name": name, "bytes": rec.Bytes, "total": rec.Total})
		} else {
			fmt.Fprintf(os.Stderr, "\n%s %s\n", doneVerb, name)
		}
		return false
	case "failed":
		if jsonOut {
			enc.Encode(map[string]any{"type": "error", "id": rec.ID, "error": rec.Error})
		} else {
			fmt.Fprintf(os.Stderr, "\nerror: %s\n", rec.Error)
		}
		return true
	default:
		if jsonOut {
			enc.Encode(map[string]any{"type": "cancelled", "id": rec.ID})
		} else {
			fmt.Fprintln(os.Stderr, "\ncancelled")
		}
		return true
	}
}

func runSend(ctx context.Context, ln *lantern.Lantern, opts cliOptions) {
	if len(opts.positional) < 1 {
		fatal(opts.jsonOut, fmt.Errorf("usage: lantern send <path>"))
	}
	path := opts.positional[0]

	session, peer, err := ln.ShareSession(ctx, path)
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	defer session.Close()

	enc := json.NewEncoder(os.Stdout)
	if opts.jsonOut {
		enc.Encode(map[string]any{
			"type":      "share",
			"code":      peer.Code,
			"file_name": peer.FileName,
			"file_size": peer.FileSize,
		})
	} else {
		fmt.Printf("share code: %s\n", peer.Code)
		fmt.Fprintln(os.Stderr, "waiting for receiver...")
	}

	failed := runTransfer(ctx, session.Events(), enc, opts.jsonOut, "sent", peer.FileName)
	if failed {
		exit(1)
	}
}

func runReceive(ctx context.Context, ln *lantern.Lantern, opts cliOptions) {
	if len(opts.positional) < 1 {
		fatal(opts.jsonOut, fmt.Errorf("usage: lantern receive <code> [output-dir]"))
	}
	code := strings.TrimSpace(opts.positional[0])
	outputDir := opts.outDir
	if len(opts.positional) > 1 && opts.positional[1] != "" {
		outputDir = opts.positional[1]
	}

	session, peer, err := ln.ReceiveSession(ctx, code, outputDir)
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	defer session.Close()

	enc := json.NewEncoder(os.Stdout)
	if opts.jsonOut {
		enc.Encode(map[string]any{
			"type":    "peer",
			"peer_id": peer.ID,
			"code":    peer.Code,
		})
	} else {
		fmt.Fprintf(os.Stderr, "connecting to %s...\n", peer.ID)
	}

	failed := runTransfer(ctx, session.Events(), enc, opts.jsonOut, "received:", "")
	if failed {
		exit(1)
	}
}

// runTransfer renders session events until the transfer completes, fails,
// or the context is cancelled. In JSON mode every event is a JSON object on
// stdout for agents/GUI; in human mode progress goes to stderr and only the
// final result touches stdout. It reports whether the transfer failed.
//
// It takes the event channel rather than the session. A session is a lifecycle
// with states and a Done channel, and none of that is used here: this loop only
// ever reads events. Taking the channel is what makes the rendering -- the part
// with the actual branches -- drivable from a test, which a session could not
// be, because sessions are only built inside pkg/lantern.
func runTransfer(ctx context.Context, events <-chan lantern.Event, enc *json.Encoder, jsonOut bool, doneVerb, doneName string) bool {
	for {
		select {
		case e, ok := <-events:
			if !ok {
				// The channel closed without a terminal event, so the transfer
				// did not complete. Returning false here -- success -- made
				// `lantern send` exit 0 having printed nothing at all, which
				// is how an aborted or cancelled send reported itself as a
				// finished one. A Done event is always emitted before the
				// close, so reaching here means there was none.
				if jsonOut {
					enc.Encode(map[string]any{
						"type":  "error",
						"error": "the transfer ended without completing",
					})
				} else {
					fmt.Fprintln(os.Stderr, "\nerror: the transfer ended without completing")
				}
				return true
			}
			switch e.Type {
			case lantern.EventTransferProgress:
				if jsonOut {
					enc.Encode(map[string]any{
						"type":      "progress",
						"file_name": e.FileName,
						"bytes":     e.Bytes,
						"total":     e.Total,
					})
				} else {
					pct := progressPercent(e.Bytes, e.Total)
					fmt.Fprintf(os.Stderr, "\rprogress: %.1f%% (%s / %s)", pct, format.Bytes(e.Bytes), format.Bytes(e.Total))
				}
			case lantern.EventTransferDone:
				name := doneName
				if name == "" {
					name = e.FileName
				}
				if jsonOut {
					enc.Encode(map[string]any{
						"type":      "done",
						"file_name": name,
						"bytes":     e.Bytes,
						"total":     e.Total,
					})
				} else {
					fmt.Fprintf(os.Stderr, "\n%s %s\n", doneVerb, name)
				}
				return false
			case lantern.EventError:
				if jsonOut {
					enc.Encode(map[string]any{
						"type":  "error",
						"error": e.Err.Error(),
					})
				} else {
					fmt.Fprintf(os.Stderr, "\nerror: %v\n", e.Err)
				}
				return true
			}
		case <-ctx.Done():
			if jsonOut {
				enc.Encode(map[string]any{"type": "cancelled"})
			} else {
				fmt.Fprintln(os.Stderr, "\ncancelled")
			}
			return true
		}
	}
}

// exit is os.Exit, indirected so a test can drive a command that would
// otherwise take the whole test process down with it. Every command below
// reports failure through fatal, so without this seam none of them is reachable
// from a test at all.
var exit = os.Exit

func fatal(jsonOut bool, err error) {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.Encode(map[string]any{"type": "error", "error": err.Error()})
	} else {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	exit(1)
}

func handleSignal(cancel context.CancelFunc, jsonOut bool) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, shutdownSignals()...)
	defer signal.Stop(sig)
	<-sig
	if !jsonOut {
		fmt.Fprintln(os.Stderr, "\ninterrupted")
	}
	cancel()
}

func progressPercent(bytes, total int64) float64 {
	if total <= 0 {
		return 100
	}
	return format.Ratio(bytes, total) * 100
}
