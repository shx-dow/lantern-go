package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/shx-dow/lantern-go/internal/service"
)

// serviceUsage is the help text for `lantern service`, also shown by
// `lantern help service`.
const serviceUsage = `lantern service <install|uninstall|status>

Installs the daemon as a per-user service, so it starts at login and keeps
running after the terminal closes. It runs as you, with your identity and
shared roots, and needs no administrator rights.

Commands:
  install [flags]  write the platform service file and start the daemon now
                   (on Windows the entry takes effect at the next sign-in)
  uninstall        stop the daemon and remove the service file
  status           print whether the service is installed and running

install flags are written into the service and passed to "lantern daemon", so
the always-on daemon runs with the same choices as a foreground one:
  --allow-writes           let paired devices write into the shared roots
  --writable-dirs DIRS     comma-separated directories they may write to
  --p2p-port N             fixed libp2p port (0 = random, default from config)
  --peer ADDRS             comma-separated full peer multiaddrs
  --data-dir DIR           state directory for identity, pairings, and token
  --addr ADDR              loopback HTTP listen address
  --config FILE            daemon config file
  --allow-remote           bind the HTTP API beyond localhost
Changing one means running install again; the registration is the only place
they are stored. The bearer token cannot be installed: the service file is
readable by every local user, so the token stays in the data directory.

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
	if len(args) == 0 {
		return fmt.Errorf("usage: lantern service install|uninstall|status")
	}
	switch args[0] {
	case "install":
		return runServiceInstall(ctx, args[1:], out)
	case "uninstall":
		if len(args) != 1 {
			return fmt.Errorf("usage: lantern service uninstall")
		}
		return runServiceUninstall(ctx, out)
	case "status":
		if len(args) != 1 {
			return fmt.Errorf("usage: lantern service status")
		}
		return runServiceStatus(ctx, out)
	case "help", "-h", "--help":
		fmt.Fprint(out, serviceUsage)
		return nil
	default:
		return fmt.Errorf("unknown service subcommand %q\n  usage: lantern service install|uninstall|status", args[0])
	}
}

func runServiceInstall(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	// The caller prints serviceUsage on a help request and report the parse
	// error itself, so the flag package must not write.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	allowWrites := fs.Bool("allow-writes", false, "let paired devices write into the shared roots")
	writableDirs := fs.String("writable-dirs", "", "comma-separated directories a paired device may write to")
	p2pPort := fs.Int("p2p-port", -1, "fixed libp2p port (0 = random)")
	peers := fs.String("peer", "", "comma-separated full peer multiaddrs")
	dataDir := fs.String("data-dir", "", "state directory for identity, pairings, and token")
	addr := fs.String("addr", "", "loopback HTTP listen address")
	configPath := fs.String("config", "", "daemon config file")
	allowRemote := fs.Bool("allow-remote", false, "bind the HTTP API beyond localhost")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(out, serviceUsage)
			return nil
		}
		return fmt.Errorf("%v\n  usage: lantern service install [flags]; \"lantern help service\" lists them", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q\n  usage: lantern service install [flags]", fs.Arg(0))
	}
	if *p2pPort < -1 || *p2pPort > 65535 {
		return fmt.Errorf("--p2p-port %d is not a port (0-65535, or leave it unset)", *p2pPort)
	}

	st, err := service.Install(ctx, service.Options{DaemonArgs: serviceDaemonArgs(
		*allowWrites, *writableDirs, *p2pPort, *peers, *dataDir, *addr, *configPath, *allowRemote,
	)})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "installed the Lantern service at %s\n", st.UnitPath)
	if st.LogTail != "" {
		// The manager accepted the start and the daemon exited anyway. Its
		// own words are the only useful diagnosis: a second daemon holding
		// the identity directory and a busy port look identical otherwise.
		fmt.Fprintf(out, "  the daemon did not stay up; recent output:\n%s\n", indent(st.LogTail, "    "))
		return errSilent
	}
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

// serviceDaemonArgs turns the install flags into the daemon's own arguments.
// An unset flag is omitted rather than written as its default, so the daemon
// keeps reading lanternd.json for values the operator did not choose here.
func serviceDaemonArgs(allowWrites bool, writableDirs string, p2pPort int, peers, dataDir, addr, configPath string, allowRemote bool) []string {
	var args []string
	if allowWrites {
		args = append(args, "--allow-writes")
	}
	add := func(flag, value string) {
		if strings.TrimSpace(value) != "" {
			args = append(args, flag+"="+value)
		}
	}
	add("--writable-dirs", writableDirs)
	if p2pPort >= 0 {
		args = append(args, "--p2p-port="+strconv.Itoa(p2pPort))
	}
	add("--peer", peers)
	add("--data-dir", dataDir)
	add("--addr", addr)
	add("--config", configPath)
	if allowRemote {
		args = append(args, "--allow-remote")
	}
	return args
}

// indent prefixes every line, so captured output reads as a quotation rather
// than as the command's own words.
func indent(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
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
