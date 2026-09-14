package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ConfigFile holds lanternd file configuration. Flags always override
// file values; zero values mean "flag decides".
type ConfigFile struct {
	Addr              string   `json:"addr"`
	P2PPort           int      `json:"p2p_port"`
	DataDir           string   `json:"data_dir"`
	DeviceName        string   `json:"device_name,omitempty"`
	SharedDirs        []string `json:"shared_dirs,omitempty"`
	LANOnly           *bool    `json:"lan_only,omitempty"`
	DefaultTTLSeconds int64    `json:"default_ttl_seconds,omitempty"`
}

// DefaultConfigPath returns $XDG_CONFIG_HOME/lantern/lanternd.json,
// falling back to ~/.config/lantern/lanternd.json.
func DefaultConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "lantern", "lanternd.json")
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
