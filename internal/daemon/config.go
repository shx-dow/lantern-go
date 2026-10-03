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
	Addr              string   `json:"addr"`
	P2PPort           int      `json:"p2p_port"`
	DataDir           string   `json:"data_dir"`
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
