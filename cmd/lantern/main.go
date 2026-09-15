package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/shx-dow/lantern-go/internal/format"
	"github.com/shx-dow/lantern-go/pkg/lantern"
	"github.com/shx-dow/lantern-go/pkg/lanternclient"
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

const defaultDaemonURL = "http://127.0.0.1:43782"

type cliOptions struct {
	jsonOut     bool
	port        int
	dataDir     string
	outDir      string
	daemon      bool
	daemonURL   string
	daemonToken string
	command     string
	positional  []string
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go handleSignal(cancel, opts.jsonOut)

	if opts.daemon {
		runDaemonCommand(ctx, opts)
		return
	}

	ln, err := lantern.New(lantern.Config{Port: opts.port, DataDir: opts.dataDir})
	if err != nil {
		fatal(opts.jsonOut, err)
	}
	defer ln.Close()

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
	opts.daemonURL = os.Getenv("LANTERND_URL")
	if opts.daemonURL == "" {
		opts.daemonURL = defaultDaemonURL
	}
	opts.daemonToken = os.Getenv("LANTERN_DAEMON_TOKEN")
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			opts.jsonOut = true
		case a == "--daemon":
			opts.daemon = true
		case strings.HasPrefix(a, "--daemon="):
			opts.daemon = true
			if v := strings.TrimPrefix(a, "--daemon="); v != "" {
				opts.daemonURL = normalizeBaseURL(v)
			}
		case a == "--daemon-url" || a == "--daemon-addr":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("flag %s needs a value", a)
			}
			i++
			opts.daemon = true
			opts.daemonURL = normalizeBaseURL(args[i])
		case strings.HasPrefix(a, "--daemon-url="):
			opts.daemon = true
			opts.daemonURL = normalizeBaseURL(strings.TrimPrefix(a, "--daemon-url="))
		case strings.HasPrefix(a, "--daemon-addr="):
			opts.daemon = true
			opts.daemonURL = normalizeBaseURL(strings.TrimPrefix(a, "--daemon-addr="))
		case a == "--daemon-token":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("flag --daemon-token needs a value")
			}
			i++
			opts.daemonToken = args[i]
		case strings.HasPrefix(a, "--daemon-token="):
			opts.daemonToken = strings.TrimPrefix(a, "--daemon-token=")
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

func normalizeBaseURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return defaultDaemonURL
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return strings.TrimSuffix(v, "/")
	}
	if strings.HasPrefix(v, ":") {
		return "http://127.0.0.1" + v
	}
	return "http://" + strings.TrimSuffix(v, "/")
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: lantern [--json] [--port N] [--data-dir DIR] [--daemon[=URL]] <command> [args]

  commands:
    send <path>                  share a file or directory (dirs arrive as <name>.zip)
    receive <code> [output-dir]  fetch a file (or use --out DIR)
    status                       daemon status (needs --daemon)
    list [transfers|history]     daemon transfers (needs --daemon)
    peers                        connected peers (needs --daemon)
    discover                     self + connected peers (needs --daemon)
    trust list                   paired devices (needs --daemon)
    trust add <peer-id> [alias]  pair a device (needs --daemon)
    trust remove <peer-id>       unpair a device (needs --daemon)
    files [dir]                  list shared-dir files (needs --daemon)
    remote-files <peer-id> [dir] list files on a connected peer (needs --daemon)

