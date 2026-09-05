package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const MaxMetadataSize = 64 * 1024

// TransferRequest is the receiver-to-sender handshake sent before any
// file bytes flow. It is intentionally small JSON so the sender can
// dispatch to the right share before switching to framed metadata.
type TransferRequest struct {
	Offset    int64  `json:"offset,omitempty"`
	Challenge []byte `json:"challenge"`
	Proof     []byte `json:"proof"`
}

// WriteMetadata writes one length-delimited JSON metadata frame.
// The frame is deliberately separate from the file stream so readers never
// need a buffering JSON decoder before consuming file bytes.
func WriteMetadata(w io.Writer, metadata any) error {
	payload, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	if len(payload) == 0 || len(payload) > MaxMetadataSize {
		return fmt.Errorf("metadata size %d is outside the allowed range", len(payload))
	}

	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := WriteFull(w, header[:]); err != nil {
		return fmt.Errorf("write metadata length: %w", err)
	}
	if err := WriteFull(w, payload); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	return nil
}

// ReadMetadata reads one length-delimited JSON metadata frame.
func ReadMetadata(r io.Reader, dst any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return fmt.Errorf("read metadata length: %w", err)
	}

	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxMetadataSize {
		return fmt.Errorf("metadata size %d is outside the allowed range", size)
	}

	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return fmt.Errorf("read metadata: %w", err)
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return fmt.Errorf("unmarshal metadata: %w", err)
	}
	return nil
}

// WriteFull writes all of p to w, guarding against short writes.
func WriteFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n < 0 || n > len(p) {
			return fmt.Errorf("invalid write count %d", n)
		}
		p = p[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
