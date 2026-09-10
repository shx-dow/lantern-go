package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/shx-dow/lantern-go/internal/format"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

type mdnsFilter struct{}

func (mdnsFilter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("[WARN] mdns:")) {
		return len(p), nil
	}
	return os.Stderr.Write(p)
}

func init() {
	log.SetOutput(mdnsFilter{})
}

type cliOptions struct {
	jsonOut    bool
	port       int
	dataDir    string
	outDir     string
	command    string
	positional []string
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage()
		os.Exit(2)
	}

	if opts.command == "" || opts.command == "help" || opts.command == "-h" || opts.command == "--help" {
		usage()
		return
	}

	ln, err := lantern.New(lantern.Config{Port: opts.port, DataDir: opts.dataDir})
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go handleSignal(cancel, opts.jsonOut)

	switch opts.command {
	case "send":
		runSend(ctx, ln, opts)
	case "receive":
		runReceive(ctx, ln, opts)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", opts.command)
		usage()
		os.Exit(2)
	}
}

func parseArgs(args []string) (cliOptions, error) {
	var opts cliOptions
	opts.outDir = "."
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			opts.jsonOut = true
		case a == "--out":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("flag --out needs a value")
			}
			i++
			opts.outDir = args[i]
		case strings.HasPrefix(a, "--out="):
			opts.outDir = strings.TrimPrefix(a, "--out=")
		case a == "--port":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("flag --port needs a value")
			}
			i++
			p, err := strconv.Atoi(args[i])
			if err != nil {
				return opts, fmt.Errorf("invalid --port %q", args[i])
			}
			opts.port = p
		case strings.HasPrefix(a, "--port="):
			p, err := strconv.Atoi(strings.TrimPrefix(a, "--port="))
			if err != nil {
				return opts, fmt.Errorf("invalid --port %q", a)
			}
			opts.port = p
		case a == "--data-dir":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("flag --data-dir needs a value")
			}
			i++
			opts.dataDir = args[i]
		case strings.HasPrefix(a, "--data-dir="):
			opts.dataDir = strings.TrimPrefix(a, "--data-dir=")
		case a == "-h" || a == "--help":
			if opts.command == "" {
				opts.command = "help"
			} else {
				opts.positional = append(opts.positional, a)
			}
		case strings.HasPrefix(a, "-") && a != "-":
			return opts, fmt.Errorf("unknown flag %q", a)
		default:
			if opts.command == "" {
				opts.command = a
			} else {
				opts.positional = append(opts.positional, a)
			}
		}
	}
	return opts, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: lantern [--json] [--port N] [--data-dir DIR] <command> [args]

commands:
  send <path>                share a file (prints a share code)
  receive <code> [output-dir]  fetch a file (or use --out DIR)

flags:
  --json        machine-readable JSONL on stdout (agents/MCP/GUI)
  --out DIR     receive output directory (default ".")
  --port N      listen port (default 0 = random)
  --data-dir DIR  local advertisement directory

examples:
  lantern send ./photo.jpg
  lantern send --json ./photo.jpg
  lantern receive <code>
  lantern receive --json <code> --out ./inbox`)
}

func runSend(ctx context.Context, ln *lantern.Lantern, opts cliOptions) {
	if len(opts.positional) < 1 {
		fatal(opts.jsonOut, fmt.Errorf("usage: lantern send <path>"))
	}
	path := opts.positional[0]

	session, peer, err := ln.ShareSession(ctx, path)
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	defer session.Close()

	enc := json.NewEncoder(os.Stdout)
	if opts.jsonOut {
		enc.Encode(map[string]any{
			"type":      "share",
			"code":      peer.Code,
			"file_name": peer.FileName,
			"file_size": peer.FileSize,
		})
	} else {
		fmt.Printf("share code: %s\n", peer.Code)
		fmt.Fprintln(os.Stderr, "waiting for receiver...")
	}

	failed := runTransfer(ctx, session, enc, opts.jsonOut, "sent", peer.FileName)
	if failed {
		os.Exit(1)
	}
}

func runReceive(ctx context.Context, ln *lantern.Lantern, opts cliOptions) {
	if len(opts.positional) < 1 {
		fatal(opts.jsonOut, fmt.Errorf("usage: lantern receive <code> [output-dir]"))
	}
	code := strings.TrimSpace(opts.positional[0])
	outputDir := opts.outDir
	if len(opts.positional) > 1 && opts.positional[1] != "" {
		outputDir = opts.positional[1]
	}

	session, peer, err := ln.ReceiveSession(ctx, code, outputDir)
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	defer session.Close()

	enc := json.NewEncoder(os.Stdout)
	if opts.jsonOut {
		enc.Encode(map[string]any{
			"type":    "peer",
			"peer_id": peer.ID,
			"code":    peer.Code,
		})
	} else {
		fmt.Fprintf(os.Stderr, "connecting to %s...\n", peer.ID)
	}

	failed := runTransfer(ctx, session, enc, opts.jsonOut, "received:", "")
	if failed {
		os.Exit(1)
	}
}

// runTransfer renders session events until the transfer completes, fails,
// or the context is cancelled. In JSON mode every event is a JSON object on
// stdout for agents/GUI; in human mode progress goes to stderr and only the
// final result touches stdout. It reports whether the transfer failed.
func runTransfer(ctx context.Context, session *lantern.Session, enc *json.Encoder, jsonOut bool, doneVerb, doneName string) bool {
	for {
		select {
		case e, ok := <-session.Events():
			if !ok {
				return false
			}
			switch e.Type {
			case lantern.EventTransferProgress:
				if jsonOut {
					enc.Encode(map[string]any{
						"type":      "progress",
						"file_name": e.FileName,
						"bytes":     e.Bytes,
						"total":     e.Total,
					})
				} else {
					pct := progressPercent(e.Bytes, e.Total)
					fmt.Fprintf(os.Stderr, "\rprogress: %.1f%% (%s / %s)", pct, format.Bytes(e.Bytes), format.Bytes(e.Total))
				}
			case lantern.EventTransferDone:
				name := doneName
				if name == "" {
					name = e.FileName
				}
				if jsonOut {
					enc.Encode(map[string]any{
						"type":      "done",
						"file_name": name,
						"bytes":     e.Bytes,
						"total":     e.Total,
					})
				} else {
					fmt.Fprintf(os.Stderr, "\n%s %s\n", doneVerb, name)
				}
				return false
			case lantern.EventError:
				if jsonOut {
					enc.Encode(map[string]any{
						"type":  "error",
						"error": e.Err.Error(),
					})
				} else {
					fmt.Fprintf(os.Stderr, "\nerror: %v\n", e.Err)
				}
				return true
			}
		case <-ctx.Done():
			if jsonOut {
				enc.Encode(map[string]any{"type": "cancelled"})
			} else {
				fmt.Fprintln(os.Stderr, "\ncancelled")
			}
			return true
		}
	}
}

func fatal(jsonOut bool, err error) {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.Encode(map[string]any{"type": "error", "error": err.Error()})
	} else {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	os.Exit(1)
}

func handleSignal(cancel context.CancelFunc, jsonOut bool) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	<-sig
	if !jsonOut {
		fmt.Fprintln(os.Stderr, "\ninterrupted")
	}
	cancel()
}

func progressPercent(bytes, total int64) float64 {
	if total <= 0 {
		return 100
	}
	return format.Ratio(bytes, total) * 100
}
