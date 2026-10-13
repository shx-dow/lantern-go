package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shx-dow/lantern-go/internal/doctor"
)

// doctor's value is that each line names the problem and the command that fixes
// it. The rendering is the whole of that, and none of it was reachable before
// this, because doctor had never been run from a test at all.

func TestDoctorReportsEachCheckAndItsFix(t *testing.T) {
	d := newFakeDaemon(t)
	var buf bytes.Buffer

	// A daemon that answers, so the report is about the checks rather than
	// about a refused connection.
	err := runDoctorFlags(t.Context(), cliOptions{
		daemonURL: d.URL, command: "doctor",
	}, &buf)

	out := buf.String()
	if out == "" {
		t.Fatal("doctor printed nothing")
	}
	// Which checks run depends on the platform: internal/paths branches on
	// GOOS, so a Windows runner does not produce the same set as a Linux one.
	// Asserting that all four marks appear held on Linux and failed on Windows.
	// What has to hold everywhere is that every line is a mark, a name and a
	// detail, and that the daemon is among the things checked.
	if !strings.Contains(out, "daemon") {
		t.Errorf("doctor did not check the daemon:\n%s", out)
	}
	// The renderer prints a blank line between the checks and the closing
	// summary, so only what comes before that blank line is a check row. The
	// summary is prose and would otherwise read as a malformed row.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			break
		}
		if strings.Contains(line, "fix:") {
			continue
		}
		if !strings.HasPrefix(line, "ok  ") && !strings.HasPrefix(line, "warn") &&
			!strings.HasPrefix(line, "FAIL") && !strings.HasPrefix(line, "skip") {
			t.Errorf("a line does not begin with a status mark: %q", line)
		}
	}
	// A healthy run says so rather than leaving the reader to infer it.
	if !strings.Contains(out, "no blocking problems found") && !strings.Contains(out, "some checks failed") {
		t.Errorf("doctor did not say whether anything failed:\n%s", out)
	}
	// A failing report is signalled by errSilent, which carries no text: the
	// report is already printed and a second message would be noise.
	if err != nil && err.Error() != "" {
		t.Errorf("doctor returned %q; the failure is meant to be silent", err)
	}
}

// A fix has to sit under the check that needs it, and a run has to say whether
// anything failed. Both are built from a known report rather than from
// whatever a fake daemon happens to produce, because the earlier version of
// these two tests passed against a report they did not control and so could
// not fail: a control that removed the healthy message still printed the
// failing one, and the assertion accepted either.
func TestRenderDoctorPairsFixesAndSaysWhatFailed(t *testing.T) {
	report := doctor.Report{
		Healthy: true,
		Checks: []doctor.Check{
			{Name: "daemon", Status: doctor.Pass, Detail: "answering on :43782"},
			{Name: "multicast", Status: doctor.Warn, Detail: "nothing heard", Fix: "lantern daemon --no-lan"},
			{Name: "laptop", Status: doctor.Pass, Detail: "reachable"},
		},
	}

	var buf bytes.Buffer
	renderDoctorReport(report, false, &buf)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")

	// Each fix sits directly under the check it belongs to.
	for i, l := range lines {
		if !strings.Contains(l, "fix:") {
			continue
		}
		if i == 0 || !strings.Contains(lines[i-1], "multicast") {
			t.Errorf("a fix is not directly under the check it belongs to:\n%s", buf.String())
		}
	}
	if !strings.Contains(buf.String(), "no blocking problems found") {
		t.Errorf("a healthy report did not say so:\n%s", buf.String())
	}

	// A report with a failure says that instead, and the two are different
	// sentences so a reader cannot mistake one for the other.
	failing := report
	failing.Healthy = false
	failing.Checks = append([]doctor.Check{
		{Name: "token", Status: doctor.Fail, Detail: "no bearer token", Fix: "export LANTERN_DAEMON_TOKEN=$(cat ~/.local/share/lantern/.lanternd-token)"},
	}, report.Checks...)
	buf.Reset()
	renderDoctorReport(failing, false, &buf)
	if !strings.Contains(buf.String(), "some checks failed") {
		t.Errorf("a failing report did not say so:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "no blocking problems found") {
		t.Errorf("a failing report also claimed health:\n%s", buf.String())
	}
}

// --json is the form an agent reads and a CI gate parses, so it has to be one
// JSON document with every check in it.
func TestDoctorJSONIsOneObjectWithEveryCheck(t *testing.T) {
	d := newFakeDaemon(t)
	var buf bytes.Buffer
	_ = runDoctorFlags(t.Context(), cliOptions{
		daemonURL: d.URL, command: "doctor", jsonOut: true,
	}, &buf)

	var report struct {
		Checks []struct {
			Status string `json:"status"`
			Name   string `json:"name"`
			Detail string `json:"detail"`
			Fix    string `json:"fix"`
		} `json:"checks"`
		Healthy bool `json:"healthy"`
	}
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("doctor --json is not JSON: %v\n%s", err, buf.String())
	}
	if len(report.Checks) == 0 {
		t.Fatal("doctor --json reported no checks")
	}
	for _, c := range report.Checks {
		if c.Name == "" {
			t.Errorf("a check came back with no name: %+v", c)
		}
		if c.Status == "" {
			t.Errorf("check %q came back with no status", c.Name)
		}
	}
}

