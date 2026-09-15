package daemon

import (
	"os"
	"strings"
	"time"
)

// DefaultAddr is the default lanternd listen address.
const DefaultAddr = "127.0.0.1:43782"

// Flags mirrors the lanternd CLI flags as plain values. The zero value
// means "unset": P2PPort and DefaultTTL use -1, strings use "".
type Flags struct {
	Addr       string
	P2PPort    int
	DataDir    string
	DeviceName string
	SharedDirs string
	Bootstrap  string
	Relay      string
	Token      string
	DefaultTTL int64
	LAN        bool
	LANNeg     bool
	// LANSet reports whether -lan was passed explicitly, so the config
	// file can override the default without fighting the flag default.
	LANSet bool
}

// Resolved is the effective runtime configuration: flags win, then the
// config file, then built-in defaults (hostname for the device name).
type Resolved struct {
	Addr           string
	P2PPort        int
	DataDir        string
	DeviceName     string
	SharedDirs     []string
	BootstrapPeers []string
	RelayAddrs     []string
	LANOnly        bool
	DefaultTTL     time.Duration
	TokenSeed      string
}

// Resolve merges file and flags into one tested place. main only parses
// flags and serves; all precedence lives here.
func Resolve(file ConfigFile, f Flags) Resolved {
	var r Resolved
	r.Addr = firstNonEmptyStr(f.Addr, file.Addr, DefaultAddr)
	r.P2PPort = f.P2PPort
	if r.P2PPort < 0 {
		r.P2PPort = file.P2PPort
	}
	r.DataDir = firstNonEmptyStr(f.DataDir, file.DataDir)

	r.LANOnly = f.LAN && !f.LANNeg
	if file.LANOnly != nil && !f.LANSet && !f.LANNeg {
		r.LANOnly = *file.LANOnly
	}

	ttlSecs := f.DefaultTTL
	if ttlSecs < 0 {
		ttlSecs = file.DefaultTTLSeconds
	}
	if ttlSecs > 0 {
		r.DefaultTTL = time.Duration(ttlSecs) * time.Second
	}

	r.BootstrapPeers = []string{"none"}
	if !r.LANOnly {
		r.BootstrapPeers = nil // nil keeps the default public bootstraps
	}
	if strings.TrimSpace(f.Bootstrap) != "" {
		r.BootstrapPeers = SplitCSV(f.Bootstrap)
	} else if len(file.BootstrapPeers) > 0 {
		r.BootstrapPeers = file.BootstrapPeers
	}
	if strings.TrimSpace(f.Relay) != "" {
		r.RelayAddrs = SplitCSV(f.Relay)
	} else {
		r.RelayAddrs = file.RelayAddrs
	}

	r.SharedDirs = file.SharedDirs
	if strings.TrimSpace(f.SharedDirs) != "" {
		r.SharedDirs = SplitCSV(f.SharedDirs)
	}

	r.DeviceName = firstNonEmptyStr(f.DeviceName, file.DeviceName)
	if r.DeviceName == "" {
		if hn, err := os.Hostname(); err == nil {
			r.DeviceName = hn
		}
	}

	r.TokenSeed = firstNonEmptyStr(f.Token, os.Getenv("LANTERND_TOKEN"))
	return r
}

// SplitCSV splits comma-separated flag values, dropping blanks.
func SplitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
