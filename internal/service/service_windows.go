//go:build windows

package service

import (
	"context"
	"fmt"
	"strings"
)

const (
	// runKey is the per-user Run key. HKCU means no elevation and no effect on
	// other users, which matches the daemon's per-user identity and roots.
	runKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	// runValue is the value name under that key.
	runValue = "Lantern"
)

// Install writes the per-user startup value with the operator's daemon
// arguments. It takes effect at the next sign-in; a daemon started by hand now
// is left alone.
func Install(ctx context.Context, opts Options) (Status, error) {
	if err := validateArgs(opts.DaemonArgs); err != nil {
		return Status{}, err
	}
	if regs := otherRegistrations(ctx); len(regs) > 0 {
		return Status{}, conflictError(regs)
	}
	exe, err := executablePath()
	if err != nil {
		return Status{}, err
	}
	out, err := commandRunner(ctx, "reg", "add", runKey, "/v", runValue, "/t", "REG_SZ", "/d", windowsRunValue(exe, opts.DaemonArgs), "/f")
	if err != nil {
		return Status{}, fmt.Errorf("reg add %s: %w%s",
			runKey, missingCommand(err, "reg", "the Windows registry is not available here"), outputSuffix(out))
	}
	return Inspect(ctx)
}

// otherRegistrations finds other Run values that start a Lantern daemon. Two
// entries would start two daemons at the next sign-in, and the second would
// fail on the identity directory.
func otherRegistrations(ctx context.Context) []registration {
	out, err := commandRunner(ctx, "reg", "query", runKey)
	if err != nil {
		return nil
	}
	var regs []registration
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.EqualFold(fields[1], "REG_SZ") {
			continue
		}
		name := fields[0]
		if name == runValue {
			continue
		}
		lower := strings.ToLower(strings.Join(fields[2:], " "))
		if !strings.Contains(lower, "lantern") || !strings.Contains(lower, "daemon") {
			continue
		}
		regs = append(regs, registration{
			Path:   runKey + `\` + name,
			Remove: "reg delete " + runKey + " /v " + name + " /f",
		})
	}
	return regs
}

// Uninstall removes the startup value. It does not stop a daemon the user
// started by hand; the registration is all this command owns. removed reports
// whether the value was there.
func Uninstall(ctx context.Context) (removed bool, err error) {
	if _, err := commandRunner(ctx, "reg", "query", runKey, "/v", runValue); err != nil {
		// No value is the idempotent case: there is nothing to remove.
		return false, nil
	}
	out, err := commandRunner(ctx, "reg", "delete", runKey, "/v", runValue, "/f")
	if err != nil {
		return false, fmt.Errorf("reg delete %s: %w%s", runKey, err, outputSuffix(out))
	}
	return true, nil
}

// Inspect reports whether the Run value exists and whether a lantern.exe is
// running. tasklist cannot say which command line a process was started with,
// so a hand-started daemon counts as running; the registration is what this
// check is about.
func Inspect(ctx context.Context) (Status, error) {
	st := Status{Supported: true, Platform: "windows-startup", UnitPath: runKey + `\` + runValue}
	if out, err := commandRunner(ctx, "reg", "query", runKey, "/v", runValue); err != nil {
		if missing := missingCommand(err, "reg", "the Windows registry is not available here"); missing != err {
			return st, missing
		}
		// reg query exits non-zero when the value is absent.
		st.Detail = "no per-user startup entry for Lantern"
		st.Fix = "lantern service install"
		return st, nil
	} else if !strings.Contains(string(out), runValue) {
		st.Detail = "no per-user startup entry for Lantern"
		st.Fix = "lantern service install"
		return st, nil
	}
	st.Installed = true

	if out, err := commandRunner(ctx, "tasklist", "/FI", "IMAGENAME eq lantern.exe", "/NH"); err == nil {
		st.Running = strings.Contains(strings.ToLower(string(out)), "lantern.exe")
	}
	if st.Running {
		st.Detail = "per-user startup entry at " + st.UnitPath + " is installed and lantern.exe is running"
	} else {
		st.Detail = "per-user startup entry at " + st.UnitPath + " is installed; it starts at the next sign-in"
		st.Fix = "lantern daemon"
	}
	return st, nil
}
