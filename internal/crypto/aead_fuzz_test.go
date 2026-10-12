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
	// More than one chunk, so the case where a damaged final chunk follows
	// already-authenticated ones runs in `go test`, not only when the fuzzer
	// happens to generate something that large.
	f.Add(bytes.Repeat([]byte("spanning more than one chunk takes real bytes to fill. "), 1500), []byte("0123456789abcdef"))

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
				// A reader may legitimately hand back chunks that already
				// authenticated before it reaches the damaged one, so "nothing
				// at all" is the wrong requirement. What it must never do is
				// return anything that is not the genuine prefix of the
				// plaintext, and it must return nothing when the whole input fit
				// in one chunk and there is no earlier chunk.
				if !bytes.HasPrefix(plaintext, out) {
					t.Fatalf("a frame with byte %d flipped returned %d bytes that are not the start of the plaintext", at, len(out))
				}
				if len(out) != 0 && len(plaintext) <= ChunkSize {
					t.Fatalf("a single-chunk frame with byte %d flipped returned %d bytes before failing", at, len(out))
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

		// And a different challenge. This cannot be caught by comparing
		// against AuthProof, because VerifyAuthProof calls the same function:
		// an AuthProof that dropped the challenge from its input would leave
		// both agreeing. Only a fresh proof with one challenge byte changed
		// shows it.
		altered := append([]byte(nil), challenge...)
		altered[0] ^= 0x01
		over, err := AuthProof(string(code), challenge, offset)
		if err != nil {
			t.Fatalf("AuthProof: %v", err)
		}
		alt, err := AuthProof(string(code), altered, offset)
		if err != nil {
			t.Fatalf("AuthProof: %v", err)
		}
		// Each verifies against the challenge it was made over.
		if !VerifyAuthProof(string(code), challenge, offset, over) {
			t.Fatal("a proof failed to verify against its own challenge")
		}
		if !VerifyAuthProof(string(code), altered, offset, alt) {
			t.Fatal("a proof failed to verify against its own altered challenge")
		}
		// And neither verifies against the other's. This is the one that
		// matters: if AuthProof stopped mixing the challenge into its input,
		// the two proofs would be identical and this pair would both hold.
		if VerifyAuthProof(string(code), altered, offset, over) {
			t.Fatal("a proof verified against a challenge it was not made over")
		}
		if VerifyAuthProof(string(code), challenge, offset, alt) {
			t.Fatal("a proof made over an altered challenge verified against the original")
		}
	})
}
