//go:build darwin

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

// plistPath is the per-user launch agent. launchd reads this directory for
// agents that run as the logged-in user, which is exactly the privilege the
// daemon needs.
func plistPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "lantern", launchdLabel+".plist")
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func logDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "lantern")
	}
	return filepath.Join(home, "Library", "Logs", "lantern")
}

// homeDir returns the current user's home directory for the plist's absolute
// log paths. launchd does not expand "~".
func homeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot find the home directory for a per-user service: %w", err)
	}
	if home == "" {
		return "", errors.New("cannot find the home directory for a per-user service")
	}
	return home, nil
}

// Install writes the launch agent with the operator's daemon arguments, boots
// out any previous copy, and bootstraps the new one so it runs now and at every
// login.
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
	home, err := homeDir()
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath()), 0o755); err != nil {
		return Status{}, fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	if err := os.MkdirAll(logDir(), 0o755); err != nil {
		return Status{}, fmt.Errorf("create log directory: %w", err)
	}
	if err := os.WriteFile(plistPath(), []byte(launchdPlist(exe, home, opts.DaemonArgs)), 0o644); err != nil {
		return Status{}, fmt.Errorf("write launch agent %s: %w", plistPath(), err)
	}

	uid := strconv.Itoa(os.Getuid())
	// A previous copy is booted out first so install is idempotent. A failure
	// here means nothing was loaded, which is the normal first install.
	_, _ = commandRunner(ctx, "launchctl", "bootout", "gui/"+uid+"/"+launchdLabel)
	if out, err := commandRunner(ctx, "launchctl", "bootstrap", "gui/"+uid, plistPath()); err != nil {
		return Status{}, fmt.Errorf("launchctl bootstrap: %w%s",
			missingCommand(err, "launchctl", "launchd is not available here"),
			outputSuffix(out))
	}
	st, err := Inspect(ctx)
	if err != nil {
		return st, err
	}
	if !st.Running {
		st.LogTail = plistLogTail()
	}
	return st, nil
}

// plistLogTail returns the last lines launchd captured on stderr, which is
// where the daemon's own reason for exiting lands when a start fails.
func plistLogTail() string {
	body, err := os.ReadFile(filepath.Join(logDir(), "lanternd.err.log"))
	if err != nil {
		return ""
	}
	const maxBytes = 8 << 10
	if len(body) > maxBytes {
		body = body[len(body)-maxBytes:]
	}
	return logTail(string(body), 15)
}

// otherRegistrations finds launch agents that already run a Lantern daemon.
// Two loaded agents compete for the identity directory and the port.
func otherRegistrations() []registration {
	home, _ := os.UserHomeDir()
	dirs := []string{"/Library/LaunchAgents"}
	if home != "" {
		dirs = append([]string{filepath.Join(home, "Library", "LaunchAgents")}, dirs...)
	}
	ours := plistPath()
	uid := strconv.Itoa(os.Getuid())
	seen := make(map[string]bool)
	var regs []registration
	for _, dir := range dirs {
		paths, _ := filepath.Glob(filepath.Join(dir, "*.plist"))
		for _, path := range paths {
			if path == ours || seen[path] {
				continue
			}
			seen[path] = true
			body, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if !plistRunsLanternDaemon(string(body)) {
				continue
			}
			label := plistLabel(string(body))
			if label == "" {
				label = strings.TrimSuffix(filepath.Base(path), ".plist")
			}
			regs = append(regs, registration{
				Path:   path,
				Remove: "launchctl bootout gui/" + uid + "/" + label + " && rm " + path,
			})
		}
	}
	return regs
}

// plistRunsLanternDaemon is a deliberately loose match: the file names a
// lantern binary somewhere and has a program argument that is exactly
// "daemon". Parsing the plist as XML would be stricter, but launchd accepts
// more than one shape and a false negative here means two daemons start.
func plistRunsLanternDaemon(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "lantern") && strings.Contains(body, "<string>daemon</string>")
}

// plistLabel reads the Label value, which is what launchctl addresses.
func plistLabel(body string) string {
	i := strings.Index(body, "<key>Label</key>")
	if i < 0 {
		return ""
	}
	after := body[i+len("<key>Label</key>"):]
	start := strings.Index(after, "<string>")
	if start < 0 {
		return ""
	}
	after = after[start+len("<string>"):]
	end := strings.Index(after, "</string>")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(after[:end])
}

// Uninstall boots the agent out and removes the plist. removed reports
// whether there was a plist to remove.
func Uninstall(ctx context.Context) (removed bool, err error) {
	if _, statErr := os.Stat(plistPath()); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read launch agent %s: %w", plistPath(), statErr)
	}
	uid := strconv.Itoa(os.Getuid())
	// Bootout first: removing the file alone would leave the running daemon
	// orphaned with no way to stop it through launchd.
	_, _ = commandRunner(ctx, "launchctl", "bootout", "gui/"+uid+"/"+launchdLabel)
	if err := os.Remove(plistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove launch agent %s: %w", plistPath(), err)
	}
	return true, nil
}

// Inspect reports whether the plist exists and whether launchd says the agent
// is running.
func Inspect(ctx context.Context) (Status, error) {
	st := Status{Supported: true, Platform: "launchd", UnitPath: plistPath()}
	if _, err := os.Stat(st.UnitPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			st.Detail = "no launch agent at " + st.UnitPath
			st.Fix = "lantern service install"
			return st, nil
		}
		return st, fmt.Errorf("read launch agent %s: %w", st.UnitPath, err)
	}
	st.Installed = true

	uid := strconv.Itoa(os.Getuid())
	out, err := commandRunner(ctx, "launchctl", "print", "gui/"+uid+"/"+launchdLabel)
	if err != nil {
		if missing := missingCommand(err, "launchctl", "launchd is not available here"); missing != err {
			return st, missing
		}
		st.Detail = "launch agent at " + st.UnitPath + " is installed but not loaded"
		st.Fix = "launchctl bootstrap gui/" + uid + " " + st.UnitPath
		return st, nil
	}

	st.Running = strings.Contains(string(out), "state = running")
	if st.Running {
		st.Detail = "launch agent at " + st.UnitPath + " is running"
	} else {
		st.Detail = "launch agent at " + st.UnitPath + " is installed but not running"
		st.Fix = "launchctl kickstart gui/" + uid + "/" + launchdLabel
	}
	return st, nil
}
