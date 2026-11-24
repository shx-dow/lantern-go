package mcpserver

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// ClientConfig is the document an MCP client reads to launch this shim. It is
// shaped like the `mcpServers` block every client accepts, so the output can be
// pasted into a client config without editing.
type ClientConfig struct {
	MCPServers map[string]ServerEntry `json:"mcpServers"`
}

// ServerEntry is one MCP server registration.
type ServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// PrintConfig writes a ready-to-paste MCP client entry to w.
//
// command is the lantern executable to run, normally an absolute path because
// an MCP client is often launched by a GUI with a PATH that does not include a
// shell-installed binary. daemonURL and tokenFile name the daemon this config
// will reach; an empty daemonURL means the default, and an empty tokenFile
// means the daemon's default token location.
//
// The token path is included for a loopback daemon because that is the only
// case the client reads a file. For a non-loopback daemon a token file would be
// refused (a credential written for this machine must not be sent elsewhere),
// so only the URL is written and the caller has to supply the token through
// the client's environment. The token itself is never printed.
func PrintConfig(w io.Writer, command, daemonURL, tokenFile string) error {
	base := strings.TrimSuffix(strings.TrimSpace(daemonURL), "/")
	if base == "" {
		base = lanternclient.DefaultURL
	}

	env := map[string]string{}
	if base != lanternclient.DefaultURL {
		env["LANTERND_URL"] = base
	}
	if lanternclient.IsLoopbackURL(base) {
		file := strings.TrimSpace(tokenFile)
		if file == "" {
			file = lanternclient.TokenPath()
		}
		env[lanternclient.TokenFileEnv] = file
	}

	entry := ServerEntry{Command: command, Args: []string{"mcp"}}
	if len(env) > 0 {
		entry.Env = env
	}
	return json.NewEncoder(w).Encode(ClientConfig{
		MCPServers: map[string]ServerEntry{"lantern": entry},
	})
}
