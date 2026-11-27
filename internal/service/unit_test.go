package service

import (
	"strings"
	"testing"
)

// The generated unit is the whole contract with the operating system. These
// tests pin the parts a person would notice if they were wrong: the daemon is
// the command, a crash is restarted, and the agent runs at login.

func TestSystemdUnitRunsTheDaemonAndRestartsOnFailure(t *testing.T) {
	unit := systemdUnit("/opt/Lantern Bin/lantern")
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
	plist := launchdPlist("/Applications/Lantern & Co/lantern", "/Users/me")
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

func TestWindowsRunValueQuotesTheExecutable(t *testing.T) {
	got := windowsRunValue(`C:\Program Files\Lantern\lantern.exe`)
	want := `"C:\Program Files\Lantern\lantern.exe" daemon`
	if got != want {
		t.Fatalf("windowsRunValue = %q, want %q", got, want)
	}
}
