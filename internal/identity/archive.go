// Package identity exports and restores the files that make a device itself:
// the libp2p private key and the trust store that records its pairings.
//
// A device's peer ID is derived from that key, and every other device pairs
// with the ID. Losing the key does not merely forget the pairings on this
// side: every device that paired with this one now knows an ID that no longer
// exists, so each of them has to be paired again by hand.
//
// That makes a backup a secret rather than a convenience. The archive is
// encrypted with a passphrase the operator chooses, and it carries no
// plaintext key bytes. The token and the machine's settings are deliberately
// not included; see docs/adr/0008-identity-backup-is-an-encrypted-archive.md.
package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	lpcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/crypto/scrypt"

	"github.com/shx-dow/lantern-go/internal/paths"
	"github.com/shx-dow/lantern-go/internal/storage"
)

const (
	// formatVersion is both the byte after the magic and the header's version.
	// A reader refuses anything else rather than guessing at the layout.
	formatVersion = 1
	magicPrefix   = "LANTERNID"

	// maxHeaderBytes bounds the unencrypted header. It holds one peer ID, a
	// device name, and KDF parameters; anything near this is not one of ours.
	maxHeaderBytes = 64 << 10

	kdfName    = "scrypt"
	cipherName = "aes-256-gcm"
	keySize    = 32
	saltSize   = 16
	nonceSize  = 12

	// MinPassphrase is the shortest passphrase export accepts. scrypt makes a
	// short passphrase expensive to attack, but not free, and the archive is
	// the device's whole identity.
	MinPassphrase = 8

	// scryptN/R/P are the work factor export writes: 32 MiB and tens of
	// milliseconds on a laptop, which a dictionary attack cannot skip.
	scryptN = 1 << 15
	scryptR = 8
	scryptP = 1
)

// ErrWrongPassphrase is returned when decryption fails. It deliberately does
// not say whether the passphrase or the file is wrong: GCM cannot tell the
// difference, and claiming otherwise would invite guessing.
var ErrWrongPassphrase = errors.New("wrong passphrase, or the archive is damaged")

// KDFParams describes how the passphrase was stretched. They are stored in
// the header so an archive written with different parameters can still be
// read, and so a reader can refuse parameters that would exhaust memory.
type KDFParams struct {
	Name string `json:"name"`
	N    int    `json:"n"`
	R    int    `json:"r"`
	P    int    `json:"p"`
	Salt string `json:"salt"`
}

// Header is the unencrypted, authenticated part of an archive. It is shown by
// "lantern identity import" before the passphrase is asked for, and it is the
// additional data AES-GCM authenticates, so changing any field breaks the
// decryption instead of quietly renaming the device.
type Header struct {
	Version    int       `json:"version"`
	PeerID     string    `json:"peer_id"`
	DeviceName string    `json:"device_name,omitempty"`
	CreatedAt  string    `json:"created_at"`
	Pairings   int       `json:"pairings"`
	KDF        KDFParams `json:"kdf"`
	Cipher     string    `json:"cipher"`
	Nonce      string    `json:"nonce"`
}

// Result describes a completed restore.
type Result struct {
	PeerID     string
	DeviceName string
	Pairings   int
	DataDir    string
	// Replaced is true when a previous identity or trust store was moved
	// aside. Previous lists those paths.
	Replaced bool
	Previous []string
}

type payload struct {
	IdentityKey  string `json:"identity_key"`
	TrustedPeers string `json:"trusted_peers,omitempty"`
}

