package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shx-dow/lantern-go/internal/mcpserver"
	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

func runPrintConfig(t *testing.T, args ...string) mcpserver.ServerEntry {
	t.Helper()
	var out bytes.Buffer
	if err := runMCP(context.Background(), args, strings.NewReader(""), &out); err != nil {
		t.Fatalf("runMCP(%v): %v", args, err)
	}
	var cfg mcpserver.ClientConfig
	if err := json.Unmarshal(out.Bytes(), &cfg); err != nil {
		t.Fatalf("printed config is not JSON: %v\n%s", err, out.String())
	}
	entry, ok := cfg.MCPServers["lantern"]
	if !ok {
		t.Fatalf("no lantern entry in %s", out.String())
	}
	return entry
}

func TestMCPPrintConfigProducesARunnableEntry(t *testing.T) {
	entry := runPrintConfig(t, "--print-config", "--daemon-url", ":45999", "--token-file", "/srv/lantern/token")

	if entry.Command == "" {
		t.Error("command is empty")
	}
	if !filepath.IsAbs(entry.Command) {
		t.Errorf("command = %q, want an absolute path", entry.Command)
	}
	if len(entry.Args) != 1 || entry.Args[0] != "mcp" {
		t.Errorf("args = %v, want [mcp]", entry.Args)
	}
	if got := entry.Env["LANTERND_URL"]; got != "http://127.0.0.1:45999" {
		t.Errorf("LANTERND_URL = %q, want the shorthand expanded", got)
	}
	if got := entry.Env[lanternclient.TokenFileEnv]; got != "/srv/lantern/token" {
		t.Errorf("%s = %q", lanternclient.TokenFileEnv, got)
	}
}

func TestMCPPrintConfigReflectsTheEnvironment(t *testing.T) {
	t.Setenv("LANTERND_URL", "http://127.0.0.1:46000")
	t.Setenv(lanternclient.TokenFileEnv, "/tmp/custom-token")

	entry := runPrintConfig(t, "--print-config")

	if got := entry.Env["LANTERND_URL"]; got != "http://127.0.0.1:46000" {
		t.Errorf("LANTERND_URL = %q, want the inherited value", got)
	}
	if got := entry.Env[lanternclient.TokenFileEnv]; got != "/tmp/custom-token" {
		t.Errorf("%s = %q, want the inherited value", lanternclient.TokenFileEnv, got)
	}
}

func TestMCPPrintConfigNeverPrintsTheToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("do-not-print-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runMCP(context.Background(), []string{"--print-config", "--token-file", path}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "do-not-print-me") {
		t.Fatalf("printed config contains the token:\n%s", out.String())
	}
}

func TestMCPRejectsArgumentsItCannotUse(t *testing.T) {
	for _, args := range [][]string{
		{"--print-config", "extra"},
		{"--daemon-url", "http://127.0.0.1:43782"},
		{"--token-file", "/tmp/token"},
		{"--not-a-flag"},
	} {
		if err := runMCP(context.Background(), args, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Errorf("runMCP(%v) accepted arguments it should refuse", args)
		}
	}
}
