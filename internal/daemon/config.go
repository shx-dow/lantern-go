package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shx-dow/lantern-go/internal/paths"
)

// ConfigFile holds lanternd file configuration. Flags always override
// file values; zero values mean "flag decides".
type ConfigFile struct {
	Addr              string   `json:"addr,omitempty"`
	P2PPort           int      `json:"p2p_port,omitempty"`
	DataDir           string   `json:"data_dir,omitempty"`
	DeviceName        string   `json:"device_name,omitempty"`
	SharedDirs        []string `json:"shared_dirs,omitempty"`
	BootstrapPeers    []string `json:"bootstrap_peers,omitempty"`
	RelayAddrs        []string `json:"relay_addrs,omitempty"`
	PeerAddrs         []string `json:"peer_addrs,omitempty"`
	LANOnly           *bool    `json:"lan_only,omitempty"`
	DefaultTTLSeconds int64    `json:"default_ttl_seconds,omitempty"`
}

// DefaultConfigPath returns the platform-appropriate config file path:
// %APPDATA% on Windows, ~/Library/Application Support on macOS, and
// $XDG_CONFIG_HOME or ~/.config elsewhere.
func DefaultConfigPath() string {
	return filepath.Join(paths.Config(), "lanternd.json")
}

// LoadConfigFile reads path, returning zero ConfigFile when path is empty
// or the file does not exist. Malformed files are errors.
func LoadConfigFile(path string) (ConfigFile, error) {
	var cfg ConfigFile
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// WriteConfigFile writes cfg to path as JSON, creating the parent directory.
// It writes a temporary file and renames it into place, so an interrupted
// setup leaves the previous config intact rather than a half-written file the
// daemon refuses to start with.
func WriteConfigFile(path string, cfg ConfigFile) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".lanternd-*.json")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op once the rename succeeds

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
