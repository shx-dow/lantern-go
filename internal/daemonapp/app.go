// Package daemonapp runs the Lantern daemon: the background service that
// pairs devices, serves shared directories, and serves the HTTP API that the
// CLI, agents, and SDKs talk to.
//
// It lives in an internal package rather than a main package so the single
// lantern binary can expose it as a subcommand, and so it can be started and
// stopped from a test without spawning a process.
package daemonapp

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shx-dow/lantern-go/internal/daemon"
	"github.com/shx-dow/lantern-go/internal/p2p"
	"github.com/shx-dow/lantern-go/internal/paths"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

// Usage is the daemon's help text, also shown by `lantern help daemon`.
const Usage = `lantern daemon [flags]

Runs the background service: pairs devices, serves shared directories, and
serves the local HTTP API that the CLI, agents, and SDKs use.

It listens on localhost only. Writes are refused unless --allow-writes is
passed, so pairing a device is never by itself enough to change anything here.

Flags:
  --addr string               localhost HTTP listen address (default 127.0.0.1:43782)
  --p2p-port int              libp2p listen port (0 = random)
  --data-dir string           identity, pairings, and token live here
                              (default: per-user data dir, never a temp dir)
  --device-name string        human name for this device (default hostname)
  --shared-dirs string        comma-separated dirs exposed to paired devices
  --writable-dirs string      comma-separated dirs paired devices may write to
                              (default: same as --shared-dirs)
  --allow-writes              let paired devices write files here (default false)
  --max-write-bytes int       largest single file a paired device may push (default 512 MiB)
  --lan                       LAN-only: no public DHT bootstraps (default true)
  --no-lan                    use the global DHT instead
  --bootstrap string          comma-separated bootstrap multiaddrs
  --relay string              comma-separated static relay multiaddrs
  --token string              bearer token (default $LANTERND_TOKEN, else persisted)
  --default-ttl int           default share lifetime in seconds (0 = no expiry)
  --config string             config file (default $XDG_CONFIG_HOME/lantern/lanternd.json)
`

// mdnsFilter swallows the mDNS multicast warning. A node on a busy network
// can receive stray announcements for other services, and the warning says
// so every time; it is noise rather than a fault the operator can act on.
type mdnsFilter struct{ w io.Writer }

func (f mdnsFilter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("[WARN] mdns:")) {
		return len(p), nil
	}
	return f.w.Write(p)
}

