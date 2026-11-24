package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestLoadConfigFile(t *testing.T) {
	// Missing file is not an error.
	cfg, err := LoadConfigFile(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "" || cfg.P2PPort != 0 {
		t.Fatalf("expected zero config, got %+v", cfg)
	}

	// Round trip.
	dir := t.TempDir()
	path := filepath.Join(dir, "lanternd.json")
	data := `{"addr":"127.0.0.1:9999","p2p_port":4001,"data_dir":"/tmp/x","device_name":"laptop","lan_only":false,"default_ttl_seconds":600}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9999" || cfg.P2PPort != 4001 || cfg.DataDir != "/tmp/x" {
		t.Fatalf("bad decode: %+v", cfg)
	}
	if cfg.DeviceName != "laptop" {
		t.Fatalf("device_name not decoded: %+v", cfg)
	}
	if cfg.LANOnly == nil || *cfg.LANOnly != false {
		t.Fatalf("lan_only pointer not decoded: %+v", cfg)
	}
	if cfg.DefaultTTLSeconds != 600 {
		t.Fatalf("ttl not decoded: %+v", cfg)
	}

	// Malformed JSON errors.
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{nope"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfigFile(bad); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestWriteConfigFileRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "lanternd.json")
	want := ConfigFile{
		Addr:       "127.0.0.1:9999",
		DeviceName: "laptop",
		SharedDirs: []string{"/srv/share"},
		PeerAddrs:  []string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooW"},
		LANOnly:    boolPtr(false),
	}
	if err := WriteConfigFile(path, want); err != nil {
		t.Fatal(err)
	}

	got, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != want.Addr || got.DeviceName != want.DeviceName {
		t.Errorf("round trip lost fields: %+v", got)
	}
	if !slices.Equal(got.SharedDirs, want.SharedDirs) || !slices.Equal(got.PeerAddrs, want.PeerAddrs) {
		t.Errorf("round trip lost lists: %+v", got)
	}
	if got.LANOnly == nil || *got.LANOnly != false {
		t.Errorf("round trip lost lan_only: %+v", got)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "lanternd.json" {
		t.Errorf("config directory has leftovers: %v", entries)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("config mode = %o, want 600", perm)
		}
	}
}

func TestWriteConfigFileReplacesTheExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lanternd.json")
	if err := WriteConfigFile(path, ConfigFile{DeviceName: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfigFile(path, ConfigFile{DeviceName: "new", SharedDirs: []string{"/s"}}); err != nil {
		t.Fatal(err)
	}

	got, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceName != "new" || !slices.Equal(got.SharedDirs, []string{"/s"}) {
		t.Errorf("replacement kept stale fields: %+v", got)
	}
}
