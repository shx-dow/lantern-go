package daemon

import (
	"os"
	"path/filepath"
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
