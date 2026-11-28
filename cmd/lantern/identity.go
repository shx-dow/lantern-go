package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/shx-dow/lantern-go/internal/daemon"
	"github.com/shx-dow/lantern-go/internal/identity"
	"github.com/shx-dow/lantern-go/internal/storage"
)

// identityUsage is the help text for `lantern identity`, also shown by
// `lantern help identity`.
const identityUsage = `lantern identity <export|import> [flags]

Backs up this device's identity and pairings, or restores them.

The identity is the private key behind this device's peer ID. Other devices
pair with that peer ID, so losing the key means pairing every one of them
again. "export" writes the key and the trust store to one archive, encrypted
with a passphrase; "import" restores that archive, keeping the peer ID and the
pairings.

Commands:
  export [-o FILE]   write an encrypted archive
  import FILE        restore an archive into the data directory

Flags:
  --data-dir DIR        state directory (default: data_dir in the config, else
                        the per-user data directory)
  -o, --out FILE        archive path for export
  --config FILE         config file to read data_dir, and the device name on
                        export, from (default: the per-user lanternd.json)
  --passphrase-file F   read the passphrase from F; "-" reads one line from stdin
  --force               replace an existing archive or identity
  --json                machine-readable JSON on stdout

The archive holds a private key. Without the passphrase it is useless, and
there is no recovery: choose one you will still have when the disk is gone.

export is safe while the daemon runs. import is not: stop the daemon first
("lantern service status" shows how it is registered), because the running
daemon holds the key it loaded.

A restore keeps the peer ID and the pairings. Shared directories and
--writable-dirs are settings of this machine, not identity, and are not
restored. The daemon token is not restored either; the daemon creates one when
it starts.
`

// maxArchiveBytes bounds an import. A trust store with every pairing a device
// could plausibly have is far smaller, and a huge file is not one of ours.
const maxArchiveBytes = 8 << 20

func runIdentity(args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New(`usage: lantern identity export|import; "lantern help identity" lists the flags`)
	}
	switch args[0] {
	case "export":
		return runIdentityExport(args[1:], in, out, errOut)
	case "import":
		return runIdentityImport(args[1:], in, out, errOut)
	case "help", "-h", "--help":
		fmt.Fprint(out, identityUsage)
		return nil
	default:
		return fmt.Errorf("unknown identity subcommand %q\n  usage: lantern identity export|import", args[0])
	}
}

func runIdentityExport(args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("identity export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	dataDir := fs.String("data-dir", "", "state directory (default: the per-user data directory)")
	archivePath := fs.String("out", "", "archive path")
	fs.StringVar(archivePath, "o", "", "archive path")
	configPath := fs.String("config", "", "config file to read the device name from")
	passFile := fs.String("passphrase-file", "", "read the passphrase from FILE (\"-\" = stdin)")
	force := fs.Bool("force", false, "overwrite an existing archive")
	jsonOut := fs.Bool("json", false, "machine-readable JSON on stdout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(out, identityUsage)
			return nil
		}
		return fmt.Errorf("%v\n  usage: lantern identity export [-o FILE]", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q\n  usage: lantern identity export [-o FILE]", fs.Arg(0))
	}

	// Check an explicit destination before asking for a passphrase twice.
	if *archivePath != "" && !*force {
		if err := archiveAbsent(*archivePath); err != nil {
			return err
		}
	}

	cfg, err := loadIdentityConfig(*configPath)
	if err != nil {
		return err
	}
	pass, err := readPassphrase(*passFile, in, errOut, true)
	if err != nil {
		return err
	}
	archive, header, err := identity.Export(identityDataDir(*dataDir, cfg), deviceName(cfg), pass)
	if err != nil {
		return err
	}

	path := *archivePath
	if path == "" {
		path = defaultArchivePath(header)
		if !*force {
			if err := archiveAbsent(path); err != nil {
				return err
			}
		}
	}
	if err := storage.WritePrivateFile(path, archive); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}

	if *jsonOut {
		return json.NewEncoder(out).Encode(struct {
			Archive    string `json:"archive"`
			PeerID     string `json:"peer_id"`
			DeviceName string `json:"device_name,omitempty"`
			Pairings   int    `json:"pairings"`
			CreatedAt  string `json:"created_at"`
		}{path, header.PeerID, header.DeviceName, header.Pairings, header.CreatedAt})
	}
	fmt.Fprintf(out, "wrote %s\n  device:   %s\n  peer:     %s\n  pairings: %d\n  created:  %s\n\n"+
		"Keep it somewhere safe. It holds the private key for this device's identity:\n"+
		"anyone with the file and the passphrase can act as this device.\n",
		path, displayDevice(header), header.PeerID, header.Pairings, header.CreatedAt)
	return nil
}

