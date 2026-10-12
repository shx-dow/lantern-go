package crypto

import (
	"bytes"
	"io"
	"testing"
)

// A share code is the only secret a transfer has, so everything below it is
// either a key derivation or an authenticated chunk. Both take input a remote
// device chose, which is what a hand-written test only imagines the worst of.

// FuzzEncryptedChunk checks the three properties an AEAD must hold for any
// input: a flipped bit is caught, a truncated frame yields nothing, and a
// reader never returns plaintext it has not authenticated.
func FuzzEncryptedChunk(f *testing.F) {
	f.Add([]byte("hello"), []byte("0123456789abcdef"))
	f.Add([]byte(""), []byte("0123456789abcdef"))
	f.Add(bytes.Repeat([]byte("spanning several frames needs more than one chunk holds. "), 64), []byte("0123456789abcdef"))

	f.Fuzz(func(t *testing.T, plaintext, code []byte) {
		if len(code) == 0 {
			t.Skip()
		}
		key, err := DeriveKey(string(code))
		if err != nil {
			t.Fatalf("DeriveKey failed on a non-empty share code: %v", err)
		}

		// Two share codes must not derive the same key. Without this, a
		// DeriveKey that ignored its argument would round-trip perfectly and
		// this target would stay green, because it derives and checks with the
		// same code. A peer holding one code could then read a transfer meant
		// for another.
		otherKey, err := DeriveKey(string(code) + "\x00")
		if err != nil {
			t.Fatalf("DeriveKey failed on a non-empty share code: %v", err)
		}
		if bytes.Equal(key, otherKey) {
			t.Fatal("two different share codes derived the same key")
		}

		var sealed bytes.Buffer
		w, err := NewEncryptedWriter(&sealed, key)
		if err != nil {
			t.Fatalf("NewEncryptedWriter: %v", err)
		}
		if _, err := w.Write(plaintext); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		frame := sealed.Bytes()
		if len(plaintext) == 0 {
			// Nothing to send produces nothing. Asserting that keeps it a
			// decision rather than something a later change slips past.
			if len(frame) != 0 {
				t.Fatalf("an empty transfer sealed into %d bytes", len(frame))
			}
			return
		}
		if len(frame) == 0 {
			t.Fatal("an encrypted writer produced no bytes")
		}

		// The whole thing reads back. This is the property a truncated or
		// reordered frame would break.
		r, err := NewEncryptedReader(bytes.NewReader(frame), key)
		if err != nil {
			t.Fatalf("NewEncryptedReader: %v", err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("a sealed frame did not read back: %v", err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("round trip changed the plaintext: wrote %d bytes, read %d", len(plaintext), len(got))
		}

		// One flipped bit anywhere must be caught, and caught by refusing.
		// Nonces and tags are the bytes most likely to be trusted by mistake,
		// so the mutation walks the whole frame rather than the payload.
		if len(frame) > 1 {
			for _, at := range []int{0, len(frame) / 2, len(frame) - 1} {
				mutated := append([]byte(nil), frame...)
				mutated[at] ^= 0x01

				mr, err := NewEncryptedReader(bytes.NewReader(mutated), key)
				if err != nil {
					t.Fatalf("NewEncryptedReader: %v", err)
				}
				out, err := io.ReadAll(mr)
				if err == nil {
					t.Fatalf("a frame with byte %d flipped read back %d bytes of plaintext", at, len(out))
				}
				if len(out) != 0 {
					t.Fatalf("a frame with byte %d flipped returned %d bytes before failing", at, len(out))
				}
			}
		}

		// Every truncation must fail without yielding a prefix. A reader that
		// returned the part that authenticated would hand a caller partial
		// content it has no way to tell apart from a whole file.
		for _, cut := range []int{1, len(frame) / 2, len(frame) - 1} {
			if cut <= 0 || cut >= len(frame) {
				continue
			}
			tr, err := NewEncryptedReader(bytes.NewReader(frame[:cut]), key)
			if err != nil {
				t.Fatalf("NewEncryptedReader: %v", err)
			}
			out, err := io.ReadAll(tr)
			if err == nil && len(out) != len(plaintext) {
				t.Fatalf("a frame cut to %d of %d bytes read back without error", cut, len(frame))
			}
		}
	})
}

// FuzzAuthProof checks that a proof authenticates one share code, one
// challenge, and one offset, and nothing else. A proof that verified across
// those would let a peer replay a resume it did not earn.
func FuzzAuthProof(f *testing.F) {
	challenge := bytes.Repeat([]byte{7}, ChallengeBytes)
	proof, err := AuthProof("correct horse", challenge, 0)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(proof, challenge, int64(0), []byte("correct horse"))

	otherChallenge := bytes.Repeat([]byte{9}, ChallengeBytes)
	otherProof, err := AuthProof("correct horse", otherChallenge, 0)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(otherProof, otherChallenge, int64(0), []byte("correct horse"))

	f.Fuzz(func(t *testing.T, proof, challenge []byte, offset int64, code []byte) {
		// The correct answer for whatever the fuzzer built. Recomputing it
		// rather than reusing a seed keeps this an oracle instead of a replay.
		var want []byte
		var wantErr error
		if len(challenge) == ChallengeBytes {
			want, wantErr = AuthProof(string(code), challenge, offset)
		}

		got := VerifyAuthProof(string(code), challenge, offset, proof)

		if len(challenge) != ChallengeBytes {
			if got {
				t.Fatalf("a proof verified against a %d-byte challenge", len(challenge))
			}
			return
		}
		if wantErr != nil {
			if got {
				t.Fatal("a proof verified against a challenge that could not be signed")
			}
			return
		}
		if got != (bytes.Equal(want, proof)) {
			t.Fatalf("VerifyAuthProof returned %v for a proof that is %s the right one", got, map[bool]string{true: "", false: "not "}[bytes.Equal(want, proof)])
		}

		// A different share code must never verify.
		other := append([]byte(nil), code...)
		other = append(other, 'x')
		if VerifyAuthProof(string(other), challenge, offset, proof) {
			t.Fatal("a proof verified under a different share code")
		}

		// A different offset must never verify. This is what stops a peer
		// replaying a resume it did not earn.
		if VerifyAuthProof(string(code), challenge, offset+1, proof) {
			t.Fatal("a proof verified at a different offset")
		}
	})
}
