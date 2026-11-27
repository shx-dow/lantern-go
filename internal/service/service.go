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

// Options are the operator's choices for one installation. The registration is
// the only place they are stored, so changing one means installing again.
type Options struct {
	// DaemonArgs are extra arguments after "daemon" in the registration. They
	// are the daemon's own flags — --allow-writes, --writable-dirs, --peer,
	// and so on — so the service runs with the choices the operator made
	// instead of the bare defaults. The bearer token is refused: the unit,
	// plist, and registry value are readable by every local user.
	DaemonArgs []string
}

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
	// LogTail is the service manager's recent output when installation
	// succeeded but the daemon did not come up. Empty when nothing was
	// captured; it exists so a failure like "another daemon holds the
	// identity directory" is reported instead of guessed at.
	LogTail string `json:"log_tail,omitempty"`
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

// validateArgs refuses an argument that would write the bearer token into the
// service file. That file is mode 0644 (or a registry value) and every local
// user can read it, while the token is what protects the loopback API even
// from another account on the same machine.
func validateArgs(args []string) error {
	for _, a := range args {
		name := a
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		if name == "--token" || name == "-token" {
			return errors.New("refusing to put the bearer token in the service file: every local user can read it; leave the token in the data directory")
		}
	}
	return nil
}

// registration is another service entry on this machine that runs
// `lantern daemon`. Two of them compete for the same identity directory and
// the same listen port, and the second one to start loses.
type registration struct {
	// Path is the file or registry value that defines it, for the message.
	Path string
	// Remove is the command that unregisters it.
	Remove string
}

// conflictError explains what already owns the daemon role and how to hand it
// over. Installing over it would leave two registrations enabled, so the next
// login would start two daemons and one would fail.
func conflictError(regs []registration) error {
	var b strings.Builder
	b.WriteString("another registration already runs the Lantern daemon:")
	for _, r := range regs {
		b.WriteString("\n  " + r.Path)
	}
	b.WriteString("\nremove it before installing, or the two will compete for the identity directory:")
	for _, r := range regs {
		if r.Remove != "" {
			b.WriteString("\n  " + r.Remove)
		}
	}
	return errors.New(b.String())
}

// logTail keeps the last few lines of captured output, for the message an
// install failure deserves. An empty or unreadable source yields "".
func logTail(text string, lines int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	parts := strings.Split(text, "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}
