package main

import (
	"testing"

	"github.com/shx-dow/lantern-go/internal/daemon"
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
