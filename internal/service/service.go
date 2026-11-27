// Package service installs and inspects the per-user daemon service: a
// systemd user unit on Linux, a launchd user agent on macOS, and a per-user
// startup entry on Windows. See ADR 0007.
//
// The package never elevates. Everything it writes belongs to the user who
// ran it, because the identity key, the trust store, the token, and the shared
// roots all belong to that user.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Status describes the service registration and whether the daemon is running.
type Status struct {
	// Supported is false on a platform with no per-user mechanism, so callers
	// can skip rather than report a failure.
	Supported bool `json:"supported"`
	// Platform names the mechanism: systemd, launchd, or windows-startup.
	Platform string `json:"platform"`
	// UnitPath is the unit, plist, or registry value that defines the service.
	UnitPath string `json:"unit_path,omitempty"`
	// Installed reports whether that registration exists.
	Installed bool `json:"installed"`
	// Running reports whether the daemon is running under that registration.
	Running bool `json:"running"`
	// Linger reports whether a systemd user manager keeps running after
	// logout. It is nil on platforms where the question does not apply.
	Linger *bool `json:"linger,omitempty"`
	// Detail is one human sentence describing the state.
	Detail string `json:"detail,omitempty"`
	// Fix is the next command when the state is not the desired one.
	Fix string `json:"fix,omitempty"`
}

// commandRunner runs one command and returns its combined output. It is a
// variable so tests can describe a machine without a running service manager.
var commandRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// executablePath returns the absolute path of the binary that should run as
// the service. Symlinks are resolved so a service installed from a versioned
// path keeps pointing at the same file after the link is repointed.
func executablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find this binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", exe, err)
	}
	return abs, nil
}

// missingCommand turns exec's "not found" into a message that names the
// mechanism rather than the binary, so a systemd-less Linux host gets a
// diagnosis instead of "exec: systemctl: executable file not found".
func missingCommand(err error, name, fallback string) error {
	var execErr *exec.Error
	if errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound) {
		return fmt.Errorf("%s was not found; %s", name, fallback)
	}
	return err
}

// outputSuffix appends a command's output to an error, on one line, when the
// command said anything. An empty suffix keeps the error readable.
func outputSuffix(out []byte) string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return ""
	}
	return ": " + strings.Join(strings.Fields(s), " ")
}
