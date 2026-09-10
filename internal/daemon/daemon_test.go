package daemon

import "testing"

func TestNormalizeCode(t *testing.T) {
	cases := map[string]string{
		"  A1B2-C3D4  ": "a1b2c3d4",
		"ABC DEF":       "abcdef",
		"9f3k":          "9f3k",
		"":              "",
	}
	for in, want := range cases {
		if got := NormalizeCode(in); got != want {
			t.Fatalf("NormalizeCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEventDTOShapes(t *testing.T) {
	e := EventDTO{Type: "progress", ID: "abc", Bytes: 10, Total: 100}
	if e.Type != "progress" || e.Bytes != 10 || e.Total != 100 {
		t.Fatal("event DTO mutated unexpectedly")
	}
}
