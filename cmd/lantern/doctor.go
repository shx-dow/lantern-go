package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shx-dow/lantern-go/internal/doctor"
	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// doctorUsage is the help text for `lantern doctor`, also shown by
// `lantern help doctor`.
const doctorUsage = `lantern doctor [flags]

Checks a Lantern setup and prints what is wrong plus the command that fixes
it. Every check is read-only: nothing is paired, dialed for real, or written.

The failures this exists for are silent ones. A dismissed Windows Firewall
prompt, a network that drops mDNS (WSL2 NAT, Docker, corporate Wi-Fi), and a
device that is simply switched off all look identical from the outside.

Flags:
  --probe-peers      dial each paired device to test reachability, rather than
                     reporting only whether a connection happens to be open
  --daemon-url URL   daemon base URL (same as $LANTERND_URL)
  --daemon-token T   daemon bearer token (same as $LANTERN_DAEMON_TOKEN)
  --json             machine-readable JSON on stdout

Point it at a daemon with the same environment the CLI and MCP shim use:
  LANTERND_URL           daemon base URL (default http://127.0.0.1:43782)
  LANTERN_DAEMON_TOKEN   daemon bearer token (or $LANTERND_TOKEN)

Examples:
  lantern doctor
  export LANTERND_URL=http://127.0.0.1:43792
  export LANTERN_DAEMON_TOKEN=$(cat ~/.local/share/lantern/.lanternd-token)
  lantern doctor --probe-peers
  lantern doctor --daemon-url http://127.0.0.1:43792
`

// runDoctor handles `lantern doctor` with its own flag set, so doctor flags
// are parsed by Go's flag package rather than the CLI's positional parser.
func runDoctor(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, doctorUsage) }
	probe := fs.Bool("probe-peers", false, "dial each paired device to test reachability")
	jsonOut := fs.Bool("json", false, "machine-readable JSON on stdout")
	base := fs.String("daemon-url", "", "daemon base URL (same as $LANTERND_URL)")
	tokenFlag := fs.String("daemon-token", "", "daemon bearer token (same as $LANTERN_DAEMON_TOKEN)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Flags win over the environment, which wins over the default, so doctor
	// resolves its target exactly the way the rest of the CLI does.
	addr := firstEnv(*base, os.Getenv("LANTERND_URL"), defaultDaemonURL)
	token := firstEnv(*tokenFlag, os.Getenv("LANTERN_DAEMON_TOKEN"), os.Getenv("LANTERND_TOKEN"))
	return runDoctorReport(ctx, lanternclient.New(normalizeBaseURL(addr), token), doctor.Options{
		Token:      token,
		ProbePeers: *probe,
	}, *jsonOut, out)
}

// runDoctorFlags serves doctor when it arrived with CLI flags parsed, so
// `lantern --daemon-url :43792 doctor` works the same as the env vars.
func runDoctorFlags(ctx context.Context, opts cliOptions, out io.Writer) error {
	report := doctor.Run(ctx, newDaemonClient(opts.daemonURL, opts.daemonToken), doctor.Options{
		Token:      opts.daemonToken,
		ProbePeers: opts.probePeers,
	})
	renderDoctorReport(report, opts.jsonOut, out)
	if report.Failed() {
		return errSilent
	}
	return nil
}

func runDoctorReport(ctx context.Context, client *lanternclient.Client, opts doctor.Options, jsonOut bool, out io.Writer) error {
	report := doctor.Run(ctx, client, opts)
	renderDoctorReport(report, jsonOut, out)
	if report.Failed() {
		return errSilent
	}
	return nil
}

// renderDoctorReport prints the report. Human output puts the fix under the
// check that needs it, since that pairing is what makes the command useful;
// JSON keeps one flat object per check for agents.
func renderDoctorReport(report doctor.Report, jsonOut bool, out io.Writer) {
	if jsonOut {
		enc := json.NewEncoder(out)
		_ = enc.Encode(report)
		return
	}
	for _, c := range report.Checks {
		fmt.Fprintf(out, "%s %-18s %s\n", doctorMark(c.Status), c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(out, "  %-20s fix: %s\n", "", c.Fix)
		}
	}
	fmt.Fprintln(out)
	if report.Healthy {
		fmt.Fprintln(out, "no blocking problems found; warnings above are things Lantern works around")
		return
	}
	fmt.Fprintln(out, "some checks failed; each fix above is the next command to run")
}

func doctorMark(s doctor.Status) string {
	switch s {
	case doctor.Pass:
		return "ok  "
	case doctor.Warn:
		return "warn"
	case doctor.Fail:
		return "FAIL"
	default:
		return "skip"
	}
}

// errSilent means "already reported to the user"; the caller exits non-zero
// without printing a second message.
var errSilent = fmt.Errorf("")

func firstEnv(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
