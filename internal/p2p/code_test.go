package p2p

import (
	"strings"
	"testing"
)

func TestValidateCodeAcceptsGeneratedCodes(t *testing.T) {
	code, err := GenerateCode()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCode(code); err != nil {
		t.Fatalf("the generator's own code %q was refused: %v", code, err)
	}
}

func TestValidateCodeRejectsOtherShapes(t *testing.T) {
	good, err := GenerateCode()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"",
		"not-a-code",
		strings.Repeat("a", CodeHexLen-1),
		strings.Repeat("a", CodeHexLen+1),
		strings.Repeat("g", CodeHexLen),
		strings.ToUpper(good), // canonical codes are lowercase; normalize first
	} {
		if err := ValidateCode(bad); err == nil {
			t.Errorf("%q was accepted, want a shape error", bad)
		}
	}
}

func TestNormalizeCodeStripsSeparators(t *testing.T) {
	const canonical = "0123456789abcdef0123456789abcdef"
	for _, in := range []string{
		canonical,
		"0123-4567-89ab-cdef-0123-4567-89ab-cdef",
		" 0123456789ABCDEF0123456789abcdef ",
		"0123 4567 89ab cdef 0123 4567 89ab cdef",
	} {
		if got := NormalizeCode(in); got != canonical {
			t.Errorf("NormalizeCode(%q) = %q, want %q", in, got, canonical)
		}
	}
}
