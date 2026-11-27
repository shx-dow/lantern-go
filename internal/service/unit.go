package service

import (
	"path"
	"strings"
)

// The three generators below are pure: they take the executable and, where
// the platform needs it, the home directory, and return the file or registry
// value to install. Keeping them separate from install means the shape of
// each unit can be tested on any platform.

// launchdLabel is the reverse-DNS name launchd uses for this agent. It is
// shared because the plist generator is compiled on every platform.
const launchdLabel = "com.lantern.lanternd"

// systemdUnit returns a systemd user unit that runs the daemon with the given
// extra arguments. A user unit (not a system one) is deliberate: it runs as the
// user who owns the identity and the shared roots, and needs no elevation.
func systemdUnit(exe string, args []string) string {
	execStart := systemdQuote(exe) + " daemon"
	for _, a := range args {
		execStart += " " + systemdQuote(a)
	}
	return `[Unit]
Description=Lantern daemon

[Service]
Type=simple
ExecStart=` + execStart + `
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
}

// systemdQuote quotes one ExecStart argument for systemd. systemd expands
// "%" specifiers even inside quotes, so a literal percent is doubled; a
// backslash and a double quote are escaped so a path containing either still
// names one argument.
func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// launchdPlist returns the launch agent that runs the daemon at login and
// restarts it after a crash. KeepAlive is keyed on SuccessfulExit=false so a
// clean stop (launchctl bootout) is not immediately undone.
func launchdPlist(exe, home string, args []string) string {
	logDir := path.Join(home, "Library", "Logs", "lantern")
	var programArgs strings.Builder
	for _, a := range append([]string{exe, "daemon"}, args...) {
		programArgs.WriteString("    <string>" + xmlEscape(a) + "</string>\n")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + launchdLabel + `</string>
  <key>ProgramArguments</key>
  <array>
` + programArgs.String() + `  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>` + xmlEscape(path.Join(logDir, "lanternd.log")) + `</string>
  <key>StandardErrorPath</key>
  <string>` + xmlEscape(path.Join(logDir, "lanternd.err.log")) + `</string>
</dict>
</plist>
`
}

// windowsRunValue is the data for the per-user Run value: the executable,
// "daemon", and the operator's arguments, quoted by the same rules Windows
// uses to split a command line.
func windowsRunValue(exe string, args []string) string {
	return windowsCommandLine(append([]string{exe, "daemon"}, args...))
}

// windowsCommandLine joins arguments the way CreateProcess parses them:
// backslashes are literal except in the run before a quote, where each one is
// doubled, and a quote is escaped with a backslash. Quoting only when needed
// keeps the common path readable.
func windowsCommandLine(args []string) string {
	var b strings.Builder
	for i, arg := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		if arg != "" && !strings.ContainsAny(arg, " \t\"") {
			b.WriteString(arg)
			continue
		}
		b.WriteByte('"')
		slashes := 0
		for _, r := range arg {
			switch r {
			case '\\':
				slashes++
				continue
			case '"':
				b.WriteString(strings.Repeat(`\`, 2*slashes+1))
				b.WriteByte('"')
			default:
				b.WriteString(strings.Repeat(`\`, slashes))
				b.WriteRune(r)
			}
			slashes = 0
		}
		// Trailing backslashes would escape the closing quote.
		b.WriteString(strings.Repeat(`\`, 2*slashes))
		b.WriteByte('"')
	}
	return b.String()
}

// xmlEscape escapes the five characters that cannot appear literally in XML
// text. An executable path can contain "&" on every platform, so this is not
// theoretical.
func xmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}
