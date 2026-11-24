package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/shx-dow/lantern-go/internal/daemon"
	"github.com/shx-dow/lantern-go/internal/daemonapp"
)

// setupUsage is the help text for `lantern setup`.
const setupUsage = `lantern setup [flags]

Writes the daemon's config file for this device, so the directories to share
and the name other devices see do not have to be typed on every start.

It asks for a device name and the directories to share, creates the shared
directories if they do not exist, and writes the config to the platform's
per-user config directory. It neither starts the daemon nor changes what a
paired device may do.

Flags:
  --config FILE        config file to write (default: the per-user lanternd.json)
  --device-name NAME   name other devices see (default: this machine's hostname)
  --shared-dirs DIRS   comma-separated directories to share (default: ~/Share)
  --force              replace an existing config file

Afterwards:
  lantern daemon       start the service; it reads this config
  lantern doctor       check the setup from another terminal

Writes stay off. A pairing is not permission to write, so the daemon is
read-only unless started with --allow-writes and --writable-dirs DIR. Those are
deliberately not stored in the config file.
`

func runSetup(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "config file to write")
	deviceName := flags.String("device-name", "", "name other devices see")
	sharedDirs := flags.String("shared-dirs", "", "comma-separated directories to share")
	force := flags.Bool("force", false, "replace an existing config file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("usage: lantern setup [--config FILE] [--device-name NAME] [--shared-dirs DIRS] [--force]")
	}

	path := *configPath
	if path == "" {
		path = daemon.DefaultConfigPath()
	}
	if !*force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("config already exists at %s\n"+
				"  re-run with --force to replace it, or edit it directly", path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	reader := bufio.NewReader(in)

	name := strings.TrimSpace(*deviceName)
	if name == "" {
		def := defaultDeviceName()
		answer, err := ask(reader, out, fmt.Sprintf("Device name [%s]: ", def))
		if err != nil {
			return err
		}
		if answer == "" {
			answer = def
		}
		name = answer
	}

	dirsValue := strings.TrimSpace(*sharedDirs)
	if dirsValue == "" {
		def, err := defaultSharedDir()
		if err != nil {
			return err
		}
		answer, err := ask(reader, out, fmt.Sprintf("Shared directories [%s]: ", def))
		if err != nil {
			return err
		}
		if answer != "" {
			dirsValue = answer
		} else {
			dirsValue = def
		}
	}

	dirs, err := prepareSharedDirs(dirsValue)
	if err != nil {
		return err
	}

	if err := daemon.WriteConfigFile(path, daemon.ConfigFile{
		DeviceName: name,
		SharedDirs: dirs,
	}); err != nil {
		return err
	}

	fmt.Fprintf(out, "\nwrote %s\n  device name: %s\n  shared dirs: %s\n", path, name, strings.Join(dirs, ", "))

	daemonCmd := "lantern daemon"
	if path != daemon.DefaultConfigPath() {
		daemonCmd += " --config " + daemonapp.ShellQuote(path)
	}
	fmt.Fprintf(out, `
Next:
  %s
      start the service; it reads this config
  lantern doctor
      check the setup from another terminal

Paired devices can read these directories. Writes stay off: start the daemon
with --allow-writes and --writable-dirs DIR to allow them. Write access is
deliberately not saved in the config file.
`, daemonCmd)
	return nil
}

// ask writes a prompt and reads one line. A closed input is not an error: it
// means the caller is not interactive, and every question has a default.
func ask(r *bufio.Reader, out io.Writer, prompt string) (string, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return "", err
	}
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func defaultDeviceName() string {
	if name, err := os.Hostname(); err == nil && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return "lantern"
}

func defaultSharedDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("cannot find the home directory; pass --shared-dirs")
	}
	return filepath.Join(home, "Share"), nil
}

// prepareSharedDirs expands, cleans, de-duplicates, and creates the directories
// the device will share. Absolute paths are written to the config because the
// daemon does not expand "~" and may run from a different working directory.
func prepareSharedDirs(value string) ([]string, error) {
	var dirs []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(value, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		abs, err := filepath.Abs(expandHome(raw))
		if err != nil {
			return nil, fmt.Errorf("shared directory %s: %w", raw, err)
		}
		abs = filepath.Clean(abs)
		if seen[abs] {
			continue
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return nil, fmt.Errorf("shared directory %s: %w", raw, err)
		}
		seen[abs] = true
		dirs = append(dirs, abs)
	}
	if len(dirs) == 0 {
		return nil, errors.New("setup needs at least one shared directory")
	}
	return dirs, nil
}

// expandHome expands a leading "~" because the daemon treats a shared directory
// as a path, not as a shell word.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
