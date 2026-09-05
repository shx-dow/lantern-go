package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/shx-dow/lantern-go/internal/protocol"
	"golang.org/x/crypto/hkdf"
)

const (
	ChunkSize = 64 * 1024
	KeySize   = 32
	NonceSize = 12
	TagSize   = 16
)

func DeriveKey(code string) ([]byte, error) {
	return deriveKey(code, "file-encryption-key")
}

func deriveKey(code, info string) ([]byte, error) {
	r := hkdf.New(sha256.New, []byte(code), []byte("lantern-transfer-v2"), []byte(info))
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	return key, nil
}

func AuthProof(code string, challenge []byte, offset int64) ([]byte, error) {
	if len(challenge) != 32 {
		return nil, errors.New("challenge must be 32 bytes")
	}
	if offset < 0 {
		return nil, errors.New("offset must not be negative")
	}
	key, err := deriveKey(code, "request-authentication-key")
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("lantern-transfer-request-v2"))
	mac.Write(challenge)
	var encodedOffset [8]byte
	binary.BigEndian.PutUint64(encodedOffset[:], uint64(offset))
	mac.Write(encodedOffset[:])
	return mac.Sum(nil), nil
}

func VerifyAuthProof(code string, challenge []byte, offset int64, proof []byte) bool {
	want, err := AuthProof(code, challenge, offset)
	return err == nil && hmac.Equal(want, proof)
}

type EncryptedWriter struct {
	w       io.Writer
	gcm     cipher.AEAD
	buf     []byte
	scratch [4 + NonceSize + ChunkSize + TagSize]byte
}

func NewEncryptedWriter(w io.Writer, key []byte) (*EncryptedWriter, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &EncryptedWriter{
		w:   w,
		gcm: gcm,
		buf: make([]byte, 0, ChunkSize),
	}, nil
}

func (ew *EncryptedWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		space := ChunkSize - len(ew.buf)
		if space == 0 {
			if err := ew.flush(); err != nil {
				return total, err
			}
			space = ChunkSize
		}
		n := len(p)
		if n > space {
			n = space
		}
		ew.buf = append(ew.buf, p[:n]...)
		p = p[n:]
		total += n
	}
	return total, nil
}

func (ew *EncryptedWriter) flush() error {
	if len(ew.buf) == 0 {
		return nil
	}

	var nonce [NonceSize]byte
	if _, err := crand.Read(nonce[:]); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := ew.gcm.Seal(nil, nonce[:], ew.buf, nil)
	ew.buf = ew.buf[:0]

	payloadLen := len(ciphertext)
	binary.BigEndian.PutUint32(ew.scratch[:4], uint32(payloadLen))
	copy(ew.scratch[4:], nonce[:])
	copy(ew.scratch[4+NonceSize:], ciphertext)

	totalLen := 4 + NonceSize + payloadLen
	if err := protocol.WriteFull(ew.w, ew.scratch[:totalLen]); err != nil {
		return fmt.Errorf("write encrypted chunk: %w", err)
	}
	return nil
}

func (ew *EncryptedWriter) Close() error {
	return ew.flush()
}

type EncryptedReader struct {
	r      io.Reader
	gcm    cipher.AEAD
	buf    []byte
	pos    int
	header [4 + NonceSize]byte
}

func NewEncryptedReader(r io.Reader, key []byte) (*EncryptedReader, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &EncryptedReader{
		r:   r,
		gcm: gcm,
	}, nil
}

func (er *EncryptedReader) Read(p []byte) (int, error) {
	if er.pos >= len(er.buf) {
		if err := er.readChunk(); err != nil {
			return 0, err
		}
	}

	n := copy(p, er.buf[er.pos:])
	er.pos += n
	return n, nil
}

func (er *EncryptedReader) readChunk() error {
	if _, err := io.ReadFull(er.r, er.header[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return io.EOF
		}
		return fmt.Errorf("read chunk header: %w", err)
	}

	payloadLen := binary.BigEndian.Uint32(er.header[:4])
	if payloadLen > ChunkSize+TagSize {
		return errors.New("chunk too large")
	}

	var nonce [NonceSize]byte
	copy(nonce[:], er.header[4:])

	encrypted := make([]byte, payloadLen)
	if _, err := io.ReadFull(er.r, encrypted); err != nil {
		return fmt.Errorf("read chunk data: %w", err)
	}

	er.buf = er.buf[:0]
	var err error
	er.buf, err = er.gcm.Open(er.buf[:0], nonce[:], encrypted, nil)
	if err != nil {
		return fmt.Errorf("decrypt chunk: %w", err)
	}
	er.pos = 0
	return nil
}
