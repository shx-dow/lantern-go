// Package paths resolves the per-user directories Lantern keeps its state in.
//
// Everything that must survive a reboot lives here. That matters more than it
// looks: the libp2p identity key is the device's only identity, and the trust
// store is the only record of who it is paired with. Put either of them in a
// temporary directory and a reboot silently mints a new identity and
// invalidates every pairing on every other device, with no way to recover
// except pairing again by hand.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

// appDir is the directory name used under each platform's per-user root.
const appDir = "lantern"

// Data returns the directory for state that must survive a reboot: the
// identity key, the trust store, the daemon token, and the address cache.
//
// It follows each platform's convention rather than assuming Unix:
//
//	Linux/BSD: $XDG_DATA_HOME/lantern, else ~/.local/share/lantern
//	macOS:     ~/Library/Application Support/lantern
//	Windows:   %LOCALAPPDATA%\lantern
//
// It falls back to the home directory and finally to the OS temp directory, so
// it always returns something usable, but those fallbacks are exactly the
// cases where state can be lost.
func Data() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" && runtime.GOOS != "darwin" {
		return filepath.Join(dir, appDir)
	}
	if v := os.Getenv("LOCALAPPDATA"); v != "" && runtime.GOOS == "windows" {
		return filepath.Join(v, appDir)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), appDir)
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", appDir)
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "AppData", "Local", appDir)
	}
	return filepath.Join(home, ".local", "share", appDir)
}

// Config returns the directory for the daemon's configuration file.
//
// On Windows and macOS this uses each platform's own convention so the
// directory looks where a user expects to find it; elsewhere it honours
// XDG_CONFIG_HOME.
func Config() string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if dir, err := os.UserConfigDir(); err == nil && dir != "" {
			return filepath.Join(dir, appDir)
		}
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, appDir)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), appDir)
	}
	return filepath.Join(home, ".config", appDir)
}

// EnsureData returns Data, creating it if needed. It is the entry point
// callers should prefer, so the directory exists before anything writes to it.
func EnsureData() (string, error) {
	return ensure(Data())
}

// EnsureConfig returns Config, creating it if needed.
func EnsureConfig() (string, error) {
	return ensure(Config())
}

func ensure(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}
