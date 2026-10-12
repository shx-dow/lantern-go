package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shx-dow/lantern-go/internal/daemon"
	"github.com/shx-dow/lantern-go/internal/doctor"
)

func TestSubcommand(t *testing.T) {
	owned := []string{"daemon", "mcp", "version"}
	for _, name := range owned {
		if got := subcommand([]string{name}); got != name {
			t.Errorf("subcommand(%q) = %q, want %q", name, got, name)
		}
		if got := subcommand([]string{name, "--flag", "value"}); got != name {
			t.Errorf("subcommand(%q, --flag value) = %q, want %q", name, got, name)
		}
	}
}

func TestSubcommandIgnoresCLIAgainstNoSubcommand(t *testing.T) {
	cases := [][]string{
		nil,
		{},
		{"send", "file.txt"},
		{"--daemon", "send", "file.txt"},
		{"--json", "receive", "code"},
		{"trust", "list"},
		// A flag whose *value* happens to be spelled like a subcommand must
		// not be mistaken for one, since flags are handled by parseArgs.
		{"--daemon-token", "daemon", "send", "file.txt"},
	}
	for _, args := range cases {
		if got := subcommand(args); got != "" {
			t.Errorf("subcommand(%q) = %q, want \"\" (belongs to the CLI)", args, got)
		}
	}
}

// The embedded UI must survive the move to a single binary: it is the only
// copy now that the Wails shell is gone.
func TestUIIsEmbedded(t *testing.T) {
	page, contentType := daemon.UI()
	if len(page) == 0 {
		t.Fatal("embedded UI page is empty")
	}
	if contentType == "" {
		t.Fatal("embedded UI page has no content type")
	}
}

func TestHelpRoutesToSubcommandHelp(t *testing.T) {
	// Every command must have help that actually describes it. These are
	// checked for content rather than exact text, so help stays editable.
	if !strings.Contains(usageFor("daemon"), "--allow-writes") {
		t.Error("daemon help should document the write opt-in")
	}
	if usageFor("daemon") == "" {
		t.Error("lantern help daemon produced nothing")
	}
	if !strings.Contains(usageFor("mcp"), "LANTERND_URL") {
		t.Error("mcp help should document the environment it reads")
	}
	if !strings.Contains(usageFor("doctor"), "--probe-peers") {
		t.Error("doctor help should document the reachability probe")
	}
	if usageFor("doctor") == "" {
		t.Error("lantern help doctor produced nothing")
	}
}

