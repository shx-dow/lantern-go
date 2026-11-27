//go:build linux

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner replaces the command runner for one test. Tests in this file do
// not run in parallel: the hook is package state.
func fakeRunner(t *testing.T, fn func(context.Context, string, ...string) ([]byte, error)) {
	t.Helper()
	old := commandRunner
	commandRunner = fn
	t.Cleanup(func() { commandRunner = old })
}

func writeUnit(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := unitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(systemdUnit("/usr/local/bin/lantern")), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInspectReportsInstalledAndRunning(t *testing.T) {
	path := writeUnit(t)
	fakeRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "systemctl" && containsArg(args, "is-active"):
			return []byte("active\n"), nil
		case name == "loginctl":
			return []byte("Linger=yes\n"), nil
		}
		return nil, errors.New("unexpected command: " + name + " " + strings.Join(args, " "))
	})

	st, err := Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Supported || !st.Installed || !st.Running {
		t.Fatalf("status = %+v, want supported, installed, and running", st)
	}
	if st.UnitPath != path {
		t.Errorf("unit path = %q, want %q", st.UnitPath, path)
	}
	if st.Fix != "" {
		t.Errorf("a running service should have no fix, got %q", st.Fix)
	}
	if st.Linger == nil || !*st.Linger {
		t.Errorf("linger = %v, want true", st.Linger)
	}
}

func TestInspectReportsInstalledButStopped(t *testing.T) {
	writeUnit(t)
	fakeRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "systemctl" && containsArg(args, "is-active"):
			return []byte("inactive\n"), errors.New("exit status 3")
		case name == "loginctl":
			return []byte("Linger=no\n"), nil
		}
		return nil, errors.New("unexpected command: " + name)
	})

	st, err := Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || st.Running {
		t.Fatalf("status = %+v, want installed but not running", st)
	}
	if !strings.Contains(st.Fix, "systemctl --user start "+systemdUnitName) {
		t.Errorf("fix = %q, want the start command", st.Fix)
	}
	if st.Linger == nil || *st.Linger {
		t.Errorf("linger = %v, want false", st.Linger)
	}
	if !strings.Contains(st.Detail, "lingering is off") {
		t.Errorf("detail does not mention lingering: %q", st.Detail)
	}
}

func TestInspectReportsNotInstalledWithoutRunningCommands(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fakeRunner(t, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		t.Fatalf("Inspect ran %s for a unit that does not exist", name)
		return nil, nil
	})

	st, err := Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Installed || st.Running {
		t.Fatalf("status = %+v, want not installed", st)
	}
	if st.Fix != "lantern service install" {
		t.Errorf("fix = %q, want the install command", st.Fix)
	}
}

func TestInstallWritesTheUnitAndEnablesIt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var calls [][]string
	fakeRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		switch {
		case name == "systemctl" && containsArg(args, "is-active"):
			return []byte("active\n"), nil
		case name == "loginctl":
			return []byte("Linger=yes\n"), nil
		}
		return nil, nil
	})

	st, err := Install(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Running {
		t.Fatalf("status after install = %+v, want installed and running", st)
	}
	body, err := os.ReadFile(unitPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "ExecStart=") || !strings.Contains(string(body), " daemon") {
		t.Errorf("installed unit does not run the daemon:\n%s", body)
	}
	for _, want := range [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", systemdUnitName},
	} {
		if !hasCall(calls, want) {
			t.Errorf("install did not run %v; calls: %v", want, calls)
		}
	}
}

func TestUninstallStopsAndRemovesTheUnit(t *testing.T) {
	path := writeUnit(t)
	var calls [][]string
	fakeRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	})

	removed, err := Uninstall(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("uninstall reported nothing removed")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit still exists after uninstall: %v", err)
	}
	if !hasCall(calls, []string{"systemctl", "--user", "disable", "--now", systemdUnitName}) {
		t.Errorf("uninstall did not stop the unit; calls: %v", calls)
	}
	if !hasCall(calls, []string{"systemctl", "--user", "daemon-reload"}) {
		t.Errorf("uninstall did not reload the user manager; calls: %v", calls)
	}
}

func TestUninstallWithoutAUnitDoesNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fakeRunner(t, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		t.Fatalf("Uninstall ran %s with no unit installed", name)
		return nil, nil
	})
	removed, err := Uninstall(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("uninstall reported a removal with no unit installed")
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func hasCall(calls [][]string, want []string) bool {
	for _, call := range calls {
		if len(call) != len(want) {
			continue
		}
		same := true
		for i := range want {
			if call[i] != want[i] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}