// Run starts the daemon and blocks until ctx is cancelled, then shuts down
// gracefully. All failure modes are returned rather than fatal, so a caller
// decides how to exit.
func Run(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, Usage) }

	var (
		addr       = fs.String("addr", "", "localhost HTTP listen address (default from config file or 127.0.0.1:43782)")
		p2pPort    = fs.Int("p2p-port", -1, "libp2p listen port (0 = random, -1 = config default)")
		dataDir    = fs.String("data-dir", "", "state directory for identity, pairings, and token (default: per-user data dir)")
		lan        = fs.Bool("lan", true, "LAN-only mode: no public DHT bootstraps, mDNS plus local adverts")
		lanNeg     = fs.Bool("no-lan", false, "disable LAN-only mode (global DHT)")
		configPath = fs.String("config", "", "config file path (default $XDG_CONFIG_HOME/lantern/lanternd.json)")
		tokenFlag  = fs.String("token", "", "bearer token (default $LANTERND_TOKEN, else persisted in data dir)")
		defaultTTL = fs.Int64("default-ttl", -1, "default share lifetime in seconds (0 = no expiry, -1 = config default)")
		deviceName = fs.String("device-name", "", "human alias for this device (default config device_name or OS hostname)")
		sharedDirs = fs.String("shared-dirs", "", "comma-separated dirs exposed via GET /v1/files (default config shared_dirs)")
		bootstrapF = fs.String("bootstrap", "", "comma-separated bootstrap multiaddrs (default config bootstrap_peers; 'none' = LAN-only)")
		relayF     = fs.String("relay", "", "comma-separated static relay multiaddrs for NAT traversal (default config relay_addrs)")
		allowWrite = fs.Bool("allow-writes", false, "let paired devices write files into shared-dirs (default: this device is read-only)")
		writableF  = fs.String("writable-dirs", "", "comma-separated dirs paired devices may write to (default: same as --shared-dirs)")
		maxWriteF  = fs.Int64("max-write-bytes", -1, "largest single file a paired device may push here (0 = 512 MiB, -1 = default)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := log.New(mdnsFilter{w: stderr}, "", log.LstdFlags)

	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = daemon.DefaultConfigPath()
	}
	cfg, err := daemon.LoadConfigFile(cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	r := daemon.Resolve(cfg, daemon.Flags{
		Addr: *addr, P2PPort: *p2pPort, DataDir: *dataDir,
		DeviceName: *deviceName, SharedDirs: *sharedDirs,
		Bootstrap: *bootstrapF, Relay: *relayF, Token: *tokenFlag,
		DefaultTTL: *defaultTTL, LAN: *lan, LANNeg: *lanNeg,
		LANSet: flagSet(fs, "lan"),
	})
	listenAddr, port, dir := r.Addr, r.P2PPort, r.DataDir
	lanOnly, ttl := r.LANOnly, r.DefaultTTL
	name := r.DeviceName

	// Resolve the state directory once, here, rather than letting each
	// call site fall back on its own. Everything below then works on the same
	// resolved path, and the path can be shown to the operator instead of
	// being an empty string they have to guess at.
	if strings.TrimSpace(dir) == "" {
		dir = paths.Data()
	}
	if _, err := paths.EnsureData(); err != nil {
		return fmt.Errorf("state directory: %w", err)
	}

	ln, err := lantern.New(lantern.Config{Port: port, DataDir: dir, Bootstrap: r.BootstrapPeers, Relay: r.RelayAddrs})
	if err != nil {
		return fmt.Errorf("init p2p: %w", err)
	}
	defer ln.Close()

	token, err := daemon.LoadOrCreateToken(dir, r.TokenSeed)
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}

	d := daemon.New(ln)
	d.SharedDirs = r.SharedDirs
	trust, err := daemon.NewTrustStore(dir)
	if err != nil {
		return fmt.Errorf("trust store: %w", err)
	}
	d.Trust = trust
	// Let the session layer borrow the trust store's address cache, so a
	// restart can reach a paired device before the network has announced it
	// again.
	ln.WithKnownAddrs(d.PeerAddrs)

	if node := ln.Node(); node != nil && node.Host != nil {
		roots := d.SharedDirs
		node.SetListAccess(roots, func(id string) bool { return trust.Trusted(id) })
		node.RegisterListHandler()
		node.RegisterFSHandler()

		// Writes are opt-in. Without --allow-writes this device serves reads
		// and refuses every write, so pairing a device is never by itself
		// enough to change anything here.
		if *allowWrite {
			writable := roots
			if s := strings.TrimSpace(*writableF); s != "" {
				writable = daemon.SplitCSV(s)
			}
			var maxWrite int64 = p2p.DefaultMaxWriteBytes
			if *maxWriteF > 0 {
				maxWrite = *maxWriteF
			}
			node.SetWritePolicy(&p2p.WritePolicy{
				Mode:     p2p.WriteSharedRoots,
				Roots:    writable,
				MaxBytes: maxWrite,
			})
			logger.Printf("writes enabled for paired devices, limited to %v", writable)
		} else {
			logger.Printf("writes are disabled; pass --allow-writes to let paired devices write here")
		}
	}

	var addrs []string
	var peerID string
	// Host stays exported for dialing until the transport seam is extracted.
	if node := ln.Node(); node != nil && node.Host != nil {
		peerID = node.Host.ID().String()
		for _, a := range node.Host.Addrs() {
			addrs = append(addrs, a.String())
		}
	}

	mux := http.NewServeMux()
	daemon.NewHandler(d, peerID, addrs, lanOnly, ttl, dir).WithDeviceName(name).Routes(mux)

	// The UI page is public (it holds no secrets; API calls carry the token
	// from browser storage). Everything under /v1/ stays authed.
	top := http.NewServeMux()
	page, contentType := daemon.UI()
	top.HandleFunc("GET /ui", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(page)
	})
	// Unqualified so it never conflicts with the authed /v1/ subtree.
	top.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/ui", http.StatusFound)
	})
	top.Handle("/v1/", daemon.RequireAuth(mux, token))

	srv := &http.Server{Addr: listenAddr, Handler: daemon.CORS(top)}

	serveErr := make(chan error, 1)
	go func() {
		fmt.Fprintf(stderr, "lantern daemon listening on http://%s/ui (lan_only=%v)\n", listenAddr, lanOnly)
		fmt.Fprintf(stderr, "  identity and pairings: %s\n", dir)
		// An agent client needs this token and nothing else reveals it, so
		// say where it is rather than making people search the filesystem.
		fmt.Fprintf(stderr, "  daemon token:          %s\n", daemon.TokenPath(dir))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	fmt.Fprintln(stderr, "\nshutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Printf("shutdown: %v", err)
	}
	return nil
}

// flagSet reports whether name was given explicitly, so a config file can
// override the flag default without fighting it.
func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// Stderr is where the daemon writes when it is run as a subcommand.
var Stderr io.Writer = os.Stderr
