package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestUIURL(t *testing.T) {
	for _, c := range []struct{ base, want string }{
		{defaultDaemonURL, "http://127.0.0.1:43782/ui"},
		{"http://127.0.0.1:43782/", "http://127.0.0.1:43782/ui"},
		{"http://192.0.2.10:9000", "http://192.0.2.10:9000/ui"},
	} {
		if got := uiURL(c.base); got != c.want {
			t.Errorf("uiURL(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

func TestBrowserCommandPerPlatform(t *testing.T) {
	const url = "http://127.0.0.1:43782/ui"
	for _, c := range []struct {
		goos      string
		wantName  string
		wantFirst string
	}{
		{"linux", "xdg-open", url},
		{"freebsd", "xdg-open", url},
		{"darwin", "open", url},
		{"windows", "rundll32", "url.dll,FileProtocolHandler"},
	} {
		name, args := browserCommand(c.goos, url)
		if name != c.wantName {
			t.Errorf("%s: program = %q, want %q", c.goos, name, c.wantName)
		}
		if len(args) == 0 || args[0] != c.wantFirst {
			t.Errorf("%s: args = %v, want first %q", c.goos, args, c.wantFirst)
		}
		if last := args[len(args)-1]; last != url {
			t.Errorf("%s: args = %v, want the URL last", c.goos, args)
		}
	}
}

func TestRunUIFlagsOpensTheConfiguredDaemon(t *testing.T) {
	opts, err := parseArgs([]string{"--daemon-url", ":45999", "ui"})
	if err != nil {
		t.Fatal(err)
	}

	var opened string
	var out bytes.Buffer
	err = runUIFlags(opts, &out, func(url string) error {
		opened = url
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened != "http://127.0.0.1:45999/ui" {
		t.Errorf("opened %q", opened)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q on success, want nothing", out.String())
	}
}

func TestRunUIFlagsDefaultsToTheLocalDaemon(t *testing.T) {
	t.Setenv("LANTERND_URL", "")
	opts, err := parseArgs([]string{"ui"})
	if err != nil {
		t.Fatal(err)
	}

	var opened string
	if err := runUIFlags(opts, &bytes.Buffer{}, func(url string) error {
		opened = url
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if opened != defaultDaemonURL+"/ui" {
		t.Errorf("opened %q, want the default daemon", opened)
	}
}

func TestRunUIPrintsTheURLWhenTheBrowserFails(t *testing.T) {
	var out bytes.Buffer
	err := runUI("http://127.0.0.1:43782", &out, func(string) error {
		return errors.New("no browser")
	})
	if err == nil {
		t.Fatal("runUI reported success when the browser could not start")
	}
	if got := strings.TrimSpace(out.String()); got != "http://127.0.0.1:43782/ui" {
		t.Errorf("printed %q, want the URL to open by hand", got)
	}
}

func TestRunUIFlagsRejectsExtraArguments(t *testing.T) {
	opts, err := parseArgs([]string{"ui", "extra"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runUIFlags(opts, &bytes.Buffer{}, func(string) error { return nil }); err == nil {
		t.Error("runUIFlags accepted an argument it cannot use")
	}
}
