package protocol

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type metadata struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func TestMetadataRoundTripDoesNotConsumeFollowingBytes(t *testing.T) {
	var wire bytes.Buffer
	want := metadata{Name: "résumé.txt", Size: 42}
	if err := WriteMetadata(&wire, want); err != nil {
		t.Fatal(err)
	}
	wire.WriteString("file payload")

	var got metadata
	if err := ReadMetadata(&wire, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	payload, err := io.ReadAll(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "file payload" {
		t.Fatalf("following bytes changed: %q", payload)
	}
}

func TestReadMetadataRejectsOversizedFrame(t *testing.T) {
	var wire bytes.Buffer
	wire.Write([]byte{0, 1, 0, 0})

	var got metadata
	if err := ReadMetadata(&wire, &got); err == nil {
		t.Fatal("expected oversized metadata error")
	}
}

func TestReadMetadataRejectsEmptyAndTruncatedHeaders(t *testing.T) {
	var got metadata
	if err := ReadMetadata(bytes.NewReader([]byte{0, 0, 0, 0}), &got); err == nil {
		t.Fatal("empty metadata frame was accepted")
	}
	if err := ReadMetadata(bytes.NewReader([]byte{0, 0}), &got); err == nil {
		t.Fatal("truncated header was accepted")
	}
	if err := ReadMetadata(bytes.NewReader(nil), &got); err == nil {
		t.Fatal("missing header was accepted")
	}
}

func TestTransferRequestRoundTrip(t *testing.T) {
	var wire bytes.Buffer
	want := TransferRequest{
		Offset:    12345,
		Challenge: bytes.Repeat([]byte{9}, 32),
		Proof:     []byte("proof-bytes"),
	}
	if err := WriteMetadata(&wire, want); err != nil {
		t.Fatal(err)
	}
	var got TransferRequest
	if err := ReadMetadata(&wire, &got); err != nil {
		t.Fatal(err)
	}
	if got.Offset != want.Offset || !bytes.Equal(got.Challenge, want.Challenge) || !bytes.Equal(got.Proof, want.Proof) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestReadMetadataRejectsTruncatedFrame(t *testing.T) {
	var got metadata
	err := ReadMetadata(bytes.NewReader([]byte{0, 0, 0, 4, '{'}), &got)
	if err == nil {
		t.Fatal("expected truncated metadata error")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want unexpected EOF", err)
	}
}

type shortWriter struct {
	buf bytes.Buffer
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.buf.WriteByte(p[0])
	return 1, nil
}

func TestWriteMetadataHandlesShortWrites(t *testing.T) {
	var wire shortWriter
	if err := WriteMetadata(&wire, metadata{Name: "x", Size: 1}); err != nil {
		t.Fatal(err)
	}

	var got metadata
	if err := ReadMetadata(&wire.buf, &got); err != nil {
		t.Fatal(err)
	}
	if got != (metadata{Name: "x", Size: 1}) {
		t.Fatalf("got %+v", got)
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWriteMetadataPropagatesWriteErrors(t *testing.T) {
	sentinel := errors.New("boom")
	if err := WriteMetadata(failingWriter{sentinel}, metadata{Name: "x"}); err == nil {
		t.Fatal("write error was swallowed")
	} else if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want %v", err, sentinel)
	}
}
