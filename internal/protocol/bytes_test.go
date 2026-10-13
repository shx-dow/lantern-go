package protocol

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf8"
)

// Base64Of and FromBase64 decide whether a name survives a hop. They are only
// reached end to end through the fs tests, which cover the paths a filename
// takes and nothing else: not the empty string, not a value that is valid
// UTF-8, not the refusal, and not which field a refusal names.

func TestBase64OfCarriesOnlyWhatPlainCannot(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // "" means no companion
	}{
		{"empty", "", ""},
		{"ascii", "report.txt", ""},
		{"valid multibyte", "café/naïve/日本語.txt", ""},
		{"latin supplement", "£100", ""},
		{"one bad byte", "report\xff.txt", base64.StdEncoding.EncodeToString([]byte("report\xff.txt"))},
		{"lone continuation byte", "\x80", "gA=="},
		{"truncated sequence", "a\xe2\x82", "YeKC"},
		{"surrogate half", "\xed\xa0\x80", "7aCA"},
		{"only bad bytes", "\xff\xfe\xfd", "//79"},
	}
	for _, c := range cases {
		if got := Base64Of(c.in); got != c.want {
			t.Errorf("%s: Base64Of(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestFromBase64PrefersTheCompanion(t *testing.T) {
	const name = "report\xff.txt"
	b64 := Base64Of(name)

	// The ordinary case: no companion, the plain value stands.
	got, err := FromBase64("plain.txt", "", "path_b64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plain.txt" {
		t.Errorf("got %q, want the plain value untouched", got)
	}

	// Both present: the companion wins, because a sender that set it knew the
	// plain value was not faithful.
	got, err = FromBase64("report�.txt", b64, "path_b64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != name {
		t.Errorf("got %q, want %q", got, name)
	}

	// Both empty is empty, not an error.
	got, err = FromBase64("", "", "path_b64")
	if err != nil || got != "" {
		t.Errorf("FromBase64(\"\", \"\") = %q, %v; want \"\", nil", got, err)
	}
}

// A refusal has to say which field a peer got wrong. The same helper decodes
// path_b64, name_b64 and dir_b64, so a message that names one of them
// unconditionally sends a reader to the wrong field most of the time.
func TestFromBase64RefusalNamesTheField(t *testing.T) {
	for _, field := range []string{"path_b64", "name_b64", "dir_b64"} {
		got, err := FromBase64("x", "!", field)
		if err == nil {
			t.Fatalf("%s: a malformed companion was accepted as %q", field, got)
		}
		if got != "" {
			t.Errorf("%s: returned %q alongside an error", field, got)
		}
		if !strings.Contains(err.Error(), field) {
			t.Errorf("%s: refusal does not name the field: %v", field, err)
		}
	}
}

// The round trip is the property that matters, and the input is arbitrary
// bytes on both sides. This is also the right shape for a fuzz target: the
// companion arrives over a connection, so it is chosen by a peer.
//
// The asymmetry is deliberate and is what stops a mangled name from decoding
// to itself. A value that is valid UTF-8 carries no companion and comes back
// as-is; a value that is not carries one and comes back byte for byte.
func FuzzBase64RoundTrip(f *testing.F) {
	f.Add("report.txt", "", "report\xff.txt")
	f.Add("report�.txt", "cmVwb3J0�4udHh0", "")
	f.Add("café/日本語", "", "")

	f.Fuzz(func(t *testing.T, plain, b64, value string) {
		// Half one: a companion that arrived on the wire decodes, or is
		// refused. It is never quietly replaced by the plain value.
		got, err := FromBase64(plain, b64, "path_b64")

		if b64 == "" {
			if err != nil {
				t.Fatalf("an absent companion must not be refused: %v", err)
			}
			if got != plain {
				t.Fatalf("an absent companion changed the value: %q became %q", plain, got)
			}
		} else if raw, decErr := base64.StdEncoding.DecodeString(b64); decErr != nil {
			if err == nil {
				t.Fatalf("a malformed companion was accepted, returning %q", got)
			}
			if got != "" {
				t.Fatalf("returned %q alongside an error", got)
			}
		} else {
			if err != nil {
				t.Fatalf("a well-formed companion was refused: %v", err)
			}
			if got != string(raw) {
				t.Fatalf("decoded %q, want %q", got, string(raw))
			}
		}

		// Half two: this build's own encoding of an arbitrary value survives
		// the pair the wire actually uses.
		//
		// The two halves are not the same check. Half one never calls
		// Base64Of, so a change to the encoder could have broken every value
		// it produces while half one stayed green. What has to hold is that
		// the value comes back whether the pair carries it in the plain field
		// or in the companion:
		//
		//   valid UTF-8    -> plain field, no companion
		//   not valid      -> companion, plain field empty
		//
		// An earlier version of this target asserted only half one, with a
		// Base64Of call inside a block that had no assertion in it at all.
		encoded := Base64Of(value)
		plainField := value
		if encoded != "" {
			plainField = ""
		}
		back, err := FromBase64(plainField, encoded, "path_b64")
		if err != nil {
			t.Fatalf("the encoder's own output was refused: %v", err)
		}
		if back != value {
			t.Fatalf("round trip of %q gave %q (plain %q, companion %q)", value, back, plainField, encoded)
		}
	})
}

// The two halves agree on when to carry a value. A companion exists only for a
// value the plain field cannot hold, so the invariant is two-sided rather than
// "Base64Of then FromBase64":
//
//	valid UTF-8    -> no companion, the plain field carries it
//	not valid      -> a companion, which decodes back byte for byte
//
// The one-sided form does not hold, and it is worth writing down that it does
// not: FromBase64("", "") is the empty string, so a value that travels in the
// plain field does not come back through the companion at all.
func TestBase64OfAndFromBase64AgreeOnWhenToCarry(t *testing.T) {
	for _, v := range []string{
		"",
		"plain",
		"café",
		"日本語のファイル.txt",
		"report\xff.txt",
		"\xff",
		"\x00\x01\x02",
		"trailing\xff",
	} {
		b64 := Base64Of(v)
		if utf8.ValidString(v) {
			if b64 != "" {
				t.Errorf("%q is valid UTF-8 but carries a companion %q", v, b64)
			}
			// Nothing to decode, and the plain value is returned untouched.
			got, err := FromBase64(v, b64, "path_b64")
			if err != nil || got != v {
				t.Errorf("%q: got %q, %v", v, got, err)
			}
			continue
		}
		if b64 == "" {
			t.Errorf("%q is not valid UTF-8 but carries no companion", v)
			continue
		}
		got, err := FromBase64("", b64, "path_b64")
		if err != nil {
			t.Errorf("%q: %v", v, err)
			continue
		}
		if got != v {
			t.Errorf("round trip of %q gave %q", v, got)
		}
	}
}
