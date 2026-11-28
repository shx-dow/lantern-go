package identity

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lpcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shx-dow/lantern-go/internal/paths"
	"github.com/shx-dow/lantern-go/internal/storage"
)

const testPassphrase = "correct horse battery"

const seedTrust = `[
  {
    "peer_id": "12D3KooWDEwzFKVu6kM2atQLgDE1PhyfACEc9uwqtzh2zQmv1iWE",
    "alias": "windows",
    "added_at": "2026-01-02T03:04:05Z",
    "tier": "read-write",
    "writable_roots": ["/home/shx/Inbox"]
  }
]`

// newSeed writes a complete identity directory: a fresh key and one pairing.
func newSeed(t *testing.T) (dir, peerID string, keyRaw, trustRaw []byte) {
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
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	trustRaw = []byte(seedTrust)
	if err := os.WriteFile(filepath.Join(dir, paths.TrustFileName), trustRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, pid.String(), keyRaw, trustRaw
}

func TestExportThenImportRestoresTheIdentityAndPairings(t *testing.T) {
	src, peerID, keyRaw, trustRaw := newSeed(t)

	archive, header, err := Export(src, "shx-lab", []byte(testPassphrase))
	if err != nil {
		t.Fatal(err)
	}
	if header.PeerID != peerID {
		t.Fatalf("header peer %q, want %q", header.PeerID, peerID)
	}
	if header.DeviceName != "shx-lab" {
		t.Fatalf("header device %q", header.DeviceName)
	}
	if header.Pairings != 1 {
		t.Fatalf("header pairings %d, want 1", header.Pairings)
	}

	// Inspect reads the header without the passphrase, which is what import
	// prints before asking for one.
	inspected, err := Inspect(archive)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.PeerID != peerID || inspected.DeviceName != "shx-lab" {
		t.Fatalf("inspect returned %+v", inspected)
	}

	dst := t.TempDir()
	result, err := Import(dst, archive, []byte(testPassphrase), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.PeerID != peerID || result.Pairings != 1 || result.Replaced {
		t.Fatalf("result %+v", result)
	}
	if result.DataDir != dst {
		t.Fatalf("data dir %q, want %q", result.DataDir, dst)
	}

	gotKey, err := os.ReadFile(filepath.Join(dst, paths.IdentityKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotKey, keyRaw) {
		t.Fatal("restored key differs from the exported key")
	}
	gotTrust, err := os.ReadFile(filepath.Join(dst, paths.TrustFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotTrust, trustRaw) {
		t.Fatalf("restored pairings differ:\n%s", gotTrust)
	}

	fi, err := os.Stat(filepath.Join(dst, paths.IdentityKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("restored key permissions %o, want 600", perm)
	}
}

func TestWrongPassphraseWritesNothing(t *testing.T) {
	src, _, _, _ := newSeed(t)
	archive, _, err := Export(src, "shx-lab", []byte(testPassphrase))
	if err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	_, err = Import(dst, archive, []byte("not the passphrase"), false)
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("error %v, want ErrWrongPassphrase", err)
	}
	if _, err := os.Stat(filepath.Join(dst, paths.IdentityKeyFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a wrong passphrase left an identity behind: %v", err)
	}
}

func TestATamperedArchiveIsRefused(t *testing.T) {
	src, _, _, _ := newSeed(t)
	archive, _, err := Export(src, "shx-lab", []byte(testPassphrase))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("ciphertext", func(t *testing.T) {
		bad := append([]byte(nil), archive...)
		bad[len(bad)-1] ^= 0xff
		if _, err := Import(t.TempDir(), bad, []byte(testPassphrase), false); !errors.Is(err, ErrWrongPassphrase) {
			t.Fatalf("error %v, want ErrWrongPassphrase", err)
		}
	})

	t.Run("header", func(t *testing.T) {
		// The header is the AEAD's additional data: renaming the device must
		// break decryption rather than relabel the archive.
		bad := append([]byte(nil), archive...)
		i := bytes.Index(bad, []byte("shx-lab"))
		if i < 0 {
			t.Fatal("device name not found in archive")
		}
		bad[i] = 't'
		if _, err := Import(t.TempDir(), bad, []byte(testPassphrase), false); !errors.Is(err, ErrWrongPassphrase) {
			t.Fatalf("error %v, want ErrWrongPassphrase", err)
		}
	})
}

func TestImportRefusesToReplaceAnIdentityWithoutForce(t *testing.T) {
	src, _, _, _ := newSeed(t)
	archive, _, err := Export(src, "shx-lab", []byte(testPassphrase))
	if err != nil {
		t.Fatal(err)
	}
	dst, _, dstKey, _ := newSeed(t)

	_, err = Import(dst, archive, []byte(testPassphrase), false)
	if err == nil {
		t.Fatal("import replaced an existing identity without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error does not name the fix: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, paths.IdentityKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dstKey) {
		t.Fatal("a refused import changed the existing key")
	}
}

func TestImportWithForceKeepsThePreviousFiles(t *testing.T) {
	src, srcPeer, srcKey, _ := newSeed(t)
	archive, _, err := Export(src, "shx-lab", []byte(testPassphrase))
	if err != nil {
		t.Fatal(err)
	}
	dst, dstPeer, dstKey, dstTrust := newSeed(t)
	if dstPeer == srcPeer {
		t.Fatal("the two seeds share a peer ID")
	}

	result, err := Import(dst, archive, []byte(testPassphrase), true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replaced || len(result.Previous) != 2 {
		t.Fatalf("result %+v", result)
	}

	got, err := os.ReadFile(filepath.Join(dst, paths.IdentityKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, srcKey) {
		t.Fatal("the imported key was not written")
	}

	var keptKey, keptTrust string
	for _, p := range result.Previous {
		switch {
		case strings.HasPrefix(filepath.Base(p), paths.IdentityKeyFileName+".pre-restore-"):
			keptKey = p
		case strings.HasPrefix(filepath.Base(p), paths.TrustFileName+".pre-restore-"):
			keptTrust = p
		default:
			t.Fatalf("unexpected kept file %s", p)
		}
	}
	if keptKey == "" || keptTrust == "" {
		t.Fatalf("missing kept files in %v", result.Previous)
	}
	if raw, err := os.ReadFile(keptKey); err != nil || !bytes.Equal(raw, dstKey) {
		t.Fatalf("previous key not kept intact: %v", err)
	}
	if raw, err := os.ReadFile(keptTrust); err != nil || !bytes.Equal(raw, dstTrust) {
		t.Fatalf("previous pairings not kept intact: %v", err)
	}
}

func TestImportRefusesWhileTheDaemonHoldsTheStateDirectory(t *testing.T) {
	src, _, _, _ := newSeed(t)
	archive, _, err := Export(src, "shx-lab", []byte(testPassphrase))
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	release, err := storage.AcquireDirLock(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	_, err = Import(dst, archive, []byte(testPassphrase), false)
	if err == nil {
		t.Fatal("import ran while the state directory was locked")
	}
	if !strings.Contains(err.Error(), "daemon is running") {
		t.Fatalf("error does not explain the lock: %v", err)
	}
}

func TestPreRestorePathDoesNotReuseAName(t *testing.T) {
	base := filepath.Join(t.TempDir(), paths.IdentityKeyFileName)
	stamp := "20260101T000000"
	first, err := preRestorePath(base, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := preRestorePath(base, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("the second restore would overwrite the first backup")
	}
	if !strings.HasSuffix(second, "-2") {
		t.Fatalf("second backup %q does not carry a counter", second)
	}
}

func TestImportRejectsMalformedArchives(t *testing.T) {
	validPeer := "12D3KooWDEwzFKVu6kM2atQLgDE1PhyfACEc9uwqtzh2zQmv1iWE"
	validKDF := KDFParams{
		Name: kdfName, N: scryptN, R: scryptR, P: scryptP,
		Salt: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, saltSize)),
	}
	header := func(mutate func(*Header)) []byte {
		h := Header{
			Version: formatVersion, PeerID: validPeer, CreatedAt: "2026-01-01T00:00:00Z",
			KDF: validKDF, Cipher: cipherName,
			Nonce: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, nonceSize)),
		}
		if mutate != nil {
			mutate(&h)
		}
		raw, err := json.Marshal(h)
		if err != nil {
			t.Fatal(err)
		}
		out := append([]byte(magicPrefix), formatVersion)
		out = binary.BigEndian.AppendUint32(out, uint32(len(raw)))
		return append(out, raw...)
	}

	tests := []struct {
		name    string
		archive []byte
		want    string
	}{
		{"empty", nil, "not a lantern identity archive"},
		{"wrong magic", []byte("LANTERNNID"), "not a lantern identity archive"},
		{"newer format", append([]byte(magicPrefix), 2, 0, 0, 0, 0), "not supported"},
		{"huge header", func() []byte {
			out := append([]byte(magicPrefix), formatVersion)
			return binary.BigEndian.AppendUint32(out, maxHeaderBytes+1)
		}(), "header is damaged"},
		{"invalid json", func() []byte {
			out := append([]byte(magicPrefix), formatVersion)
			out = binary.BigEndian.AppendUint32(out, 5)
			return append(out, []byte("nope!")...)
		}(), "header is damaged"},
		{"bad peer", header(func(h *Header) { h.PeerID = "not-a-peer" }), "invalid peer ID"},
		{"bad cipher", header(func(h *Header) { h.Cipher = "rot13" }), "unsupported cipher"},
		{"short nonce", header(func(h *Header) { h.Nonce = base64.StdEncoding.EncodeToString([]byte{1, 2, 3}) }), "nonce is damaged"},
		{"bad kdf name", header(func(h *Header) { h.KDF.Name = "pbkdf2" }), "unsupported key derivation"},
		{"huge scrypt cost", header(func(h *Header) { h.KDF.N = 1 << 30 }), "out of range"},
		{"odd scrypt cost", header(func(h *Header) { h.KDF.N = 5000 }), "out of range"},
		{"huge block size", header(func(h *Header) { h.KDF.R = 64 }), "out of range"},
		{"zero parallelism", header(func(h *Header) { h.KDF.P = 0 }), "out of range"},
		{"short salt", header(func(h *Header) { h.KDF.Salt = base64.StdEncoding.EncodeToString([]byte{1}) }), "salt is damaged"},
		{"header version", header(func(h *Header) { h.Version = 2 }), "not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Inspect(tt.archive)
			if err == nil {
				t.Fatal("Inspect accepted a malformed archive")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
			// Import must refuse at the same point, before touching the disk.
			if _, err := Import(t.TempDir(), tt.archive, []byte(testPassphrase), true); err == nil {
				t.Fatal("Import accepted a malformed archive")
			}
		})
	}
}

func TestTheArchiveHoldsNoPlaintextKey(t *testing.T) {
	src, _, keyRaw, _ := newSeed(t)
	archive, _, err := Export(src, "shx-lab", []byte(testPassphrase))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive, keyRaw) {
		t.Fatal("the archive contains the raw private key")
	}
	if bytes.Contains(archive, []byte(base64.StdEncoding.EncodeToString(keyRaw))) {
		t.Fatal("the archive contains the base64 private key")
	}
}

func TestExportRequiresAnIdentityAndAPassphrase(t *testing.T) {
	if _, _, err := Export(t.TempDir(), "shx-lab", []byte(testPassphrase)); err == nil ||
		!strings.Contains(err.Error(), "no identity") {
		t.Fatalf("exporting an empty directory: %v", err)
	}

	src, _, _, _ := newSeed(t)
	if _, _, err := Export(src, "shx-lab", []byte("short")); err == nil ||
		!strings.Contains(err.Error(), "at least") {
		t.Fatalf("short passphrase: %v", err)
	}
}

func TestExportRefusesADamagedIdentity(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, paths.IdentityKeyFileName), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Export(src, "shx-lab", []byte(testPassphrase))
	if err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("error %v, want an unreadable-key refusal", err)
	}
}

func TestAnIdentityWithNoPairingsRoundTrips(t *testing.T) {
	dir := t.TempDir()
	priv, _, err := lpcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := lpcrypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, paths.IdentityKeyFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	archive, header, err := Export(dir, "", []byte(testPassphrase))
	if err != nil {
		t.Fatal(err)
	}
	if header.Pairings != 0 {
		t.Fatalf("pairings %d, want 0", header.Pairings)
	}

	dst := t.TempDir()
	result, err := Import(dst, archive, []byte(testPassphrase), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Pairings != 0 {
		t.Fatalf("restored pairings %d, want 0", result.Pairings)
	}
	if _, err := os.Stat(filepath.Join(dst, paths.TrustFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unpaired archive wrote a trust store: %v", err)
	}
}

func TestRestoringAnUnpairedArchiveDoesNotKeepTheOldPairings(t *testing.T) {
	unpaired := t.TempDir()
	priv, _, err := lpcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := lpcrypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unpaired, paths.IdentityKeyFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	archive, _, err := Export(unpaired, "", []byte(testPassphrase))
	if err != nil {
		t.Fatal(err)
	}

	dst, _, _, _ := newSeed(t)
	result, err := Import(dst, archive, []byte(testPassphrase), true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replaced || len(result.Previous) != 2 {
		t.Fatalf("result %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dst, paths.TrustFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old trust store survived an unpaired restore: %v", err)
	}
}
