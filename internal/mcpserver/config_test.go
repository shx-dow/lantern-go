package mcpserver

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

func decodeConfig(t *testing.T, buf *bytes.Buffer) ServerEntry {
	t.Helper()
	var cfg ClientConfig
	if err := json.NewDecoder(buf).Decode(&cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	entry, ok := cfg.MCPServers["lantern"]
	if !ok {
		t.Fatalf("no lantern entry in %s", buf.String())
	}
	return entry
}

func TestPrintConfigDefaultsToTheShimAndLocalTokenFile(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintConfig(&buf, "lantern", "", ""); err != nil {
		t.Fatal(err)
	}
	entry := decodeConfig(t, &buf)

	if entry.Command != "lantern" {
		t.Errorf("command = %q, want lantern", entry.Command)
	}
	if len(entry.Args) != 1 || entry.Args[0] != "mcp" {
		t.Errorf("args = %v, want [mcp]", entry.Args)
	}
	if got := entry.Env[lanternclient.TokenFileEnv]; got != lanternclient.TokenPath() {
		t.Errorf("%s = %q, want %q", lanternclient.TokenFileEnv, got, lanternclient.TokenPath())
	}
	// The default URL is what the shim already uses; writing it would make a
	// config that breaks the day the default changes.
	if _, ok := entry.Env["LANTERND_URL"]; ok {
		t.Errorf("default config names LANTERND_URL: %v", entry.Env)
	}
}

func TestPrintConfigNamesANonDefaultLoopbackDaemon(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintConfig(&buf, "lantern", "http://127.0.0.1:45999/", ""); err != nil {
		t.Fatal(err)
	}
	entry := decodeConfig(t, &buf)

	if got := entry.Env["LANTERND_URL"]; got != "http://127.0.0.1:45999" {
		t.Errorf("LANTERND_URL = %q, want the URL without a trailing slash", got)
	}
	if _, ok := entry.Env[lanternclient.TokenFileEnv]; !ok {
		t.Error("loopback config has no token file")
	}
}

func TestPrintConfigHonorsANamedTokenFile(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintConfig(&buf, "lantern", "", "/srv/lantern/token"); err != nil {
		t.Fatal(err)
	}
	entry := decodeConfig(t, &buf)

	if got := entry.Env[lanternclient.TokenFileEnv]; got != "/srv/lantern/token" {
		t.Errorf("%s = %q, want /srv/lantern/token", lanternclient.TokenFileEnv, got)
	}
}

func TestPrintConfigOmitsTokenFileForANonLoopbackDaemon(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintConfig(&buf, "lantern", "http://192.0.2.10:43782", ""); err != nil {
		t.Fatal(err)
	}
	entry := decodeConfig(t, &buf)

	if got := entry.Env["LANTERND_URL"]; got != "http://192.0.2.10:43782" {
		t.Errorf("LANTERND_URL = %q", got)
	}
	// A token written for this machine must not be handed to another host,
	// and ResolveToken refuses that path at run time. Printing it would only
	// produce a config that fails to start.
	if got, ok := entry.Env[lanternclient.TokenFileEnv]; ok {
		t.Errorf("%s = %q for a non-loopback daemon, want it omitted", lanternclient.TokenFileEnv, got)
	}
}
