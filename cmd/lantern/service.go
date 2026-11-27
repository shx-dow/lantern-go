package main

import (
	"context"
	"fmt"
	"io"

	"github.com/shx-dow/lantern-go/internal/service"
)

// serviceUsage is the help text for `lantern service`, also shown by
// `lantern help service`.
const serviceUsage = `lantern service <install|uninstall|status>

Installs the daemon as a per-user service, so it starts at login and keeps
running after the terminal closes. It runs as you, with your identity and
shared roots, and needs no administrator rights.

Commands:
  install     write the platform service file and start the daemon now
              (on Windows the entry takes effect at the next sign-in)
  uninstall   stop the daemon and remove the service file
  status      print whether the service is installed and running

Platforms:
  Linux    systemd user unit ~/.config/systemd/user/lanternd.service
  macOS    launchd user agent ~/Library/LaunchAgents/com.lantern.lanternd.plist
  Windows  per-user startup entry under HKCU\Software\Microsoft\Windows\
           CurrentVersion\Run

On Linux, a systemd user service stops when you log out unless lingering is
enabled. "lantern doctor" warns when it is off; the fix is:

  loginctl enable-linger "$USER"

There is no start or restart subcommand: after install, the platform's service
manager owns the process lifetime. "lantern daemon" still runs a foreground
daemon for debugging.
`

// runService handles `lantern service install|uninstall|status`. It is local
// to this machine and does not talk to the daemon API, so it works while the
// daemon is stopped.
func runService(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: lantern service install|uninstall|status")
	}
	switch args[0] {
	case "install":
		return runServiceInstall(ctx, out)
	case "uninstall":
		return runServiceUninstall(ctx, out)
	case "status":
		return runServiceStatus(ctx, out)
	default:
		return fmt.Errorf("unknown service subcommand %q\n  usage: lantern service install|uninstall|status", args[0])
	}
}

func runServiceInstall(ctx context.Context, out io.Writer) error {
	st, err := service.Install(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "installed the Lantern service at %s\n", st.UnitPath)
	if st.Running {
		fmt.Fprintln(out, "  the daemon is running")
		return nil
	}
	fmt.Fprintln(out, "  the daemon is not running yet")
	if st.Fix != "" {
		fmt.Fprintf(out, "  fix: %s\n", st.Fix)
	}
	return nil
}

func runServiceUninstall(ctx context.Context, out io.Writer) error {
	removed, err := service.Uninstall(ctx)
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintln(out, "no Lantern service is installed")
		return nil
	}
	fmt.Fprintln(out, "removed the Lantern service")
	return nil
}

func runServiceStatus(ctx context.Context, out io.Writer) error {
	st, err := service.Inspect(ctx)
	if err != nil {
		return err
	}
	if !st.Supported {
		fmt.Fprintf(out, "%s: no per-user service mechanism; run \"lantern daemon\" under your init system\n", st.Platform)
		return errSilent
	}
	fmt.Fprintln(out, st.Detail)
	if st.Fix != "" {
		fmt.Fprintf(out, "  fix: %s\n", st.Fix)
	}
	if !st.Running {
		// A status command that exits zero while the service is stopped is
		// useless in a script, so this is a silent non-zero exit after the
		// diagnosis has been printed.
		return errSilent
	}
	return nil
}
