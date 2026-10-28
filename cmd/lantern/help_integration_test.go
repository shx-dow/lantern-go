package main

import (
	"strings"
	"testing"
)

// everyCommand is the full set the binary advertises. The help tests run the
// built binary because routing happens in main, not in parseArgs.
var everyCommand = []string{
	"send", "receive", "status", "list", "peers", "discover", "trust",
	"files", "remote-files", "daemon", "mcp", "doctor", "version", "help",
}

// TestEveryCommandHelpExitsZeroAndNamesTheCommand is the control for
// consistent help. Before this change:
//
//   - `daemon --help` and `doctor --help` printed usage but exited 1, because
//     flag.ErrHelp was reported as an error.
//   - `mcp --help` started the server and printed nothing.
//   - `send --help` treated --help as the path to share.
//   - `trust --help` treated --help as a trust subcommand.
//   - `version --help` printed the version instead of help.
//
// Every one of them now prints that command's usage and exits 0.
func TestEveryCommandHelpExitsZeroAndNamesTheCommand(t *testing.T) {
	for _, cmd := range everyCommand {
		for _, flag := range []string{"--help", "-h"} {
			t.Run(cmd+"/"+flag, func(t *testing.T) {
				stdout, stderr, code := runCLI(t, cmd, flag)
				if code != 0 {
					t.Fatalf("lantern %s %s exited %d\nstdout: %s\nstderr: %s",
						cmd, flag, code, stdout, stderr)
				}
				if out := stdout + stderr; !strings.Contains(out, "lantern "+cmd) {
					t.Fatalf("lantern %s %s did not print that command's usage:\n%s",
						cmd, flag, out)
				}
			})
		}
	}
}

// The top-level list is how someone finds a command they have not used yet, so
// it has to include the ones this binary actually has. daemon, mcp, and help
// were missing.
func TestTopLevelHelpListsEveryCommand(t *testing.T) {
	stdout, stderr, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("lantern help exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	out := stdout + stderr
	for _, cmd := range everyCommand {
		if !strings.Contains(out, cmd) {
			t.Errorf("top-level help does not list %q:\n%s", cmd, out)
		}
	}
	if !strings.Contains(out, "help [command]") {
		t.Errorf("top-level help does not say how to get a command's help:\n%s", out)
	}
}

// `help trust` used to print the top-level usage, because only the subcommands
// with their own argument grammar had a usageFor entry.
func TestHelpTopicShowsThatCommandsUsage(t *testing.T) {
	for _, args := range [][]string{
		{"help", "trust"},
		{"--help", "trust"},
	} {
		stdout, stderr, code := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("lantern %v exited %d\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
		}
		out := stdout + stderr
		if !strings.Contains(out, "lantern trust") || !strings.Contains(out, "trust <command>") {
			t.Errorf("lantern %v did not print trust's usage:\n%s", args, out)
		}
	}
}

// Global flags before a command are accepted, so `--json daemon --help` reaches
// the daemon rather than being read as an unknown command.
func TestGlobalFlagsBeforeACommandReachItsHelp(t *testing.T) {
	for _, c := range []struct {
		args []string
		cmd  string
	}{
		{[]string{"--json", "daemon", "--help"}, "daemon"},
		{[]string{"--json", "mcp", "--help"}, "mcp"},
		{[]string{"--json", "version", "--help"}, "version"},
		{[]string{"--json", "doctor", "--help"}, "doctor"},
		{[]string{"--json", "status", "--help"}, "status"},
		// A value-taking flag must consume its value rather than reading it
		// as the command name.
		{[]string{"--out", "/tmp/inbox", "receive", "--help"}, "receive"},
		{[]string{"--out", "/tmp/inbox", "daemon", "--help"}, "daemon"},
	} {
		stdout, stderr, code := runCLI(t, c.args...)
		if code != 0 {
			t.Fatalf("lantern %v exited %d\nstdout: %s\nstderr: %s", c.args, code, stdout, stderr)
		}
		if out := stdout + stderr; !strings.Contains(out, "lantern "+c.cmd) {
			t.Errorf("lantern %v did not print %s's usage:\n%s", c.args, c.cmd, out)
		}
	}
}