func runIdentityImport(args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("identity import", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	dataDir := fs.String("data-dir", "", "state directory (default: data_dir in the config)")
	configPath := fs.String("config", "", "config file to read data_dir from")
	passFile := fs.String("passphrase-file", "", "read the passphrase from FILE (\"-\" = stdin)")
	force := fs.Bool("force", false, "replace an existing identity")
	jsonOut := fs.Bool("json", false, "machine-readable JSON on stdout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(out, identityUsage)
			return nil
		}
		return fmt.Errorf("%v\n  usage: lantern identity import FILE", err)
	}
	if fs.NArg() != 1 {
		return errors.New("usage: lantern identity import FILE")
	}
	path := fs.Arg(0)

	raw, err := readArchive(path)
	if err != nil {
		return err
	}
	header, err := identity.Inspect(raw)
	if err != nil {
		return err
	}
	if !*jsonOut {
		fmt.Fprintf(out, "archive %s\n  device:   %s\n  peer:     %s\n  created:  %s\n  pairings: %d\n",
			path, displayDevice(header), header.PeerID, header.CreatedAt, header.Pairings)
	}
	cfg, err := loadIdentityConfig(*configPath)
	if err != nil {
		return err
	}
	pass, err := readPassphrase(*passFile, in, errOut, false)
	if err != nil {
		return err
	}
	result, err := identity.Import(identityDataDir(*dataDir, cfg), raw, pass, *force)
	if err != nil {
		return err
	}

	if *jsonOut {
		return json.NewEncoder(out).Encode(struct {
			PeerID     string   `json:"peer_id"`
			DeviceName string   `json:"device_name,omitempty"`
			Pairings   int      `json:"pairings"`
			DataDir    string   `json:"data_dir"`
			Replaced   bool     `json:"replaced"`
			Previous   []string `json:"previous,omitempty"`
		}{result.PeerID, result.DeviceName, result.Pairings, result.DataDir, result.Replaced, result.Previous})
	}
	fmt.Fprintf(out, "restored %s (%s) into %s\n  pairings: %d\n",
		displayDevice(header), result.PeerID, result.DataDir, result.Pairings)
	if result.Replaced {
		fmt.Fprint(out, "  previous files kept:\n")
		for _, p := range result.Previous {
			fmt.Fprintf(out, "    %s\n", p)
		}
	}
	fmt.Fprint(out, "\nStart the daemon again: \"lantern service install\" if it is not installed,\n"+
		"or your platform's service manager if it is.\n")
	return nil
}

// loadIdentityConfig reads the daemon's config, so an export backs up the
// directory the daemon actually uses when data_dir is set there. A missing or
// malformed default config does not block a backup; an explicit --config that
// cannot be read does, because the operator asked for that file.
func loadIdentityConfig(configPath string) (daemon.ConfigFile, error) {
	explicit := strings.TrimSpace(configPath) != ""
	path := configPath
	if !explicit {
		path = daemon.DefaultConfigPath()
	}
	cfg, err := daemon.LoadConfigFile(path)
	if err != nil {
		if explicit {
			return daemon.ConfigFile{}, err
		}
		return daemon.ConfigFile{}, nil
	}
	return cfg, nil
}

// identityDataDir applies the same precedence as the daemon: the flag, then
// the config, then the per-user default (an empty string tells the identity
// package to use it).
func identityDataDir(flagValue string, cfg daemon.ConfigFile) string {
	if dir := strings.TrimSpace(flagValue); dir != "" {
		return dir
	}
	return strings.TrimSpace(cfg.DataDir)
}

// deviceName is the label other devices see. The daemon falls back to the
// hostname when neither a flag nor the config names the device, and the
// archive should carry the same name.
func deviceName(cfg daemon.ConfigFile) string {
	if name := strings.TrimSpace(cfg.DeviceName); name != "" {
		return name
	}
	if hn, err := os.Hostname(); err == nil {
		return strings.TrimSpace(hn)
	}
	return ""
}

func defaultArchivePath(header identity.Header) string {
	label := archiveLabel(header.DeviceName)
	if label == "" {
		label = header.PeerID
		if len(label) > 12 {
			label = label[:12]
		}
	}
	return fmt.Sprintf("lantern-identity-%s-%s.lantern", label, time.Now().Format("20060102"))
}

// archiveLabel keeps a device name usable as a file name: ASCII letters,
// digits, dash, and underscore, with spaces and dots turned into dashes. A
// name written in another script falls back to the peer ID.
func archiveLabel(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-_")
}

func displayDevice(h identity.Header) string {
	if strings.TrimSpace(h.DeviceName) == "" {
		return "(unnamed)"
	}
	return h.DeviceName
}

func archiveAbsent(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("archive already exists at %s\n  pass --force to replace it, or choose another path with -o", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	return nil
}

func readArchive(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not an archive", path)
	}
	if fi.Size() > maxArchiveBytes {
		return nil, fmt.Errorf("%s is too large to be a lantern identity archive", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	return raw, nil
}

// readPassphrase reads the passphrase from a file when one is named, and
// otherwise from the terminal with echo off. Piped stdin is refused rather
// than read silently, because a passphrase typed into a pipeline is usually a
// passphrase left in a shell history or a log.
func readPassphrase(path string, in io.Reader, prompt io.Writer, confirm bool) ([]byte, error) {
	if path != "" {
		return passphraseFromFile(path, in)
	}
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return nil, errors.New("no passphrase: stdin is not a terminal; pass --passphrase-file FILE (\"-\" reads one line from stdin)")
	}
	fmt.Fprint(prompt, "Passphrase: ")
	first, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(prompt)
	if err != nil {
		return nil, fmt.Errorf("read passphrase: %w", err)
	}
	if len(first) == 0 {
		return nil, errors.New("passphrase must not be empty")
	}
	if confirm {
		fmt.Fprint(prompt, "Repeat passphrase: ")
		second, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return nil, fmt.Errorf("read passphrase: %w", err)
		}
		if !bytes.Equal(first, second) {
			return nil, errors.New("passphrases do not match")
		}
	}
	return first, nil
}

func passphraseFromFile(path string, in io.Reader) ([]byte, error) {
	var r io.Reader
	if path == "-" {
		r = in
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("read passphrase file: %w", err)
		}
		defer f.Close()
		r = f
	}
	// One line, bounded: a passphrase file is a line, not a document.
	line, err := bufio.NewReader(io.LimitReader(r, 64<<10)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read passphrase file: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, errors.New("passphrase file is empty")
	}
	return []byte(line), nil
}
