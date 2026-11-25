package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shx-dow/lantern-go/internal/daemon"
)

func loadSetupConfig(t *testing.T, path string) daemon.ConfigFile {
	t.Helper()
	cfg, err := daemon.LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func setHome(t *testing.T, home string) {
	t.Helper()
	// USERPROFILE is what os.UserHomeDir reads on Windows.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestSetupWritesConfigFromFlags(t *testing.T) {
	share := filepath.Join(t.TempDir(), "share")
	path := filepath.Join(t.TempDir(), "config", "lanternd.json")

	var out bytes.Buffer
	err := runSetup([]string{
		"--config", path,
		"--device-name", "laptop",
		"--shared-dirs", share,
	}, strings.NewReader(""), &out)
	if err != nil {
		t.Fatal(err)
	}

	cfg := loadSetupConfig(t, path)
	if cfg.DeviceName != "laptop" {
		t.Errorf("device_name = %q", cfg.DeviceName)
	}
	if !slices.Equal(cfg.SharedDirs, []string{share}) {
		t.Errorf("shared_dirs = %v, want [%s]", cfg.SharedDirs, share)
	}
	if _, err := os.Stat(share); err != nil {
		t.Errorf("shared directory was not created: %v", err)
	}
	// The point of the file is that the daemon reads it: resolve it the way
	// the daemon will and check the values survive.
	resolved := daemon.Resolve(cfg, daemon.Flags{P2PPort: -1, DefaultTTL: -1, LAN: true})
	if resolved.DeviceName != "laptop" || !slices.Equal(resolved.SharedDirs, []string{share}) {
		t.Errorf("daemon would resolve %+v, want name laptop and share %s", resolved, share)
	}
	if want := "wrote " + path; !strings.Contains(out.String(), want) {
		t.Errorf("output does not say where the config went:\n%s", out.String())
	}
	// A custom path has to appear in the command that reads it back.
	if !strings.Contains(out.String(), "--config") || !strings.Contains(out.String(), path) {
		t.Errorf("output does not show a daemon command that reads this config:\n%s", out.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, unset := range []string{`"addr"`, `"p2p_port"`, `"data_dir"`} {
		if strings.Contains(string(data), unset) {
			t.Errorf("written config has an unset field %s:\n%s", unset, data)
		}
	}
	for _, want := range []string{"lantern service install", "lantern daemon", "lantern doctor", "--allow-writes"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output is missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetupPromptsForMissingValues(t *testing.T) {
	share := filepath.Join(t.TempDir(), "notes")
	path := filepath.Join(t.TempDir(), "lanternd.json")

	var out bytes.Buffer
	if err := runSetup([]string{"--config", path}, strings.NewReader("desk\n"+share+"\n"), &out); err != nil {
		t.Fatal(err)
	}

	cfg := loadSetupConfig(t, path)
	if cfg.DeviceName != "desk" {
		t.Errorf("device_name = %q, want the typed name", cfg.DeviceName)
	}
	if !slices.Equal(cfg.SharedDirs, []string{share}) {
		t.Errorf("shared_dirs = %v", cfg.SharedDirs)
	}
	if !strings.Contains(out.String(), "Device name [") || !strings.Contains(out.String(), "Shared directories [") {
		t.Errorf("the prompts were not shown:\n%s", out.String())
	}
}

func TestSetupAcceptsTheDefaults(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	path := filepath.Join(t.TempDir(), "lanternd.json")

	var out bytes.Buffer
	if err := runSetup([]string{"--config", path}, strings.NewReader("\n\n"), &out); err != nil {
		t.Fatal(err)
	}

	cfg := loadSetupConfig(t, path)
	hostname, _ := os.Hostname()
	if cfg.DeviceName != hostname {
		t.Errorf("device_name = %q, want the hostname %q", cfg.DeviceName, hostname)
	}
	wantDir := filepath.Join(home, "Share")
	if !slices.Equal(cfg.SharedDirs, []string{wantDir}) {
		t.Errorf("shared_dirs = %v, want [%s]", cfg.SharedDirs, wantDir)
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf("the default shared directory was not created: %v", err)
	}
}

func TestSetupExpandsHomeAndDropsDuplicates(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	path := filepath.Join(t.TempDir(), "lanternd.json")

	if err := runSetup([]string{
		"--config", path,
		"--device-name", "laptop",
		"--shared-dirs", "~/Docs,~/Music,~/Docs",
	}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}

	cfg := loadSetupConfig(t, path)
	want := []string{filepath.Join(home, "Docs"), filepath.Join(home, "Music")}
	if !slices.Equal(cfg.SharedDirs, want) {
		t.Errorf("shared_dirs = %v, want %v", cfg.SharedDirs, want)
	}
	for _, dir := range want {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s was not created: %v", dir, err)
		}
	}
}

func TestSetupRefusesToReplaceConfigWithoutForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lanternd.json")
	original := []byte(`{"device_name":"keep"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	err := runSetup([]string{
		"--config", path,
		"--device-name", "replacement",
		"--shared-dirs", t.TempDir(),
	}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want a refusal naming the existing file", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(data, original) {
		t.Errorf("refused setup changed the file:\n%s", data)
	}
}

func TestSetupForceReplacesTheConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lanternd.json")
	if err := os.WriteFile(path, []byte(`{"device_name":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	share := t.TempDir()

	if err := runSetup([]string{
		"--config", path,
		"--force",
		"--device-name", "new",
		"--shared-dirs", share,
	}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}

	cfg := loadSetupConfig(t, path)
	if cfg.DeviceName != "new" || !slices.Equal(cfg.SharedDirs, []string{share}) {
		t.Errorf("config was not replaced: %+v", cfg)
	}
}

func TestSetupUsesTheDefaultConfigPath(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	// The path is platform-specific; set each root so the test writes into the
	// temp directory rather than the real user profile.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "appdata"))

	var out bytes.Buffer
	if err := runSetup([]string{
		"--device-name", "laptop",
		"--shared-dirs", filepath.Join(home, "share"),
	}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}

	path := daemon.DefaultConfigPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config was not written to the default path %s: %v", path, err)
	}
	// The default path needs no --config on the daemon command.
	if strings.Contains(out.String(), "--config") {
		t.Errorf("default-path output asks for --config:\n%s", out.String())
	}
}

func TestSetupRejectsArgumentsItCannotUse(t *testing.T) {
	for _, args := range [][]string{
		{"extra"},
		{"--config", filepath.Join(t.TempDir(), "c.json"), "--device-name", "n", "--shared-dirs", ","},
		{"--not-a-flag"},
	} {
		if err := runSetup(args, strings.NewReader(""), io.Discard); err == nil {
			t.Errorf("runSetup(%v) accepted arguments it should refuse", args)
		}
	}
}
