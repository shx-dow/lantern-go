package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lpcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shx-dow/lantern-go/internal/identity"
	"github.com/shx-dow/lantern-go/internal/paths"
)

const cliPassphrase = "correct horse battery"

// identitySeed writes a complete identity directory for a CLI test.
func identitySeed(t *testing.T) (dir, peerID string, keyRaw []byte) {
	t.Helper()
	dir = t.TempDir()
	priv, _, err := lpcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyRaw, err = lpcrypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, paths.IdentityKeyFileName), keyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	trust := `[{"peer_id":"12D3KooWDEwzFKVu6kM2atQLgDE1PhyfACEc9uwqtzh2zQmv1iWE","alias":"windows","added_at":"2026-01-02T03:04:05Z","tier":"read"}]`
	if err := os.WriteFile(filepath.Join(dir, paths.TrustFileName), []byte(trust), 0o600); err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return dir, pid.String(), keyRaw
}

func passphraseFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "passphrase")
	if err := os.WriteFile(path, []byte(cliPassphrase+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// configFlag keeps export away from the machine's real config file.
func configFlag(t *testing.T, deviceName string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lanternd.json")
	if deviceName != "" {
		raw, err := json.Marshal(map[string]string{"device_name": deviceName})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return []string{"--config", path}
}

func TestIdentityExportAndImportThroughTheCLI(t *testing.T) {
	src, peerID, keyRaw := identitySeed(t)
	pass := passphraseFile(t)
	archive := filepath.Join(t.TempDir(), "identity.lantern")

	var out, errOut bytes.Buffer
	args := append([]string{"export", "--data-dir", src, "-o", archive, "--passphrase-file", pass}, configFlag(t, "shx-lab")...)
	if err := runIdentity(args, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("export: %v (stderr %q)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "wrote "+archive) {
		t.Fatalf("export output: %q", out.String())
	}
	fi, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("archive permissions %o, want 600", perm)
	}

	dst := t.TempDir()
	out.Reset()
	errOut.Reset()
	args = []string{"import", "--data-dir", dst, "--passphrase-file", pass, archive}
	if err := runIdentity(args, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("import: %v (stderr %q)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "restored") || !strings.Contains(out.String(), peerID) {
		t.Fatalf("import output: %q", out.String())
	}

	got, err := os.ReadFile(filepath.Join(dst, paths.IdentityKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, keyRaw) {
		t.Fatal("the CLI restore did not write the exported key")
	}
	if _, err := os.Stat(filepath.Join(dst, paths.TrustFileName)); err != nil {
		t.Fatalf("the CLI restore did not write the pairings: %v", err)
	}
}

func TestIdentityUsesTheDataDirFromTheConfig(t *testing.T) {
	src, peerID, keyRaw := identitySeed(t)
	pass := passphraseFile(t)
	archive := filepath.Join(t.TempDir(), "identity.lantern")

	configPath := filepath.Join(t.TempDir(), "lanternd.json")
	raw, err := json.Marshal(map[string]any{"device_name": "shx-lab", "data_dir": src})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := runIdentity([]string{"export", "-o", archive, "--passphrase-file", pass, "--config", configPath},
		strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("export with data_dir from config: %v", err)
	}

	dst := t.TempDir()
	importConfig := filepath.Join(t.TempDir(), "lanternd.json")
	raw, err = json.Marshal(map[string]any{"data_dir": dst})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(importConfig, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runIdentity([]string{"import", "--config", importConfig, "--passphrase-file", pass, archive},
		strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("import with data_dir from config: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, paths.IdentityKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, keyRaw) {
		t.Fatal("the restore did not use the configured data directory")
	}
	if !strings.Contains(out.String(), peerID) {
		t.Fatalf("import output does not name the restored peer: %q", out.String())
	}
}

func TestIdentityExportRefusesToOverwriteAnArchiveWithoutForce(t *testing.T) {
	src, _, _ := identitySeed(t)
	pass := passphraseFile(t)
	archive := filepath.Join(t.TempDir(), "identity.lantern")
	if err := os.WriteFile(archive, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	args := append([]string{"export", "--data-dir", src, "-o", archive, "--passphrase-file", pass}, configFlag(t, "")...)
	err := runIdentity(args, strings.NewReader(""), &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error %v, want an overwrite refusal", err)
	}
	if raw, readErr := os.ReadFile(archive); readErr != nil || string(raw) != "keep me" {
		t.Fatalf("a refused export changed the archive: %q, %v", raw, readErr)
	}
}

func TestIdentityImportRefusesToReplaceAnIdentityWithoutForce(t *testing.T) {
	src, _, _ := identitySeed(t)
	pass := passphraseFile(t)
	archive := filepath.Join(t.TempDir(), "identity.lantern")

	var out, errOut bytes.Buffer
	args := append([]string{"export", "--data-dir", src, "-o", archive, "--passphrase-file", pass}, configFlag(t, "")...)
	if err := runIdentity(args, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}

	dst, _, dstKey := identitySeed(t)
	out.Reset()
	err := runIdentity([]string{"import", "--data-dir", dst, "--passphrase-file", pass, archive}, strings.NewReader(""), &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error %v, want a refusal that names --force", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(dst, paths.IdentityKeyFileName)); readErr != nil || !bytes.Equal(got, dstKey) {
		t.Fatal("a refused import changed the existing key")
	}
}

func TestIdentityExportNeedsAPassphraseFileWithoutATerminal(t *testing.T) {
	src, _, _ := identitySeed(t)
	var out, errOut bytes.Buffer
	args := append([]string{"export", "--data-dir", src}, configFlag(t, "")...)
	err := runIdentity(args, strings.NewReader(""), &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "--passphrase-file") {
		t.Fatalf("error %v, want the passphrase-file hint", err)
	}
}

func TestIdentityExportJSON(t *testing.T) {
	src, peerID, _ := identitySeed(t)
	pass := passphraseFile(t)
	archive := filepath.Join(t.TempDir(), "identity.lantern")

	var out, errOut bytes.Buffer
	args := append([]string{"export", "--json", "--data-dir", src, "-o", archive, "--passphrase-file", pass}, configFlag(t, "shx-lab")...)
	if err := runIdentity(args, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Archive    string `json:"archive"`
		PeerID     string `json:"peer_id"`
		DeviceName string `json:"device_name"`
		Pairings   int    `json:"pairings"`
		CreatedAt  string `json:"created_at"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %q (%v)", out.String(), err)
	}
	if got.Archive != archive || got.PeerID != peerID || got.DeviceName != "shx-lab" || got.Pairings != 1 || got.CreatedAt == "" {
		t.Fatalf("JSON output %+v", got)
	}
}

func TestIdentityPassphraseFromStdin(t *testing.T) {
	src, _, _ := identitySeed(t)
	archive := filepath.Join(t.TempDir(), "identity.lantern")
	var out, errOut bytes.Buffer
	args := append([]string{"export", "--data-dir", src, "-o", archive, "--passphrase-file", "-"}, configFlag(t, "")...)
	if err := runIdentity(args, strings.NewReader(cliPassphrase+"\n"), &out, &errOut); err != nil {
		t.Fatalf("export with stdin passphrase: %v", err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityRejectsUnknownSubcommandsAndExplainsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := runIdentity(nil, strings.NewReader(""), &out, &errOut); err == nil {
		t.Fatal("no subcommand was accepted")
	}
	if err := runIdentity([]string{"bogus"}, strings.NewReader(""), &out, &errOut); err == nil ||
		!strings.Contains(err.Error(), "unknown identity subcommand") {
		t.Fatalf("error %v", err)
	}
	out.Reset()
	if err := runIdentity([]string{"help"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "lantern identity <export|import>") {
		t.Fatalf("help output %q", out.String())
	}
}

func TestIdentityImportRejectsADirectory(t *testing.T) {
	var out, errOut bytes.Buffer
	err := runIdentity([]string{"import", t.TempDir()}, strings.NewReader(""), &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("error %v", err)
	}
}

func TestDefaultArchivePath(t *testing.T) {
	header := identity.Header{DeviceName: "shx-lab", PeerID: "12D3KooWDEwzFKVu6kM2atQLgDE1PhyfACEc9uwqtzh2zQmv1iWE"}
	got := defaultArchivePath(header)
	if !strings.HasPrefix(got, "lantern-identity-shx-lab-") || !strings.HasSuffix(got, ".lantern") {
		t.Fatalf("default path %q", got)
	}

	header = identity.Header{PeerID: "12D3KooWDEwzFKVu6kM2atQLgDE1PhyfACEc9uwqtzh2zQmv1iWE"}
	got = defaultArchivePath(header)
	if !strings.HasPrefix(got, "lantern-identity-12D3KooWDEwz-") {
		t.Fatalf("unnamed default path %q", got)
	}
}

func TestArchiveLabel(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"shx-lab", "shx-lab"},
		{"Shx Lab", "shx-lab"},
		{"shx.lab_2", "shx-lab_2"},
		{"  ", ""},
		{"ランプ", ""},
		{"../etc/passwd", "etcpasswd"},
	} {
		if got := archiveLabel(tt.in); got != tt.want {
			t.Errorf("archiveLabel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
