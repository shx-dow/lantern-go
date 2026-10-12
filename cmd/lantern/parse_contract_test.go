package main

import (
	"reflect"
	"strings"
	"testing"
)

// parseArgs is about to be rewritten onto flag.FlagSet. These pin the behaviour
// that rewrite has to reproduce, including the parts that are easy to lose:
//
//   - flags are accepted anywhere, not just before the command. `lantern send
//     photo.jpg --json` parses --json today, and Go's flag package stops at the
//     first non-flag argument unless the parse is looped.
//   - --daemon is a flag that also takes a value, which is not something the
//     flag package does for bools without help.
//   - the empty `--flag=` case falls back rather than clearing, which is the
//     bug #32 fixed and the thing most likely to regress silently.
//
// Anything asserted here is a promise the rewrite must keep or change
// deliberately, not by accident.

func TestFlagsAreAcceptedAnywhereInTheArgumentList(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"flag before command", []string{"--json", "send", "a.txt"}},
		{"flag after command", []string{"send", "a.txt", "--json"}},
		{"flag between command and argument", []string{"send", "--json", "a.txt"}},
		{"global before an argument", []string{"send", "--daemon", "a.txt"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, err := parseArgs(c.args)
			if err != nil {
				t.Fatalf("parseArgs(%v) = %v", c.args, err)
			}
			if opts.command != "send" {
				t.Errorf("command = %q, want send", opts.command)
			}
			if !opts.jsonOut && !opts.daemon {
				t.Errorf("the flag was dropped: %+v", opts)
			}
			if !reflect.DeepEqual(opts.positional, []string{"a.txt"}) {
				t.Errorf("positional = %v, want [a.txt]", opts.positional)
			}
		})
	}
}

