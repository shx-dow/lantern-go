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

// Install writes the user unit with the operator's daemon arguments, reloads
// the user manager, and enables and starts the service. It is idempotent: a
// second install replaces the file and leaves an already-active service
// running, so changing an argument means simply installing again.
func Install(ctx context.Context, opts Options) (Status, error) {
	if err := validateArgs(opts.DaemonArgs); err != nil {
		return Status{}, err
	}
	if regs := otherRegistrations(); len(regs) > 0 {
		return Status{}, conflictError(regs)
	}
	exe, err := executablePath()
	if err != nil {
		return Status{}, err
	}
	path := unitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Status{}, fmt.Errorf("create unit directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(systemdUnit(exe, opts.DaemonArgs)), 0o644); err != nil {
		return Status{}, fmt.Errorf("write unit %s: %w", path, err)
	}
	if out, err := commandRunner(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return Status{}, fmt.Errorf("systemctl --user daemon-reload: %w%s",
			missingCommand(err, "systemctl", "systemd user services are not available here; run \"lantern daemon\" under your init system instead"),
			outputSuffix(out))
	}
	// enable --now returns once the start job is accepted, and Type=simple
	// reports the unit started before the process has proved it can run. A
	// daemon that immediately exits — another one holds the identity
	// directory, the port is taken — therefore shows up here as "not running",
	// not as a failed command. Ask the journal why rather than make the user
	// guess.
	if out, err := commandRunner(ctx, "systemctl", "--user", "enable", "--now", systemdUnitName); err != nil {
		return Status{}, fmt.Errorf("systemctl --user enable --now %s: %w%s", systemdUnitName, err, outputSuffix(out))
	}
	st, err := Inspect(ctx)
	if err != nil {
		return st, err
	}
	if !st.Running {
		st.LogTail = journalTail(ctx)
	}
	return st, nil
}

// journalTail returns the user journal's last lines for the unit, or "" when
// journalctl is missing or the journal has nothing yet.
func journalTail(ctx context.Context) string {
	out, err := commandRunner(ctx, "journalctl", "--user", "-u", systemdUnitName, "-n", "15", "--no-pager", "--output=cat")
	if err != nil {
		return ""
	}
	return logTail(string(out), 15)
}

// otherRegistrations finds other systemd user units that already run a Lantern
// daemon. Installing alongside one would leave both enabled, so the next login
// would start two daemons and one would fail on the identity directory. The
// search reads unit files rather than asking systemd: it covers the per-user
// and system-wide user unit directories, which is where a hand-written unit
// lives, and needs no bus connection.
func otherRegistrations() []registration {
	dirs := []string{"/etc/systemd/user"}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		dirs = append([]string{filepath.Join(x, "systemd", "user")}, dirs...)
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append([]string{filepath.Join(home, ".config", "systemd", "user")}, dirs...)
	}
	ours := unitPath()
	seen := make(map[string]bool)
	var regs []registration
	for _, dir := range dirs {
		paths, _ := filepath.Glob(filepath.Join(dir, "*.service"))
		for _, path := range paths {
			if path == ours || seen[path] {
				continue
			}
			seen[path] = true
			body, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if !runsLanternDaemon(string(body)) {
				continue
			}
			regs = append(regs, registration{
				Path:   path,
				Remove: "systemctl --user disable --now " + filepath.Base(path) + " && rm " + path,
			})
		}
	}
	return regs
}

// runsLanternDaemon reports whether a unit's ExecStart names a lantern binary
// and the daemon subcommand. The executable may carry systemd's command
// prefixes ("-", "@", "+", "!") and the path may be quoted, including when it
// contains spaces.
func runsLanternDaemon(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		fields := splitUnitArgs(strings.TrimPrefix(line, "ExecStart="))
		if len(fields) < 2 {
			continue
		}
		exe := strings.TrimLeft(fields[0], "-+!@")
		base := strings.ToLower(filepath.Base(exe))
		if base != "lantern" && base != "lantern.exe" {
			continue
		}
		for _, f := range fields[1:] {
			if f == "daemon" {
				return true
			}
		}
	}
	return false
}

// splitUnitArgs splits one ExecStart value the way systemd does for the simple
// case that matters here: whitespace separates arguments, single or double
// quotes group a path that contains spaces, and a backslash escapes the next
// character. It is not a full implementation of systemd's escaping; it only
// has to be good enough to recognise a unit.
func splitUnitArgs(s string) []string {
	var args []string
	var cur strings.Builder
	inArg := false
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
				continue
			}
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			quote = c
			inArg = true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			inArg = true
		case c == ' ' || c == '\t':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteByte(c)
			inArg = true
		}
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args
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
