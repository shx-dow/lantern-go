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
