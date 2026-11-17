package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/shx-dow/lantern-go/internal/mcpserver"
	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// mcpExecutable is the path an MCP client should launch. A GUI client often
// runs with a PATH that does not include a shell-installed binary, so the
// absolute path of the running executable is a better answer than the name.
func mcpExecutable() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "lantern"
	}
	return exe
}

// runMCP runs the MCP stdio shim, or prints a ready-to-paste client entry when
// --print-config is given. It owns its flags because the shim is started by a
// client, not typed by a person following the usual CLI grammar.
func runMCP(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	printConfig := fs.Bool("print-config", false, "print an MCP client entry and exit")
	daemonURL := fs.String("daemon-url", "", "daemon base URL for the printed config")
	tokenFile := fs.String("token-file", "", "token file for the printed config")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: lantern mcp [--print-config] [--daemon-url URL] [--token-file FILE]")
	}

	// A value in the environment is what the shim itself would use, so the
	// printed entry must name it too. Omitting an inherited setting would
	// produce a config that only works in the shell that printed it.
	rawURL := *daemonURL
	if rawURL == "" {
		rawURL = os.Getenv("LANTERND_URL")
	}
	file := *tokenFile
	if file == "" {
		file = os.Getenv(lanternclient.TokenFileEnv)
	}

	if *printConfig {
		return mcpserver.PrintConfig(stdout, mcpExecutable(), normalizeBaseURL(rawURL), file)
	}
	if rawURL != "" || file != "" {
		return errors.New("--daemon-url and --token-file only apply to --print-config")
	}
	return mcpserver.Serve(ctx, stdin, stdout)
}
