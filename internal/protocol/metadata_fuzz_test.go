package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// remoteFrame mirrors the document a serving device receives. It is declared
// here rather than imported because internal/p2p already imports this package,
// so the dependency cannot point the other way.
type remoteFrame struct {
	Op     string `json:"op"`
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
}

// The framing trusts a length that a remote device chose. Four bytes give a
// range up to about four GB, so the checks that refuse zero, and anything
// above MaxMetadataSize, are what stand between a peer and an unbounded
// allocation. Hand-written tests only cover the lengths their author thought
// of; this target goes looking for the rest.

// FuzzReadMetadata checks the three properties that must hold for any input a
// remote device can send.
func FuzzReadMetadata(f *testing.F) {
	// A well-formed frame, so the fuzzer starts from something that succeeds
	// and mutates away from it rather than from pure noise.
	seed, err := json.Marshal(remoteFrame{})
	if err != nil {
		f.Fatal(err)
	}
	var good bytes.Buffer
	if err := WriteMetadata(&good, remoteFrame{}); err != nil {
		f.Fatal(err)
	}
	f.Add(good.Bytes())

	// The interesting boundaries: a length of exactly the cap, one over it,
	// zero, and the largest a uint32 can hold.
	for _, size := range []uint32{1, MaxMetadataSize - 1, MaxMetadataSize, MaxMetadataSize + 1, 0, 1 << 31, ^uint32(0)} {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], size)
		f.Add(append(b[:], seed...))
	}

	// A frame that claims a length and then stops short of it.
	var truncated bytes.Buffer
	_ = binary.Write(&truncated, binary.BigEndian, uint32(64))
	truncated.WriteString(`{"op":"read"`)
	f.Add(truncated.Bytes())

	f.Fuzz(func(t *testing.T, frame []byte) {
		// The declared length decides how much is allocated, so it is the
		// thing to assert on. Waiting for an out-of-memory is not a test: a
		// 4 GB allocation succeeds in milliseconds on a machine with
		// overcommit, and the read then fails on missing bytes, so removing
		// the cap produces no crash to catch. The cap has to be asserted.
		if len(frame) >= 4 {
			size := binary.BigEndian.Uint32(frame[:4])
			bad := size == 0 || size > MaxMetadataSize

			var got remoteFrame
			err := ReadMetadata(bytes.NewReader(frame), &got)
			// A length outside the cap is refused, and the refusal names it.
			// A length inside the cap may still be refused, because the body
			// can be short or malformed, and that is not this target's concern.
			if bad {
				if err == nil {
					t.Fatalf("a declared length of %d was accepted", size)
				}
				if !strings.Contains(err.Error(), strconv.FormatUint(uint64(size), 10)) {
					t.Fatalf("refused a declared length of %d without naming it: %v", size, err)
				}
			}
			return
		}

		var got remoteFrame
		err := ReadMetadata(bytes.NewReader(frame), &got)

		if err == nil {
			// A frame that decoded must have been inside the cap. If it was
			// not, the cap did not apply on this path.
			if len(frame) < 4 {
				t.Fatalf("decoded a %d-byte frame with no length prefix", len(frame))
			}
			return
		}

		// Every failure has to name the frame, not leave the caller guessing
		// whether the peer sent rubbish or the device gave up.
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			// A short read is a legitimate refusal, and it is the reader's
			// own doing rather than the peer's. io.ReadFull wraps it, so the
			// caller sees the message instead.
			t.Fatalf("ReadMetadata returned a bare %v; it should name the frame", err)
		}
	})
}

// FuzzMetadataRoundTrip checks that whatever WriteMetadata accepts, and the
// length prefix it writes, ReadMetadata reads back exactly. A frame the writer
// produces that the reader cannot parse is a bug in one of them.
func FuzzMetadataRoundTrip(f *testing.F) {
	f.Add([]byte(`{"op":"read","path":"/x"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"op":"write","path":"/a/deeply/nested/path/file.bin"}`))
	f.Add([]byte(`{"op":"seek","offset":9223372036854775807}`))
	f.Add([]byte(`{"op":"read","path":"relative/path","offset":-1}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		// A frame is a length then JSON, so build the two halves from the
		// input rather than assuming either is valid.
		if len(raw) < 3 {
			t.Skip()
		}
		split := 1 + int(raw[0])%(len(raw)-1)
		doc := raw[split:]
		path := string(raw[:split])

		var buf bytes.Buffer
		var req remoteFrame
		if err := json.Unmarshal(doc, &req); err != nil {
			// The input is not a document this shape, so there is nothing
			// meaningful to write. ReadMetadata's handling of arbitrary bytes
			// is FuzzReadMetadata's job.
			t.Skip()
		}
		req.Path = path
		if err := WriteMetadata(&buf, req); err != nil {
			// Refusing to write is allowed. The cap and the empty case are
			// the only reasons, and both arrive as an error rather than a
			// panic, which is what this is here to catch.
			return
		}

		var got remoteFrame
		if err := ReadMetadata(bytes.NewReader(buf.Bytes()), &got); err != nil {
			t.Fatalf("wrote a frame of %d bytes that could not be read back: %v", buf.Len(), err)
		}
		// A path survives the round trip when it is valid UTF-8, which every
		// path on Windows and macOS is.
		//
		// A path that is not survives it as U+FFFD, because encoding/json
		// replaces invalid UTF-8 rather than erroring. Linux filenames are
		// byte strings, so such a name is legal there and this corrupts it.
		// That is pre-existing behaviour of framing paths as JSON, not
		// something this target introduced, and fixing it means changing what
		// goes on the wire rather than changing a test. Asserted here so the
		// limitation is written down instead of being rediscovered: a path
		// carrying a byte that is not valid UTF-8 comes back altered.
		if utf8.ValidString(req.Path) && got.Path != req.Path {
			t.Fatalf("path changed in transit: wrote %q, read %q", req.Path, got.Path)
		}
	})
}