// doctor is a subcommand so `lantern doctor` works without --daemon, and it
// still has to accept the CLI flags that point it at a daemon.
func TestDoctorIsASubcommandAndParsesFlags(t *testing.T) {
	if got := subcommand([]string{"doctor"}); got != "doctor" {
		t.Fatalf("subcommand([doctor]) = %q", got)
	}
	opts, err := parseArgs([]string{"--daemon-url", ":43792", "--probe-peers", "doctor"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.command != "doctor" || !opts.probePeers {
		t.Fatalf("flags lost: %+v", opts)
	}
	if opts.daemonURL != "http://127.0.0.1:43792" {
		t.Fatalf("daemon URL = %q", opts.daemonURL)
	}
}

// Every mark must be fixed-width so the report stays aligned, whatever the
// check status.
func TestDoctorMarksAreAligned(t *testing.T) {
	want := len(doctorMark(doctor.Pass))
	for _, s := range []doctor.Status{doctor.Warn, doctor.Fail, doctor.Skip} {
		if got := len(doctorMark(s)); got != want {
			t.Errorf("doctorMark(%q) is %d wide, want %d", s, got, want)
		}
	}
}

// A report that failed must render as such and exit non-zero without printing
// a second "error:" line.
func TestDoctorReportRendersAndSignalsFailure(t *testing.T) {
	report := doctor.Report{
		Healthy: false,
		Checks: []doctor.Check{
			{Name: "daemon", Status: doctor.Fail, Detail: "no daemon answering at 127.0.0.1:43782", Fix: "lantern daemon"},
			{Name: "firewall", Status: doctor.Skip, Detail: "not applicable on linux"},
		},
	}
	var buf bytes.Buffer
	renderDoctorReport(report, false, &buf)
	out := buf.String()
	if !strings.Contains(out, "FAIL daemon") || !strings.Contains(out, "fix: lantern daemon") {
		t.Fatalf("render missing failure or fix:\n%s", out)
	}
	if !strings.Contains(out, "skip firewall") {
		t.Fatalf("render missing skipped check:\n%s", out)
	}
	if !report.Failed() {
		t.Error("report with a failed check must report Failed()")
	}
}

func TestDoctorReportJSONIsParseable(t *testing.T) {
	report := doctor.Report{Healthy: true, Checks: []doctor.Check{
		{Name: "daemon", Status: doctor.Pass, Detail: "reachable"},
	}}
	var buf bytes.Buffer
	renderDoctorReport(report, true, &buf)
	var decoded doctor.Report
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("doctor --json must emit valid JSON: %v (%s)", err, buf.String())
	}
	if !decoded.Healthy || len(decoded.Checks) != 1 || decoded.Checks[0].Status != doctor.Pass {
		t.Fatalf("round-trip lost data: %+v", decoded)
	}
}

func TestFirstEnvSkipsBlanks(t *testing.T) {
	if got := firstEnv("", "  ", "tok"); got != "tok" {
		t.Fatalf("firstEnv = %q", got)
	}
	if got := firstEnv("", "  "); got != "" {
		t.Fatalf("firstEnv = %q, want empty", got)
	}
}

// An explicitly empty --daemon-url= must not discard the environment. The
// parser seeds the URL from $LANTERND_URL and falls back to the built-in
// default, so "--daemon-url=" with nothing after the "=" should mean "I did not
// override it" rather than "use the hardcoded default".
//
// The bug was visible: --daemon= carried an empty-value guard and --daemon-url=
// did not, so the two spellings of the same idea behaved differently.
func TestEmptyDaemonURLFlagKeepsEnvironment(t *testing.T) {
	const fromEnv = "http://127.0.0.1:59999"
	for _, flag := range []string{"--daemon-url=", "--daemon-addr="} {
		t.Run(flag, func(t *testing.T) {
			t.Setenv("LANTERND_URL", fromEnv)

			opts, err := parseArgs([]string{flag})
			if err != nil {
				t.Fatalf("%s: %v", flag, err)
			}
			if opts.daemonURL != fromEnv {
				t.Errorf("%s discarded $LANTERND_URL: got %q, want %q", flag, opts.daemonURL, fromEnv)
			}
			if !opts.daemon {
				t.Errorf("%s should still mark the CLI as talking to a daemon", flag)
			}
		})
	}
}

// With no environment to preserve, an empty value falls back to the built-in
// default rather than becoming an empty string that would produce a broken URL.
func TestEmptyDaemonURLFlagFallsBackToDefault(t *testing.T) {
	t.Setenv("LANTERND_URL", "")

	opts, err := parseArgs([]string{"--daemon-url="})
	if err != nil {
		t.Fatal(err)
	}
	if opts.daemonURL != defaultDaemonURL {
		t.Errorf("got %q, want the built-in default %q", opts.daemonURL, defaultDaemonURL)
	}
}

// A non-empty value still overrides the environment, in both spellings.
func TestNonEmptyDaemonURLFlagOverridesEnvironment(t *testing.T) {
	t.Setenv("LANTERND_URL", "http://127.0.0.1:59999")

	for _, args := range [][]string{
		{"--daemon-url=http://10.0.0.5:1234"},
		{"--daemon-url", "http://10.0.0.5:1234"},
		{"--daemon-addr=http://10.0.0.5:1234"},
	} {
		opts, err := parseArgs(args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if opts.daemonURL != "http://10.0.0.5:1234" {
			t.Errorf("%v: got %q, want http://10.0.0.5:1234", args, opts.daemonURL)
		}
	}
}

// --roots is an alias for --writable-roots. It worked as a separate word and
// not with "=", so the two spellings were not interchangeable — which is the
// kind of asymmetry that only shows up as "unknown flag" in someone else's
// script.
func TestRootsAliasAcceptsBothForms(t *testing.T) {
	for _, args := range [][]string{
		{"--roots", "/srv/inbox,/tmp/out"},
		{"--roots=/srv/inbox,/tmp/out"},
		{"--writable-roots", "/srv/inbox,/tmp/out"},
		{"--writable-roots=/srv/inbox,/tmp/out"},
	} {
		opts, err := parseArgs(args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		want := []string{"/srv/inbox", "/tmp/out"}
		if len(opts.writableRoots) != len(want) {
			t.Fatalf("%v: got %v, want %v", args, opts.writableRoots, want)
		}
		for i, w := range want {
			if opts.writableRoots[i] != w {
				t.Errorf("%v: got %v, want %v", args, opts.writableRoots, want)
				break
			}
		}
	}
}

// Every daemon-backed command must be recognised as one. Without --daemon the
// CLI said `unknown command "status"` and then printed usage listing status
// immediately below it — telling the user the command did not exist while
// showing them that it does.
// The commands runDaemonCommand serves, other than the two that also run
// locally. These go to the daemon whether or not --daemon was passed.
func TestDaemonCommandsAreRecognisedWithoutTheFlag(t *testing.T) {
	daemonOnly := []string{"status", "discover", "peers", "list", "trust", "files", "remote-files"}
	for _, cmd := range daemonOnly {
		if !commandNeedsDaemon(cmd) {
			t.Errorf("%q is served by the daemon but is not recognised as one", cmd)
		}
	}
	// send and receive run either way — --daemon chooses between lanternd and a
	// fresh in-process node — so they must not be forced onto the daemon.
	for _, cmd := range []string{"send", "receive", "version", "daemon", "doctor", "mcp"} {
		if commandNeedsDaemon(cmd) {
			t.Errorf("%q also runs locally but is forced onto the daemon", cmd)
		}
	}
}
