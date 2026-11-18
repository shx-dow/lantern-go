package main

import (
	"strings"
	"testing"
)

func TestParseArgsNormalizesAliases(t *testing.T) {
	for _, c := range []struct {
		name    string
		args    []string
		wantCmd string
		wantPos []string
		wantErr bool
	}{
		{name: "ls alone", args: []string{"ls"}, wantCmd: "files"},
		{name: "ls with a dir", args: []string{"ls", "notes"}, wantCmd: "files", wantPos: []string{"notes"}},
		{name: "devices", args: []string{"devices"}, wantCmd: "trust", wantPos: []string{"list"}},
		{name: "devices with an argument", args: []string{"devices", "extra"}, wantErr: true},
		// An alias is a command name, not a word that turns up anywhere.
		{name: "device word as an argument", args: []string{"send", "devices"}, wantCmd: "send", wantPos: []string{"devices"}},
		{name: "device word under trust", args: []string{"trust", "devices"}, wantCmd: "trust", wantPos: []string{"devices"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts, err := parseArgs(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseArgs(%v) accepted arguments it should refuse", c.args)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if opts.command != c.wantCmd {
				t.Errorf("command = %q, want %q", opts.command, c.wantCmd)
			}
			if strings.Join(opts.positional, " ") != strings.Join(c.wantPos, " ") {
				t.Errorf("positional = %v, want %v", opts.positional, c.wantPos)
			}
		})
	}
}

func TestAliasHelpDescribesTheRealCommand(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"help", "ls"}, "lantern files"},
		{[]string{"ls", "--help"}, "lantern files"},
		{[]string{"help", "devices"}, "lantern trust"},
		{[]string{"devices", "--help"}, "lantern trust"},
	} {
		stdout, stderr, code := runCLI(t, c.args...)
		if code != 0 {
			t.Fatalf("lantern %v exited %d\nstdout: %s\nstderr: %s", c.args, code, stdout, stderr)
		}
		if out := stdout + stderr; !strings.Contains(out, c.want) {
			t.Errorf("lantern %v did not name %q:\n%s", c.args, c.want, out)
		}
	}
}

func TestAliasesAreListedInTopLevelHelp(t *testing.T) {
	stdout, stderr, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("lantern help exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	out := stdout + stderr
	for _, want := range []string{"ls [dir]", "devices"} {
		if !strings.Contains(out, want) {
			t.Errorf("top-level help does not list %q:\n%s", want, out)
		}
	}
}

func TestLsAliasListsTheDaemonsFiles(t *testing.T) {
	d := newFakeDaemon(t)

	stdout, stderr, code := drive(t, d, "ls")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "notes.md") {
		t.Errorf("ls output is missing the file the daemon sent:\n%s", stdout)
	}
	if !requested(d, "GET /v1/files") {
		t.Errorf("ls did not ask the daemon for files: %v", d.called())
	}
}

func TestDevicesAliasListsPairedDevices(t *testing.T) {
	d := newFakeDaemon(t)

	stdout, stderr, code := drive(t, d, "devices")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"12D3kooONE", "nas"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("devices output is missing %q:\n%s", want, stdout)
		}
	}
	if !requested(d, "GET /v1/trust") {
		t.Errorf("devices did not ask the daemon for paired devices: %v", d.called())
	}
}

func requested(d *fakeDaemon, want string) bool {
	for _, r := range d.called() {
		if r == want {
			return true
		}
	}
	return false
}