// Export reads the identity key and trust store in dataDir and returns an
// encrypted archive and its header. An empty dataDir means the per-user data
// directory.
//
// It only reads, so it is safe while the daemon is running: the key is
// immutable and the trust store is published by rename.
func Export(dataDir, deviceName string, passphrase []byte) ([]byte, Header, error) {
	if len(passphrase) < MinPassphrase {
		return nil, Header{}, fmt.Errorf("passphrase must be at least %d characters", MinPassphrase)
	}
	dir := resolveDir(dataDir)

	keyRaw, err := os.ReadFile(filepath.Join(dir, paths.IdentityKeyFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, Header{}, fmt.Errorf("no identity in %s: run \"lantern setup\" or start the daemon once", dir)
	}
	if err != nil {
		return nil, Header{}, fmt.Errorf("read identity: %w", err)
	}
	priv, err := lpcrypto.UnmarshalPrivateKey(keyRaw)
	if err != nil {
		return nil, Header{}, fmt.Errorf("identity key %s is unreadable: %w\n"+
			"restore it from a backup instead of exporting it", filepath.Join(dir, paths.IdentityKeyFileName), err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return nil, Header{}, fmt.Errorf("identity key has no peer ID: %w", err)
	}

	pairings, trustRaw, err := readTrust(dir)
	if err != nil {
		return nil, Header{}, err
	}

	plain, err := json.Marshal(payload{
		IdentityKey:  base64.StdEncoding.EncodeToString(keyRaw),
		TrustedPeers: base64.StdEncoding.EncodeToString(trustRaw),
	})
	if err != nil {
		return nil, Header{}, fmt.Errorf("encode archive: %w", err)
	}

	salt, err := randomBytes(saltSize)
	if err != nil {
		return nil, Header{}, err
	}
	nonce, err := randomBytes(nonceSize)
	if err != nil {
		return nil, Header{}, err
	}
	header := Header{
		Version:    formatVersion,
		PeerID:     pid.String(),
		DeviceName: strings.TrimSpace(deviceName),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		Pairings:   pairings,
		KDF: KDFParams{
			Name: kdfName,
			N:    scryptN,
			R:    scryptR,
			P:    scryptP,
			Salt: base64.StdEncoding.EncodeToString(salt),
		},
		Cipher: cipherName,
		Nonce:  base64.StdEncoding.EncodeToString(nonce),
	}
	headerRaw, err := json.Marshal(header)
	if err != nil {
		return nil, Header{}, fmt.Errorf("encode archive header: %w", err)
	}
	if len(headerRaw) > maxHeaderBytes {
		return nil, Header{}, errors.New("device name is too long for an archive header")
	}

	key, err := scrypt.Key(passphrase, salt, header.KDF.N, header.KDF.R, header.KDF.P, keySize)
	if err != nil {
		return nil, Header{}, fmt.Errorf("derive archive key: %w", err)
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, Header{}, err
	}
	sealed := aead.Seal(nil, nonce, plain, headerRaw)

	out := make([]byte, 0, len(magicPrefix)+1+4+len(headerRaw)+len(sealed))
	out = append(out, magicPrefix...)
	out = append(out, formatVersion)
	out = binary.BigEndian.AppendUint32(out, uint32(len(headerRaw)))
	out = append(out, headerRaw...)
	out = append(out, sealed...)
	return out, header, nil
}

// Inspect parses an archive's header without the passphrase. It is what the
// CLI shows before asking for one.
func Inspect(archive []byte) (Header, error) {
	header, _, _, err := parse(archive)
	return header, err
}

// Import decrypts archive and restores its key and pairings into dataDir.
//
// The daemon must not be running: import takes the same directory lock the
// daemon holds, so the key it loaded and the key on disk cannot disagree.
// Without force an existing identity is left alone; with force it is moved
// aside as <name>.pre-restore-<timestamp> rather than deleted, so a restore
// that turns out to be the wrong archive can be undone by hand.
func Import(dataDir string, archive, passphrase []byte, force bool) (Result, error) {
	header, headerRaw, sealed, err := parse(archive)
	if err != nil {
		return Result{}, err
	}

	salt, _ := base64.StdEncoding.DecodeString(header.KDF.Salt) // validated by parse
	nonce, _ := base64.StdEncoding.DecodeString(header.Nonce)
	key, err := scrypt.Key(passphrase, salt, header.KDF.N, header.KDF.R, header.KDF.P, keySize)
	if err != nil {
		return Result{}, fmt.Errorf("derive archive key: %w", err)
	}
	aead, err := newAEAD(key)
	if err != nil {
		return Result{}, err
	}
	plain, err := aead.Open(nil, nonce, sealed, headerRaw)
	if err != nil {
		return Result{}, ErrWrongPassphrase
	}

	var p payload
	if err := json.Unmarshal(plain, &p); err != nil {
		return Result{}, fmt.Errorf("archive contents are damaged: %w", err)
	}
	keyRaw, err := base64.StdEncoding.DecodeString(p.IdentityKey)
	if err != nil {
		return Result{}, fmt.Errorf("archive identity key is damaged: %w", err)
	}
	priv, err := lpcrypto.UnmarshalPrivateKey(keyRaw)
	if err != nil {
		return Result{}, fmt.Errorf("archive identity key is unreadable: %w", err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return Result{}, fmt.Errorf("archive identity key has no peer ID: %w", err)
	}
	if pid.String() != header.PeerID {
		return Result{}, errors.New("archive key does not match the peer ID its header names")
	}

	var trustRaw []byte
	pairings := 0
	if p.TrustedPeers != "" {
		trustRaw, err = base64.StdEncoding.DecodeString(p.TrustedPeers)
		if err != nil {
			return Result{}, fmt.Errorf("archive pairings are damaged: %w", err)
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(trustRaw, &entries); err != nil {
			return Result{}, fmt.Errorf("archive pairings are not a trust store: %w", err)
		}
		pairings = len(entries)
	}

	dir := resolveDir(dataDir)
	// Report and lock the absolute path: a relative --data-dir is resolved
	// against the shell that runs the import, and the daemon may run from
	// somewhere else.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	release, err := storage.AcquireDirLock(dir)
	if err != nil {
		if errors.Is(err, storage.ErrDirLocked) {
			return Result{}, fmt.Errorf("the daemon is running and owns %s\n"+
				"  stop it first, then import again; \"lantern service status\" shows how it is registered", dir)
		}
		return Result{}, fmt.Errorf("state directory: %w", err)
	}
	defer release()

	keyPath := filepath.Join(dir, paths.IdentityKeyFileName)
	if _, err := os.Stat(keyPath); err == nil {
		if !force {
			return Result{}, fmt.Errorf("an identity already exists at %s\n"+
				"  import with --force to replace it; the current files are kept as *.pre-restore-*", keyPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("inspect %s: %w", keyPath, err)
	}

	result := Result{PeerID: header.PeerID, DeviceName: header.DeviceName, Pairings: pairings, DataDir: dir}

	stamp := time.Now().UTC().Format("20060102T150405")
	for _, name := range []string{paths.IdentityKeyFileName, paths.TrustFileName} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return result, fmt.Errorf("inspect %s: %w", path, err)
		}
		moved, err := preRestorePath(path, stamp)
		if err != nil {
			return result, fmt.Errorf("keep the previous %s: %w", name, err)
		}
		if err := os.Rename(path, moved); err != nil {
			return result, fmt.Errorf("keep the previous %s: %w", name, err)
		}
		result.Replaced = true
		result.Previous = append(result.Previous, moved)
	}

	if err := storage.WritePrivateFile(keyPath, keyRaw); err != nil {
		if len(result.Previous) > 0 {
			return result, fmt.Errorf("write identity: %w (the previous files are at %s)",
				err, strings.Join(result.Previous, ", "))
		}
		return result, fmt.Errorf("write identity: %w", err)
	}
	if trustRaw != nil {
		trustPath := filepath.Join(dir, paths.TrustFileName)
		if err := storage.WritePrivateFile(trustPath, trustRaw); err != nil {
			return result, fmt.Errorf("write pairings: %w (the previous files are at %s)",
				err, strings.Join(result.Previous, ", "))
		}
	}
	return result, nil
}

// parse splits an archive and validates everything that can be checked before
// the passphrase is used. It returns the header bytes as well, because they
// are the AEAD's additional data.
func parse(archive []byte) (Header, []byte, []byte, error) {
	var zero Header
	if len(archive) < len(magicPrefix)+1+4 || string(archive[:len(magicPrefix)]) != magicPrefix {
		return zero, nil, nil, errors.New("not a lantern identity archive")
	}
	if v := archive[len(magicPrefix)]; v != formatVersion {
		return zero, nil, nil, fmt.Errorf("identity archive format %d is not supported by this lantern (it reads format %d)", v, formatVersion)
	}
	off := len(magicPrefix) + 1
	headerLen := binary.BigEndian.Uint32(archive[off : off+4])
	off += 4
	if headerLen == 0 || headerLen > maxHeaderBytes || uint64(off)+uint64(headerLen) > uint64(len(archive)) {
		return zero, nil, nil, errors.New("identity archive header is damaged")
	}
	headerRaw := archive[off : off+int(headerLen)]
	off += int(headerLen)
	var h Header
	if err := json.Unmarshal(headerRaw, &h); err != nil {
		return zero, nil, nil, fmt.Errorf("identity archive header is damaged: %w", err)
	}
	if err := h.validate(); err != nil {
		return zero, nil, nil, err
	}
	return h, headerRaw, archive[off:], nil
}

func (h Header) validate() error {
	if h.Version != formatVersion {
		return fmt.Errorf("identity archive header version %d is not supported", h.Version)
	}
	if _, err := peer.Decode(h.PeerID); err != nil {
		return fmt.Errorf("identity archive names an invalid peer ID: %w", err)
	}
	if h.Cipher != cipherName {
		return fmt.Errorf("identity archive uses unsupported cipher %q", h.Cipher)
	}
	nonce, err := base64.StdEncoding.DecodeString(h.Nonce)
	if err != nil || len(nonce) != nonceSize {
		return errors.New("identity archive nonce is damaged")
	}
	return h.KDF.validate()
}

func (k KDFParams) validate() error {
	if k.Name != kdfName {
		return fmt.Errorf("identity archive uses unsupported key derivation %q", k.Name)
	}
	// Bound every parameter before scrypt allocates anything: a file that asks
	// for 2^30 would otherwise take the importing process down with it.
	if k.N < 1<<12 || k.N > 1<<20 || k.N&(k.N-1) != 0 {
		return fmt.Errorf("identity archive scrypt cost %d is out of range", k.N)
	}
	if k.R < 1 || k.R > 32 {
		return fmt.Errorf("identity archive scrypt block size %d is out of range", k.R)
	}
	if k.P < 1 || k.P > 16 {
		return fmt.Errorf("identity archive scrypt parallelism %d is out of range", k.P)
	}
	if uint64(128)*uint64(k.N)*uint64(k.R) > 256<<20 {
		return errors.New("identity archive scrypt memory cost is out of range")
	}
	salt, err := base64.StdEncoding.DecodeString(k.Salt)
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return errors.New("identity archive salt is damaged")
	}
	return nil
}

// preRestorePath returns a name that is not taken. Two forced imports in the
// same second would otherwise overwrite the first import's backup, which
// defeats the point of keeping the previous files.
func preRestorePath(path, stamp string) (string, error) {
	for i := 1; i <= 100; i++ {
		candidate := path + ".pre-restore-" + stamp
		if i > 1 {
			candidate = fmt.Sprintf("%s.pre-restore-%s-%d", path, stamp, i)
		}
		_, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("too many previous files already kept")
}

func readTrust(dir string) (int, []byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, paths.TrustFileName))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("read pairings: %w", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return 0, nil, fmt.Errorf("pairings file %s is not valid JSON: %w", paths.TrustFileName, err)
	}
	return len(entries), raw, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("archive cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("archive cipher: %w", err)
	}
	return aead, nil
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generate random bytes: %w", err)
	}
	return b, nil
}

func resolveDir(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return paths.Data()
	}
	return dir
}
