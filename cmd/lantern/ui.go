package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

// uiURL is the daemon's public page. The base is trimmed so a URL written with
// or without a trailing slash produces the same address.
func uiURL(base string) string {
	return strings.TrimSuffix(base, "/") + "/ui"
}

// browserCommand returns the program and arguments that open a URL on goos.
// Keeping the choice in one place makes it testable without launching a
// browser, which a test on a headless machine cannot do.
func browserCommand(goos, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}

// openBrowser launches this platform's URL opener. It waits for the opener to
// exit rather than only starting it, so "no browser installed" comes back as an
// error instead of a command that reports success and does nothing.
func openBrowser(url string) error {
	name, args := browserCommand(runtime.GOOS, url)
	return exec.Command(name, args...).Run()
}

// runUI opens the daemon's page, printing the address when the browser cannot
// be started so the person still has something to paste.
func runUI(base string, out io.Writer, open func(string) error) error {
	url := uiURL(base)
	if err := open(url); err != nil {
		fmt.Fprintln(out, url)
		return fmt.Errorf("could not open a browser: %w", err)
	}
	return nil
}

// runUIFlags applies the parsed CLI arguments to runUI. It takes no daemon
// client and no token: the page is public, and opening it must work even when
// the daemon is stopped or the token file is missing.
func runUIFlags(opts cliOptions, out io.Writer, open func(string) error) error {
	if len(opts.positional) > 0 {
		return errors.New("usage: lantern ui")
	}
	return runUI(opts.daemonURL, out, open)
}