// --daemon is spelled four ways and every one has to keep working, because
// scripts and docs in the wild use all of them.
func TestDaemonFlagSpellings(t *testing.T) {
	const want = "http://10.0.0.5:1234"
	for _, args := range [][]string{
		{"--daemon=" + want, "status"},
		{"--daemon-url", want, "status"},
		{"--daemon-url=" + want, "status"},
		{"--daemon-addr", want, "status"},
		{"--daemon-addr=" + want, "status"},
	} {
		opts, err := parseArgs(args)
		if err != nil {
			t.Fatalf("parseArgs(%v) = %v", args, err)
		}
		if !opts.daemon {
			t.Errorf("parseArgs(%v): --daemon not set", args)
		}
		if opts.daemonURL != want {
			t.Errorf("parseArgs(%v): daemonURL = %q, want %q", args, opts.daemonURL, want)
		}
		if opts.command != "status" {
			t.Errorf("parseArgs(%v): command = %q, want status", args, opts.command)
		}
	}

	// Bare --daemon selects the default URL rather than clearing it.
	opts, err := parseArgs([]string{"--daemon", "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.daemon || opts.daemonURL != defaultDaemonURL {
		t.Errorf("bare --daemon gave %+v, want daemon with %q", opts, defaultDaemonURL)
	}
}

// --writable-roots and --roots are the same flag under two names.
func TestWritableRootsSpellings(t *testing.T) {
	want := []string{"/srv/inbox", "/tmp/out"}
	for _, args := range [][]string{
		{"--writable-roots", "/srv/inbox,/tmp/out", "trust", "add", "peer"},
		{"--writable-roots=/srv/inbox,/tmp/out", "trust", "add", "peer"},
		{"--roots", "/srv/inbox,/tmp/out", "trust", "add", "peer"},
		{"--roots=/srv/inbox,/tmp/out", "trust", "add", "peer"},
	} {
		opts, err := parseArgs(args)
		if err != nil {
			t.Fatalf("parseArgs(%v) = %v", args, err)
		}
		if !reflect.DeepEqual(opts.writableRoots, want) {
			t.Errorf("parseArgs(%v): writableRoots = %v, want %v", args, opts.writableRoots, want)
		}
	}
}

// A bare -h before any command means "show help". After a command it belongs to
// the command, so `lantern help daemon` keeps working while `lantern daemon -h`
// is routed as a subcommand before parseArgs ever sees it.
func TestHelpFlagRouting(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}} {
		opts, err := parseArgs(args)
		if err != nil {
			t.Fatalf("parseArgs(%v) = %v", args, err)
		}
		if opts.command != "help" {
			t.Errorf("parseArgs(%v): command = %q, want help", args, opts.command)
		}
	}

	opts, err := parseArgs([]string{"help", "daemon"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.command != "help" || !reflect.DeepEqual(opts.positional, []string{"daemon"}) {
		t.Errorf("parseArgs(help daemon) = %+v, want command help with [daemon]", opts)
	}

	// After a real command, --help is passed through rather than hijacked.
	opts, err = parseArgs([]string{"send", "--help"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.command != "send" || !reflect.DeepEqual(opts.positional, []string{"--help"}) {
		t.Errorf("parseArgs(send --help) = %+v, want command send with [--help]", opts)
	}
}

// Anything that looks like a flag and is not one is an error, rather than being
// silently treated as a command or an argument.
func TestUnknownFlagIsRejected(t *testing.T) {
	for _, args := range [][]string{
		{"--nope"},
		{"--nope=1"},
		{"send", "a.txt", "--nope"},
		{"-z"},
	} {
		_, err := parseArgs(args)
		if err == nil {
			t.Fatalf("parseArgs(%v) accepted an unknown flag", args)
		}
		// The wording is flag.FlagSet's, and is not worth pinning; that the
		// rejected spelling is named is.
		last := args[len(args)-1]
		name, _, _ := strings.Cut(strings.TrimLeft(last, "-"), "=")
		if !strings.Contains(err.Error(), name) {
			t.Errorf("parseArgs(%v) = %v, want the error to name %q", args, err, name)
		}
	}
}

// A bare "-" is not a flag; it is what a user means by stdin.
func TestLoneDashIsNotAFlag(t *testing.T) {
	opts, err := parseArgs([]string{"send", "-"})
	if err != nil {
		t.Fatalf("a lone dash must not be parsed as a flag: %v", err)
	}
	if opts.command != "send" || !reflect.DeepEqual(opts.positional, []string{"-"}) {
		t.Errorf("parseArgs(send -) = %+v, want command send with [-]", opts)
	}

	// On its own it is not a command either — `lantern -` shows usage. The old
	// parser took it as a command name and went on to report an unknown one.
	opts, err = parseArgs([]string{"-"})
	if err != nil {
		t.Fatalf("a lone dash must not be an unknown flag: %v", err)
	}
	if opts.command != "" {
		t.Errorf("parseArgs(-) = %+v, want no command", opts)
	}
}

// Value-taking flags must report a missing value rather than consuming the
// command name and then failing somewhere less obvious.
func TestValueFlagsReportMissingValues(t *testing.T) {
	for _, flag := range []string{
		"--daemon-url", "--daemon-addr", "--daemon-token",
		"--out", "--port", "--data-dir", "--tier", "--writable-roots", "--roots",
	} {
		if _, err := parseArgs([]string{flag}); err == nil {
			t.Errorf("parseArgs(%q) accepted a flag with no value", flag)
		}
	}
}

// --port is an int, and a bad value has to name the value it choked on.
func TestPortIsValidated(t *testing.T) {
	for _, args := range [][]string{
		{"--port", "notanumber"},
		{"--port=notanumber"},
	} {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%v) accepted a non-numeric port", args)
		} else if !strings.Contains(err.Error(), "notanumber") {
			t.Errorf("parseArgs(%v) = %v, want the bad value named", args, err)
		}
	}
	opts, err := parseArgs([]string{"--port", "4242"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.port != 4242 {
		t.Errorf("port = %d, want 4242", opts.port)
	}
}

// --probe-peers is a bool that tolerates "=false" for symmetry with the others.
func TestProbePeersForms(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"--probe-peers"}, true},
		{[]string{"--probe-peers=true"}, true},
		{[]string{"--probe-peers=false"}, false},
		{[]string{"--probe-peers=no"}, false},
		{[]string{}, false},
	} {
		opts, err := parseArgs(c.args)
		if err != nil {
			t.Fatalf("parseArgs(%v) = %v", c.args, err)
		}
		if opts.probePeers != c.want {
			t.Errorf("parseArgs(%v): probePeers = %v, want %v", c.args, opts.probePeers, c.want)
		}
	}
}

