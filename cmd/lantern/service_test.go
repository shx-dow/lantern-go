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
