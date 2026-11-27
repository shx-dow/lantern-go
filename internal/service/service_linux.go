//go:build linux

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// systemdUnitName is the unit's name under the user manager.
const systemdUnitName = "lanternd.service"

// unitPath is the systemd user unit for this user. XDG_CONFIG_HOME is
// honoured because systemd does, so a user with a relocated config directory
// still finds the unit.
func unitPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "systemd", "user", systemdUnitName)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "lantern", "systemd", "user", systemdUnitName)
	}
	return filepath.Join(home, ".config", "systemd", "user", systemdUnitName)
}

// Install writes the user unit, reloads the user manager, and enables and
// starts the service. It is idempotent: a second install replaces the file
// and leaves an already-active service running.
func Install(ctx context.Context) (Status, error) {
	exe, err := executablePath()
	if err != nil {
		return Status{}, err
	}
	path := unitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Status{}, fmt.Errorf("create unit directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(systemdUnit(exe)), 0o644); err != nil {
		return Status{}, fmt.Errorf("write unit %s: %w", path, err)
	}
	if out, err := commandRunner(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return Status{}, fmt.Errorf("systemctl --user daemon-reload: %w%s",
			missingCommand(err, "systemctl", "systemd user services are not available here; run \"lantern daemon\" under your init system instead"),
			outputSuffix(out))
	}
	if out, err := commandRunner(ctx, "systemctl", "--user", "enable", "--now", systemdUnitName); err != nil {
		return Status{}, fmt.Errorf("systemctl --user enable --now %s: %w%s", systemdUnitName, err, outputSuffix(out))
	}
	return Inspect(ctx)
}

// Uninstall stops and disables the unit, then removes it. removed reports
// whether there was a unit to remove; a second uninstall is not an error.
func Uninstall(ctx context.Context) (removed bool, err error) {
	path := unitPath()
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read unit %s: %w", path, err)
	}
	// A unit that is not loaded makes disable fail; the file still has to go.
	_, _ = commandRunner(ctx, "systemctl", "--user", "disable", "--now", systemdUnitName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove unit %s: %w", path, err)
	}
	_, _ = commandRunner(ctx, "systemctl", "--user", "daemon-reload")
	return true, nil
}

// Inspect reports whether the unit exists, whether it is active, and whether
// the user manager survives logout. It runs only read-only systemctl and
// loginctl queries.
func Inspect(ctx context.Context) (Status, error) {
	st := Status{Supported: true, Platform: "systemd", UnitPath: unitPath()}
	fi, err := os.Stat(st.UnitPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		st.Detail = "no systemd user unit at " + st.UnitPath
		st.Fix = "lantern service install"
		return st, nil
	case err != nil:
		return st, fmt.Errorf("read unit %s: %w", st.UnitPath, err)
	case fi.IsDir():
		return st, fmt.Errorf("%s is a directory, not a unit file", st.UnitPath)
	}
	st.Installed = true

	out, err := commandRunner(ctx, "systemctl", "--user", "is-active", systemdUnitName)
	if err != nil {
		if missing := missingCommand(err, "systemctl", "systemd user services are not available here"); missing != err {
			return st, missing
		}
		// systemctl exists but no user manager is running (a container, WSL
		// without systemd, or a host booted with another init). That is not
		// "the service is stopped": starting it would fail the same way.
		text := strings.ToLower(string(out))
		if strings.Contains(text, "failed to connect to bus") || strings.Contains(text, "system has not been booted with systemd") {
			return st, fmt.Errorf("systemd user manager is not available: %s", strings.TrimSpace(string(out)))
		}
		// is-active exits non-zero for inactive and failed units. That is an
		// answer, not a failure to read one.
	}
	st.Running = strings.TrimSpace(string(out)) == "active"

	if st.Running {
		st.Detail = "systemd user unit at " + st.UnitPath + " is active"
	} else {
		st.Detail = "systemd user unit at " + st.UnitPath + " is installed but not running"
		st.Fix = "systemctl --user start " + systemdUnitName
	}

	if linger, ok := lingerEnabled(ctx); ok {
		st.Linger = &linger
		if !linger {
			st.Detail += "; lingering is off, so it stops when you log out"
		}
	}
	return st, nil
}

// lingerEnabled asks systemd whether this user's manager survives logout. A
// host without loginctl, or a user with no logind session, answers "unknown"
// rather than "off", so the check does not invent a warning.
func lingerEnabled(ctx context.Context) (bool, bool) {
	out, err := commandRunner(ctx, "loginctl", "show-user", strconv.Itoa(os.Getuid()), "--property=Linger")
	if err != nil {
		return false, false
	}
	return strings.TrimSpace(string(out)) == "Linger=yes", true
}