// Defaults come from the environment before any flag is seen, so an empty flag
// value has something to fall back to.
func TestDefaultsComeFromEnvironment(t *testing.T) {
	t.Setenv("LANTERND_URL", "http://127.0.0.1:59999")
	t.Setenv("LANTERN_DAEMON_TOKEN", "tok")

	opts, err := parseArgs([]string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.daemonURL != "http://127.0.0.1:59999" {
		t.Errorf("daemonURL = %q, want the environment value", opts.daemonURL)
	}
	if opts.daemonToken != "tok" {
		t.Errorf("daemonToken = %q, want the environment value", opts.daemonToken)
	}
	if opts.outDir != "." {
		t.Errorf("outDir = %q, want the default \".\"", opts.outDir)
	}
}

// A daemon command is routed to the daemon whatever the caller passed. The
// routing itself happens in run() rather than parseArgs(), so what is pinned
// here is the classification it keys off; TestDaemonCommandUsesTheDaemon
// exercises the routing through the built binary.
func TestDaemonCommandsRouteRegardlessOfFlag(t *testing.T) {
	for _, cmd := range []string{"status", "discover", "peers", "files", "list", "trust", "remote-files"} {
		if !commandNeedsDaemon(cmd) {
			t.Errorf("%q is served only by the daemon but is not classified as one", cmd)
		}
	}
	// send and receive keep their choice, so they must not be forced.
	for _, cmd := range []string{"send", "receive"} {
		if commandNeedsDaemon(cmd) {
			t.Errorf("%q also runs locally but is forced onto the daemon", cmd)
		}
	}
}

// Two behaviours the rewrite deliberately does not reproduce. The hand-rolled
// parser treated `--` as an unknown flag and only ever recognised two-dash
// spellings; flag.FlagSet does the opposite on both counts. Both are standard
// Unix behaviour and neither had a test, so nothing depended on the old
// reading — but they are pinned here so a later change to them is deliberate.
func TestStandardTerminatorAndSingleDashForms(t *testing.T) {
	// `--` ends flag parsing, so what follows is an argument even when it
	// looks like a flag.
	opts, err := parseArgs([]string{"send", "--", "--json"})
	if err != nil {
		t.Fatalf("`--` should end flag parsing: %v", err)
	}
	if opts.command != "send" {
		t.Errorf("command = %q, want send", opts.command)
	}
	if !reflect.DeepEqual(opts.positional, []string{"--json"}) {
		t.Errorf("positional = %v, want [--json]", opts.positional)
	}
	if opts.jsonOut {
		t.Error("a flag after `--` must not be parsed as one")
	}

	// Single-dash spellings, accepted because that is what the flag package
	// does and rejecting them helped nobody.
	opts, err = parseArgs([]string{"-json", "send", "a.txt"})
	if err != nil {
		t.Fatalf("single-dash flags should be accepted: %v", err)
	}
	if !opts.jsonOut || opts.command != "send" {
		t.Errorf("got %+v, want json output on command send", opts)
	}
}

// A value-less `--flag=` falls back rather than clearing, for every flag that
// takes a value. #32 fixed this for --daemon-url and the rewrite must not lose
// it for the rest.
func TestEmptyValueFallsBackRatherThanClearing(t *testing.T) {
	t.Setenv("LANTERND_URL", "http://127.0.0.1:59999")

	for _, args := range [][]string{
		{"--daemon="},
		{"--daemon-url="},
		{"--daemon-addr="},
	} {
		opts, err := parseArgs(args)
		if err != nil {
			t.Fatalf("parseArgs(%v) = %v", args, err)
		}
		if !opts.daemon {
			t.Errorf("parseArgs(%v): naming the flag should still route to the daemon", args)
		}
		if opts.daemonURL != "http://127.0.0.1:59999" {
			t.Errorf("parseArgs(%v): daemonURL = %q, want the environment value", args, opts.daemonURL)
		}
	}
}

// `--help` wins when it comes before the command, and belongs to the command
// when it comes after. The rewrite cannot read that off the parsed result, so
// it is tracked while parsing; this is the case that gets it wrong.
func TestHelpBeforeACommandIsNotSwallowed(t *testing.T) {
	for _, args := range [][]string{
		{"--help", "send", "a.txt"},
		{"-h", "send", "a.txt"},
		{"--json", "--help", "send", "a.txt"},
	} {
		opts, err := parseArgs(args)
		if err != nil {
			t.Fatalf("parseArgs(%v) = %v", args, err)
		}
		if opts.command != "help" {
			t.Errorf("parseArgs(%v): command = %q, want help — it asked for help before naming a command", args, opts.command)
		}
	}
}

// `--` ends flag parsing for the whole command line, not just the argument
// after it. The parse here is re-run on what is left over, so a `--` handed to
// flag.FlagSet would protect one argument and then let the next round parse the
// rest as flags again: `receive -- ABC12 --json` lost --json as an output
// directory and downloaded into the default one instead.
func TestTerminatorProtectsEveryArgumentAfterIt(t *testing.T) {
	opts, err := parseArgs([]string{"receive", "--", "ABC12", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.command != "receive" {
		t.Errorf("command = %q, want receive", opts.command)
	}
	if !reflect.DeepEqual(opts.positional, []string{"ABC12", "--json"}) {
		t.Errorf("positional = %v, want [ABC12 --json]", opts.positional)
	}
	if opts.jsonOut {
		t.Error("a flag after `--` must not be parsed as one")
	}

	// More than one argument after the terminator, and one that looks like a
	// value-taking flag.
	opts, err = parseArgs([]string{"receive", "--", "ABC12", "--out", "/tmp/x"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opts.positional, []string{"ABC12", "--out", "/tmp/x"}) {
		t.Errorf("positional = %v, want the terminator to cover them all", opts.positional)
	}
	if opts.outDir != "." {
		t.Errorf("outDir = %q, want the default; --out after `--` is an argument", opts.outDir)
	}

	// The terminator may come first, leaving nothing to parse.
	opts, err = parseArgs([]string{"--", "send", "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.command != "send" || !reflect.DeepEqual(opts.positional, []string{"a.txt"}) {
		t.Errorf("got %+v, want command send with [a.txt]", opts)
	}
}

// A command's own --help keeps its position, so the arguments the command acts
// on are the same ones it acted on before. Appending it at the end reordered
// them, and `send --help a.txt` went from failing on a missing --help to
// sharing a.txt — a transfer the user did not ask for.
func TestHelpAfterACommandKeepsItsPosition(t *testing.T) {
	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{"send", "--help", "a.txt"}, []string{"--help", "a.txt"}},
		{[]string{"send", "a.txt", "--help"}, []string{"a.txt", "--help"}},
		// "add" is trust's own subcommand, so it is an argument here.
		{[]string{"trust", "add", "--help", "peer"}, []string{"add", "--help", "peer"}},
	} {
		opts, err := parseArgs(c.args)
		if err != nil {
			t.Fatalf("parseArgs(%v) = %v", c.args, err)
		}
		if !reflect.DeepEqual(opts.positional, c.want) {
			t.Errorf("parseArgs(%v): positional = %v, want %v", c.args, opts.positional, c.want)
		}
	}

	// A flag before the --help is still read; only --help moved.
	opts, err := parseArgs([]string{"trust", "add", "--tier", "read", "--help", "peer"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.tier != "read" {
		t.Errorf("tier = %q, want read", opts.tier)
	}
	if !reflect.DeepEqual(opts.positional, []string{"add", "--help", "peer"}) {
		t.Errorf("positional = %v, want [add --help peer]", opts.positional)
	}
}

// --port is decimal. flag.IntVar reads a leading zero as octal, which turned
// `--port 04242` into 2210 and refused `--port 08080` as a bad octal digit —
// a padded port would silently open a different one.
func TestPortIsAlwaysDecimal(t *testing.T) {
	for _, c := range []struct {
		value string
		want  int
	}{
		{"4242", 4242},
		{"04242", 4242},
		{"08080", 8080},
		{"0", 0},
		{"65535", 65535},
	} {
		opts, err := parseArgs([]string{"--port", c.value, "status"})
		if err != nil {
			t.Errorf("--port %s: %v", c.value, err)
			continue
		}
		if opts.port != c.want {
			t.Errorf("--port %s: port = %d, want %d", c.value, opts.port, c.want)
		}
	}

	// A base prefix is not a port either. 0x10 is 16 and 0o10 is 8, and
	// accepting them would mean two spellings for one value.
	for _, value := range []string{"0x10", "0o10", "0b10", "notanumber"} {
		if _, err := parseArgs([]string{"--port", value, "status"}); err == nil {
			t.Errorf("--port %s was accepted; ports are decimal", value)
		}
	}
}