flags:
  --json            machine-readable JSONL on stdout (agents/MCP/GUI)
  --out DIR         receive output directory (default ".")
  --port N          in-process listen port (default 0 = random)
  --data-dir DIR    in-process local advertisement directory
  --daemon[=URL]    talk to lanternd instead of in-process node
                    (default URL http://127.0.0.1:43782 or $LANTERND_URL)
  --daemon-url URL  same as --daemon=URL
  --daemon-token T  daemon bearer token (or $LANTERN_DAEMON_TOKEN)

examples:
  lantern send ./photo.jpg
  lantern send --json ./photo.jpg
  lantern --daemon send ./photo.jpg
  lantern --daemon receive <code> --out ./inbox
  lantern --daemon status`)
}

// daemonRecord aliases the shared client record for compactness.
type daemonRecord = lanternclient.Record

type daemonClient = lanternclient.Client

func newDaemonClient(base, token string) *daemonClient {
	return lanternclient.New(base, token)
}

func apiError(resp *http.Response) error {
	var m map[string]string
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
	if err := json.Unmarshal(body, &m); err == nil && m["error"] != "" {
		return fmt.Errorf("daemon: %s (status %d)", m["error"], resp.StatusCode)
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("daemon: %s (status %d)", msg, resp.StatusCode)
}

func runDaemonCommand(ctx context.Context, opts cliOptions) {
	c := newDaemonClient(opts.daemonURL, opts.daemonToken)
	enc := json.NewEncoder(os.Stdout)
	switch opts.command {
	case "send":
		if len(opts.positional) < 1 {
			fatal(opts.jsonOut, fmt.Errorf("usage: lantern send <path>"))
		}
		var rec daemonRecord
		rec, err := c.Share(opts.positional[0], 0)
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(map[string]any{"type": "share", "id": rec.ID, "code": rec.Code, "file_name": rec.FileName, "file_size": rec.FileSize})
		} else {
			fmt.Printf("share code: %s\n", rec.Code)
			fmt.Fprintln(os.Stderr, "waiting for receiver...")
		}
		if runDaemonTransfer(ctx, c, enc, opts.jsonOut, rec.ID, "sent", rec.FileName) {
			os.Exit(1)
		}
	case "receive":
		if len(opts.positional) < 1 {
			fatal(opts.jsonOut, fmt.Errorf("usage: lantern receive <code> [output-dir]"))
		}
		code := strings.TrimSpace(opts.positional[0])
		outputDir := opts.outDir
		if len(opts.positional) > 1 && opts.positional[1] != "" {
			outputDir = opts.positional[1]
		}
		var rec daemonRecord
		rec, err := c.Fetch(code, outputDir)
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(map[string]any{"type": "peer", "id": rec.ID, "peer_id": rec.PeerID, "code": rec.Code})
		} else {
			fmt.Fprintf(os.Stderr, "fetching %s...\n", rec.ID)
		}
		if runDaemonTransfer(ctx, c, enc, opts.jsonOut, rec.ID, "received:", "") {
			os.Exit(1)
		}
	case "status":
		st, err := c.Status()
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(st)
		} else {
			fmt.Printf("device: %s\npeer: %s\nlan_only: %v\naddrs:\n", st.DeviceName, st.PeerID, st.LANOnly)
			for _, a := range st.Addrs {
				fmt.Printf("  %s\n", a)
			}
		}
	case "discover":
		st, peers, err := c.Discover()
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(map[string]any{"self": st, "peers": peers})
		} else {
			fmt.Printf("self: %s (%s)\n", st.DeviceName, st.PeerID)
			if len(peers) == 0 {
				fmt.Println("no connected peers")
			} else {
				fmt.Println("peers:")
				for _, p := range peers {
					fmt.Printf("  %s\n", p.ID)
					for _, a := range p.Addrs {
						fmt.Printf("    %s\n", a)
					}
				}
			}
		}
	case "peers":
		peers, err := c.Peers()
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(map[string]any{"peers": peers})
		} else if len(peers) == 0 {
			fmt.Println("no connected peers")
		} else {
			for _, p := range peers {
				fmt.Printf("%s\n", p.ID)
				for _, a := range p.Addrs {
					fmt.Printf("  %s\n", a)
				}
			}
		}
	case "list":
		what := "transfers"
		if len(opts.positional) > 0 {
			what = opts.positional[0]
		}
		switch what {
		case "transfers", "shares":
			var transfers []daemonRecord
			var err error
			// /v1/shares returns {"shares":[...]}; normalize to transfers shape.
			if what == "shares" {
				transfers, err = c.Shares()
			} else {
				transfers, err = c.Transfers("")
			}
			if err != nil {
				fatal(opts.jsonOut, err)
			}
			if opts.jsonOut {
				enc.Encode(map[string][]daemonRecord{"transfers": transfers})
			} else if len(transfers) == 0 {
				fmt.Println("no transfers")
			} else {
				for _, r := range transfers {
					fmt.Printf("%s %s %s %d/%d %s\n", r.ID, r.Kind, r.FileName, r.Bytes, r.Total, r.State)
				}
			}
		case "history":
			history, err := c.History()
			if err != nil {
				fatal(opts.jsonOut, err)
			}
			if opts.jsonOut {
				enc.Encode(map[string][]daemonRecord{"history": history})
			} else if len(history) == 0 {
				fmt.Println("no history")
			} else {
				for _, r := range history {
					fmt.Printf("%s %s %s %s\n", r.ID, r.Kind, r.FileName, r.State)
				}
			}
		default:
			fatal(opts.jsonOut, fmt.Errorf("usage: lantern list [transfers|history]"))
		}
	case "trust":
		sub := ""
		if len(opts.positional) > 0 {
			sub = opts.positional[0]
		}
		switch sub {
		case "list", "":
			trusted, err := c.TrustList()
			if err != nil {
				fatal(opts.jsonOut, err)
			}
			if opts.jsonOut {
				enc.Encode(map[string]any{"trusted": trusted})
			} else if len(trusted) == 0 {
				fmt.Println("no paired devices")
			} else {
				for _, p := range trusted {
					fmt.Printf("%s %s\n", p.PeerID, p.Alias)
				}
			}
		case "add":
			if len(opts.positional) < 2 {
				fatal(opts.jsonOut, fmt.Errorf("usage: lantern trust add <peer-id> [alias]"))
			}
			alias := ""
			if len(opts.positional) > 2 {
				alias = opts.positional[2]
			}
			entry, err := c.TrustAdd(opts.positional[1], alias)
			if err != nil {
				fatal(opts.jsonOut, err)
			}
			if opts.jsonOut {
				enc.Encode(entry)
			} else {
				fmt.Printf("paired %s\n", entry.PeerID)
			}
		case "remove", "rm":
			if len(opts.positional) < 2 {
				fatal(opts.jsonOut, fmt.Errorf("usage: lantern trust remove <peer-id>"))
			}
			if err := c.TrustRemove(opts.positional[1]); err != nil {
				fatal(opts.jsonOut, err)
			}
			if opts.jsonOut {
				enc.Encode(map[string]any{"removed": opts.positional[1]})
			} else {
				fmt.Printf("removed %s\n", opts.positional[1])
			}
		default:
			fatal(opts.jsonOut, fmt.Errorf("usage: lantern trust [list|add|remove]"))
		}
	case "files":
		dir := ""
		if len(opts.positional) > 0 {
			dir = opts.positional[0]
		}
		files, err := c.Files(dir)
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(map[string]any{"files": files})
		} else if len(files) == 0 {
			fmt.Println("no files (configure --shared-dirs on lanternd)")
		} else {
			for _, f := range files {
				fmt.Printf("%s %d %s\n", f.Name, f.Size, f.ModTime)
			}
		}
	case "remote-files":
		if len(opts.positional) < 1 {
			fatal(opts.jsonOut, fmt.Errorf("usage: lantern remote-files <peer-id> [dir]"))
		}
		dir := ""
		if len(opts.positional) > 1 {
			dir = opts.positional[1]
		}
		files, err := c.RemoteFiles(opts.positional[0], dir)
		if err != nil {
			fatal(opts.jsonOut, err)
		}
		if opts.jsonOut {
			enc.Encode(map[string]any{"files": files})
		} else if len(files) == 0 {
			fmt.Println("no files")
		} else {
			for _, f := range files {
				fmt.Printf("%s %d %s\n", f.Name, f.Size, f.ModTime)
			}
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", opts.command)
		usage()
		os.Exit(2)
	}
}

type sseEvent struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	FileName string `json:"file_name"`
	Bytes    int64  `json:"bytes"`
	Total    int64  `json:"total"`
	Error    string `json:"error"`
}

// runDaemonTransfer streams SSE events for id until terminal, falling back
// to polling when the stream drops. It mirrors runTransfer output and
// reports whether the transfer failed.
func runDaemonTransfer(ctx context.Context, c *daemonClient, enc *json.Encoder, jsonOut bool, id, doneVerb, doneName string) bool {
	// Fast path: already terminal before SSE connects.
	if rec, err := daemonGet(c, id); err == nil && isTerminal(rec.State) {
		return emitDaemonRecord(enc, jsonOut, rec, doneVerb, doneName)
	}
	if err := streamDaemonEvents(ctx, c, enc, jsonOut, id, doneVerb, doneName); err == nil {
		return false // stream saw terminal (or ctx cancelled, handled inside)
	}
	// Stream dropped without terminal: poll until terminal or ctx done.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if jsonOut {
				enc.Encode(map[string]any{"type": "cancelled", "id": id})
			} else {
				fmt.Fprintln(os.Stderr, "\ncancelled")
			}
			return true
		case <-ticker.C:
			rec, err := daemonGet(c, id)
			if err != nil {
				continue
			}
			emitDaemonProgress(enc, jsonOut, rec)
			if isTerminal(rec.State) {
				return emitDaemonRecord(enc, jsonOut, rec, doneVerb, doneName)
			}
		}
	}
}

func daemonGet(c *daemonClient, id string) (daemonRecord, error) {
	return c.Get(id)
}

func isTerminal(state string) bool {
	return state == "done" || state == "failed" || state == "canceled"
}

func streamDaemonEvents(ctx context.Context, c *daemonClient, enc *json.Encoder, jsonOut bool, id, doneVerb, doneName string) error {
	resp, err := c.OpenEventsStream(ctx)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var eventName, data string
	flush := func() (bool, error) {
		defer func() { eventName, data = "", "" }()
		if data == "" {
			return false, nil
		}
		var e sseEvent
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return false, nil
		}
		if e.ID != "" && e.ID != id {
			return false, nil
		}
		switch e.Type {
		case "progress":
			if jsonOut {
				enc.Encode(map[string]any{"type": "progress", "id": e.ID, "file_name": e.FileName, "bytes": e.Bytes, "total": e.Total})
			} else {
				pct := progressPercent(e.Bytes, e.Total)
				fmt.Fprintf(os.Stderr, "\rprogress: %.1f%% (%s / %s)", pct, format.Bytes(e.Bytes), format.Bytes(e.Total))
			}
			return false, nil
		case "done":
			name := doneName
			if name == "" {
				name = e.FileName
			}
			if jsonOut {
				enc.Encode(map[string]any{"type": "done", "id": e.ID, "file_name": name, "bytes": e.Bytes, "total": e.Total})
			} else {
				fmt.Fprintf(os.Stderr, "\n%s %s\n", doneVerb, name)
			}
			return true, nil
		case "error":
			if jsonOut {
				enc.Encode(map[string]any{"type": "error", "id": e.ID, "error": e.Error})
			} else {
				fmt.Fprintf(os.Stderr, "\nerror: %s\n", e.Error)
			}
			return true, fmt.Errorf("transfer failed")
		case "cancelled":
			if jsonOut {
				enc.Encode(map[string]any{"type": "cancelled", "id": e.ID})
			} else {
				fmt.Fprintln(os.Stderr, "\ncancelled")
			}
			return true, fmt.Errorf("transfer cancelled")
		default:
			_ = eventName
			return false, nil
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if done, err := flush(); err != nil || done {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if v, ok := strings.CutPrefix(line, "event:"); ok {
			eventName = strings.TrimSpace(v)
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			v = strings.TrimSpace(v)
			if data != "" {
				data += "\n"
			}
			data += v
			continue
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("stream closed")
}

func emitDaemonProgress(enc *json.Encoder, jsonOut bool, rec daemonRecord) {
	if jsonOut {
		return // polling path stays quiet; SSE already emitted progress
	}
	if rec.State == "running" && rec.Total > 0 {
		pct := progressPercent(rec.Bytes, rec.Total)
		fmt.Fprintf(os.Stderr, "\rprogress: %.1f%% (%s / %s)", pct, format.Bytes(rec.Bytes), format.Bytes(rec.Total))
	}
}

func emitDaemonRecord(enc *json.Encoder, jsonOut bool, rec daemonRecord, doneVerb, doneName string) bool {
	switch rec.State {
	case "done":
		name := doneName
		if name == "" {
			name = rec.FileName
		}
		if jsonOut {
			enc.Encode(map[string]any{"type": "done", "id": rec.ID, "file_name": name, "bytes": rec.Bytes, "total": rec.Total})
		} else {
			fmt.Fprintf(os.Stderr, "\n%s %s\n", doneVerb, name)
		}
		return false
	case "failed":
		if jsonOut {
			enc.Encode(map[string]any{"type": "error", "id": rec.ID, "error": rec.Error})
		} else {
			fmt.Fprintf(os.Stderr, "\nerror: %s\n", rec.Error)
		}
		return true
	default:
		if jsonOut {
			enc.Encode(map[string]any{"type": "cancelled", "id": rec.ID})
		} else {
			fmt.Fprintln(os.Stderr, "\ncancelled")
		}
		return true
	}
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
