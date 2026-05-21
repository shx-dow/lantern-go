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

func TestEncryptResume(t *testing.T) {
	key, err := DeriveKey("resume1")
	if err != nil {
		t.Fatal(err)
	}

	plaintext := make([]byte, 150*1024)
	for i := range plaintext {
		plaintext[i] = byte(i % 256)
	}

	var first bytes.Buffer
	w, err := NewEncryptedWriterAt(&first, key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plaintext[:100*1024]); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	startChunk := int64(100 * 1024 / ChunkSize)
	var second bytes.Buffer
	w2, err := NewEncryptedWriterAt(&second, key, startChunk)
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
	r, err := NewEncryptedReaderAt(combined, key, 0)
	if err != nil {
		t.Fatal(err)
	}
	result, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(plaintext, result) {
		t.Fatal("resume round-trip failed")
	}
}
