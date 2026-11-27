package main

import (
	"runtime"
	"strings"
	"testing"

	"github.com/shx-dow/lantern-go/internal/doctor"
)

// A missing service registration is the state before install, and it must be
// reported as such rather than as an unknown command. This is the negative
// control for `lantern service`: before the command existed, the binary
// answered "unknown command" and exited 2.
func TestServiceStatusReportsNotInstalled(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the Linux status path is the one that can be described with a temporary XDG_CONFIG_HOME")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	stdout, stderr, code := runCLI(t, "service", "status")
	out := stdout + stderr
	if code == 0 {
		t.Fatalf("status of a missing unit exited zero:\n%s", out)
	}
	if !strings.Contains(out, "no systemd user unit") {
		t.Errorf("status did not describe the missing unit:\n%s", out)
	}
	if !strings.Contains(out, "lantern service install") {
		t.Errorf("status did not name the fix:\n%s", out)
	}
}

// The service check describes this machine, so it is only meaningful when
// doctor is pointed at a loopback daemon. A remote daemon has its own service
// on its own machine.
func TestDoctorChecksTheServiceOnlyForALoopbackDaemon(t *testing.T) {
	if doctorOptions(t.Context(), "http://127.0.0.1:43782", doctor.Options{}).ServiceStatus == nil {
		t.Error("a loopback daemon should get the local service check")
	}
	if doctorOptions(t.Context(), "http://192.0.2.1:43782", doctor.Options{}).ServiceStatus != nil {
		t.Error("a remote daemon should not get the local service check")
	}
}

// The test above pins that the command exists. These pin that install accepts
// the daemon's own flags: without the parser stopping at "service", the global
// flag set rejected --allow-writes with "flag provided but not defined" before
// the service command ever ran.
func TestServiceInstallFlagsReachTheCommand(t *testing.T) {
	opts, err := parseArgs([]string{"service", "install", "--allow-writes", "--p2p-port", "41001"})
	if err != nil {
		t.Fatalf("parseArgs rejected the daemon flags: %v", err)
	}
	if opts.command != "service" {
		t.Fatalf("command = %q, want service", opts.command)
	}
	want := []string{"install", "--allow-writes", "--p2p-port", "41001"}
	if strings.Join(opts.positional, " ") != strings.Join(want, " ") {
		t.Errorf("positional = %v, want %v", opts.positional, want)
	}

	// A global flag before the command still applies.
	opts, err = parseArgs([]string{"--json", "service", "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.jsonOut || opts.command != "service" || strings.Join(opts.positional, " ") != "status" {
		t.Errorf("parseArgs(--json service status) = %+v", opts)
	}
}

func TestServiceDaemonArgsOnlyIncludeWhatWasChosen(t *testing.T) {
	if got := serviceDaemonArgs(false, "", -1, "", "", "", "", false); len(got) != 0 {
		t.Errorf("unset flags produced %v, want none", got)
	}
	got := serviceDaemonArgs(true, "/home/me/In box", 41001, "/ip4/10.0.0.2/tcp/4001/p2p/12D3Koo", "/tmp/state", "127.0.0.1:43782", "/tmp/lanternd.json", true)
	want := []string{
		"--allow-writes",
		"--writable-dirs=/home/me/In box",
		"--p2p-port=41001",
		"--peer=/ip4/10.0.0.2/tcp/4001/p2p/12D3Koo",
		"--data-dir=/tmp/state",
		"--addr=127.0.0.1:43782",
		"--config=/tmp/lanternd.json",
		"--allow-remote",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("serviceDaemonArgs = %v, want %v", got, want)
	}
}

// `lantern service --help` no longer reaches the global help path, because the
// service subcommand owns its arguments now. It still has to print the usage
// and exit zero.
func TestServiceHelpStillPrintsUsage(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"help"}, {"install", "--help"}} {
		var out strings.Builder
		if err := runService(t.Context(), args, &out); err != nil {
			t.Fatalf("runService(%v) = %v", args, err)
		}
		if !strings.Contains(out.String(), "lantern service") {
			t.Errorf("runService(%v) did not print the usage:\n%s", args, out.String())
		}
	}
}

func TestServiceInstallRejectsAnUnknownFlag(t *testing.T) {
	var out strings.Builder
	err := runService(t.Context(), []string{"install", "--allow-write"}, &out)
	if err == nil {
		t.Fatal("install accepted an unknown flag")
	}
	if !strings.Contains(err.Error(), "allow-write") {
		t.Errorf("error does not name the flag: %v", err)
	}
}

// The top-level help has to mention the new command, and `<command> --help`
// has to name it on the first line. The shared help test covers the second
// property; this pins the first.
func TestTopLevelHelpMentionsService(t *testing.T) {
	stdout, stderr, code := runCLI(t, "help")
	out := stdout + stderr
	if code != 0 {
		t.Fatalf("lantern help exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "service install|uninstall|status") {
		t.Errorf("top-level help does not mention the service command:\n%s", out)
	}
}
