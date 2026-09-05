package crypto

import (
	"bytes"
	"io"
	"testing"
)

func TestEncryptDecryptSmall(t *testing.T) {
	key, err := DeriveKey("a1b2c3")
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("hello world")
	var buf bytes.Buffer

	w, err := NewEncryptedWriter(&buf, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plaintext); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	t.Logf("encrypted size: %d", buf.Len())

	r, err := NewEncryptedReader(&buf, key)
	if err != nil {
		t.Fatal(err)
	}
	result, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(plaintext, result) {
		t.Fatalf("got %q, want %q", result, plaintext)
	}
}

func TestEncryptDecryptLarge(t *testing.T) {
	key, err := DeriveKey("ffeedd")
	if err != nil {
		t.Fatal(err)
	}

	plaintext := make([]byte, 200*1024)
	for i := range plaintext {
		plaintext[i] = byte(i)
	}

	var buf bytes.Buffer
	w, err := NewEncryptedWriter(&buf, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plaintext); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewEncryptedReader(&buf, key)
	if err != nil {
		t.Fatal(err)
	}
	result, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(plaintext, result) {
		t.Fatal("round-trip failed")
	}
}

// Each encrypted chunk carries a fresh random nonce, so independently
// encrypted streams concatenate and still decrypt as one stream.
func TestEncryptConcatenatedStreamsDecrypt(t *testing.T) {
	key, err := DeriveKey("resume1")
	if err != nil {
		t.Fatal(err)
	}

	plaintext := make([]byte, 150*1024)
	for i := range plaintext {
		plaintext[i] = byte(i % 256)
	}

	var first bytes.Buffer
	w, err := NewEncryptedWriter(&first, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plaintext[:100*1024]); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	var second bytes.Buffer
	w2, err := NewEncryptedWriter(&second, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Write(plaintext[100*1024:]); err != nil {
		t.Fatal(err)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}

	combined := io.MultiReader(&first, &second)
	r, err := NewEncryptedReader(combined, key)
	if err != nil {
		t.Fatal(err)
	}
	result, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(plaintext, result) {
		t.Fatal("concatenated streams round-trip failed")
	}
}

func TestEncryptingSameDataUsesFreshNonces(t *testing.T) {
	key, err := DeriveKey("nonce-test")
	if err != nil {
		t.Fatal(err)
	}

	plaintext := bytes.Repeat([]byte("same payload"), 1000)
	var first, second bytes.Buffer
	for _, out := range []*bytes.Buffer{&first, &second} {
		w, err := NewEncryptedWriter(out, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(plaintext); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("same plaintext produced identical encrypted streams")
	}
}

func TestAuthProofBindsChallengeAndOffset(t *testing.T) {
	challenge := bytes.Repeat([]byte{7}, 32)
	proof, err := AuthProof("secret", challenge, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyAuthProof("secret", challenge, 12, proof) {
		t.Fatal("valid proof was rejected")
	}
	if VerifyAuthProof("wrong", challenge, 12, proof) || VerifyAuthProof("secret", challenge, 13, proof) {
		t.Fatal("proof accepted with altered credentials")
	}
	mutated := bytes.Clone(challenge)
	mutated[0] ^= 1
	if VerifyAuthProof("secret", mutated, 12, proof) {
		t.Fatal("proof accepted with mutated challenge")
	}
	if VerifyAuthProof("secret", challenge, 12, proof[:16]) {
		t.Fatal("truncated proof was accepted")
	}
	for _, bad := range [][]byte{nil, challenge[:31], append(bytes.Clone(challenge), 0)} {
		if _, err := AuthProof("secret", bad, 12); err == nil {
			t.Fatalf("challenge of length %d was accepted", len(bad))
		}
		if VerifyAuthProof("secret", bad, 12, proof) {
			t.Fatal("proof verified with malformed challenge")
		}
	}
	if _, err := AuthProof("secret", challenge, -1); err == nil {
		t.Fatal("negative offset was accepted")
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	key, err := DeriveKey("tamper-test")
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	w, err := NewEncryptedWriter(&wire, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("secret")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	encoded := wire.Bytes()
	encoded[len(encoded)-1] ^= 1

	r, err := NewEncryptedReader(bytes.NewReader(encoded), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestDecryptRejectsTruncationAndWrongKey(t *testing.T) {
	key, err := DeriveKey("tamper-test")
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	w, err := NewEncryptedWriter(&wire, key)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("0123456789abcdef"), 8192)
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	full := wire.Bytes()

	truncated := full[:len(full)-10]
	r, err := NewEncryptedReader(bytes.NewReader(truncated), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("truncated stream was accepted")
	}

	other, err := DeriveKey("other-code")
	if err != nil {
		t.Fatal(err)
	}
	r, err = NewEncryptedReader(bytes.NewReader(full), other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("wrong-key stream was accepted")
	}
}
