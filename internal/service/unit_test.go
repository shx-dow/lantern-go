package service

import (
	"strings"
	"testing"
)

// The generated unit is the whole contract with the operating system. These
// tests pin the parts a person would notice if they were wrong: the daemon is
// the command, the operator's flags survive, a crash is restarted, and the
// agent runs at login.

func TestSystemdUnitRunsTheDaemonAndRestartsOnFailure(t *testing.T) {
	unit := systemdUnit("/opt/Lantern Bin/lantern", nil)
	for _, want := range []string{
		`ExecStart="/opt/Lantern Bin/lantern" daemon`,
		"Restart=on-failure",
		"RestartSec=5",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit is missing %q:\n%s", want, unit)
		}
	}
}

// An install flag has to reach the daemon through the unit, or the always-on
// daemon quietly runs with different choices than a foreground one.
func TestSystemdUnitCarriesDaemonArgs(t *testing.T) {
	unit := systemdUnit("/opt/lantern", []string{
		"--allow-writes",
		"--writable-dirs=/home/me/In box",
	})
	want := `ExecStart="/opt/lantern" daemon "--allow-writes" "--writable-dirs=/home/me/In box"`
	if !strings.Contains(unit, want) {
		t.Fatalf("unit does not carry the daemon arguments:\n%s", unit)
	}
}

// systemd expands % specifiers inside ExecStart, so a literal percent in a
// path has to be doubled or the unit names a different file.
func TestSystemdQuoteEscapesPercentAndQuotes(t *testing.T) {
	got := systemdQuote(`/opt/100%/lantern"x`)
	want := `"/opt/100%%/lantern\"x"`
	if got != want {
		t.Fatalf("systemdQuote = %s, want %s", got, want)
	}
}

func TestLaunchdPlistRunsAtLoadAndKeepsOnlyCrashesAlive(t *testing.T) {
	plist := launchdPlist("/Applications/Lantern & Co/lantern", "/Users/me", nil)
	for _, want := range []string{
		"<string>" + launchdLabel + "</string>",
		"<string>/Applications/Lantern &amp; Co/lantern</string>",
		"<string>daemon</string>",
		"<key>RunAtLoad</key>",
		"<key>SuccessfulExit</key>",
		"<false/>",
		"<string>/Users/me/Library/Logs/lantern/lanternd.log</string>",
		"<string>/Users/me/Library/Logs/lantern/lanternd.err.log</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist is missing %q:\n%s", want, plist)
		}
	}
}

// Each argument is its own ProgramArgument. Joining them into one string
// would make launchd pass a single argument the daemon cannot parse.
func TestLaunchdPlistKeepsEachDaemonArgSeparate(t *testing.T) {
	plist := launchdPlist("/Applications/lantern", "/Users/me", []string{
		"--allow-writes",
		"--writable-dirs=/Users/me/In box",
	})
	for _, want := range []string{
		"<string>--allow-writes</string>",
		"<string>--writable-dirs=/Users/me/In box</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist is missing %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "<string>--allow-writes --writable-dirs") {
		t.Error("the daemon arguments were joined into one ProgramArgument")
	}
}

func TestWindowsRunValueQuotesTheExecutable(t *testing.T) {
	got := windowsRunValue(`C:\Program Files\Lantern\lantern.exe`, nil)
	want := `"C:\Program Files\Lantern\lantern.exe" daemon`
	if got != want {
		t.Fatalf("windowsRunValue = %q, want %q", got, want)
	}
}

func TestWindowsRunValueCarriesDaemonArgs(t *testing.T) {
	got := windowsRunValue(`C:\Lantern\lantern.exe`, []string{
		"--allow-writes",
		`--writable-dirs=C:\My Files`,
	})
	want := `C:\Lantern\lantern.exe daemon --allow-writes "--writable-dirs=C:\My Files"`
	if got != want {
		t.Fatalf("windowsRunValue = %q, want %q", got, want)
	}
}

// CreateProcess reads a backslash before a quote as an escape, so it has to be
// doubled, and a trailing backslash before the closing quote too. Getting this
// wrong truncates the path a directory argument names.
func TestWindowsCommandLineFollowsCreateProcessQuoting(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"plain", []string{"a", "b"}, `a b`},
		{"spaces", []string{"a b"}, `"a b"`},
		{"empty", []string{""}, `""`},
		{"quote", []string{`a"b`}, `"a\"b"`},
		{"backslash before quote", []string{`a\"b`}, `"a\\\"b"`},
		{"trailing backslash", []string{`C:\my dir\`}, `"C:\my dir\\"`},
	}
	for _, c := range cases {
		if got := windowsCommandLine(c.args); got != c.want {
			t.Errorf("%s: windowsCommandLine(%q) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}