// doctor's own flag set is separate from the CLI parser, so `lantern --daemon-url
// X doctor` and `lantern doctor --daemon-url X` have to agree. They are two
// routes into the same report and used to be tested by neither.
func TestDoctorResolvesItsDaemonTheSameWayEitherWay(t *testing.T) {
	d := newFakeDaemon(t)
	t.Setenv("LANTERND_URL", d.URL)

	var viaSub, viaFlag bytes.Buffer
	_ = runDoctorFlags(t.Context(), cliOptions{daemonURL: d.URL, command: "doctor", jsonOut: true}, &viaSub)
	err := runDoctor(t.Context(), []string{"--json"}, &viaFlag)
	if err != nil && err.Error() != "" {
		t.Fatalf("runDoctor: %v", err)
	}

	var a, b map[string]any
	if err := json.Unmarshal(viaSub.Bytes(), &a); err != nil {
		t.Fatalf("flag route is not JSON: %v", err)
	}
	if err := json.Unmarshal(viaFlag.Bytes(), &b); err != nil {
		t.Fatalf("env route is not JSON: %v", err)
	}
	if len(aChecks(a)) != len(aChecks(b)) {
		t.Errorf("the two routes reported %d and %d checks", len(aChecks(a)), len(aChecks(b)))
	}
}

// aChecks pulls the check list out of a decoded report, so the comparison
// above does not depend on the concrete report type.
func aChecks(m map[string]any) []any {
	list, _ := m["checks"].([]any)
	return list
}

// An unknown flag is a usage error, not a run with the flag ignored.
func TestDoctorRejectsAnUnknownFlag(t *testing.T) {
	var buf bytes.Buffer
	if err := runDoctor(t.Context(), []string{"--not-a-flag"}, &buf); err == nil {
		t.Fatal("an unknown doctor flag was accepted")
	}
}

// firstEnv picks the first non-blank value, which is how a flag beats the
// environment beats a default. A blank flag value has to fall through, or
// `--daemon-url ""` would point the CLI at nothing.
func TestFirstEnvSkipsBlanksAndKeepsOrder(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"", "b", "c"}, "b"},
		{[]string{"   ", "\t", "b"}, "b"},
		{[]string{"a", "b"}, "a"},
		{[]string{"", ""}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := firstEnv(c.in...); got != c.want {
			t.Errorf("firstEnv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The mark has to line up, because a person scans the left edge of the output.
// Compared by width rather than by name: what matters is that a renamed status
// does not shift the columns.
func TestDoctorMarkLinesUp(t *testing.T) {
	widths := map[doctor.Status]int{}
	for _, s := range []doctor.Status{doctor.Pass, doctor.Warn, doctor.Fail, doctor.Status("other")} {
		widths[s] = len(doctorMark(s))
	}
	for s, w := range widths {
		if s == doctor.Warn {
			continue // the warn mark is the one that is deliberately longer
		}
		if w != widths[doctor.Pass] {
			t.Errorf("mark for %q is %d wide, pass is %d; the columns will not line up", s, w, widths[doctor.Pass])
		}
	}
	if doctorMark(doctor.Status("other")) == "" {
		t.Error("an unknown status has no mark, so its row has no status column")
	}
}
