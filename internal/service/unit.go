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

// systemdUnit returns a systemd user unit that runs the daemon. A user unit
// (not a system one) is deliberate: it runs as the user who owns the identity
// and the shared roots, and needs no elevation.
func systemdUnit(exe string) string {
	return `[Unit]
Description=Lantern daemon

[Service]
Type=simple
ExecStart=` + systemdQuote(exe) + ` daemon
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
func launchdPlist(exe, home string) string {
	logDir := path.Join(home, "Library", "Logs", "lantern")
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + launchdLabel + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + xmlEscape(exe) + `</string>
    <string>daemon</string>
  </array>
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

// windowsRunValue is the data for the per-user Run value: the quoted
// executable followed by its subcommand. Windows keeps the quotes as part of
// the value, so a path with spaces is still one argument.
func windowsRunValue(exe string) string {
	return `"` + exe + `" daemon`
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
