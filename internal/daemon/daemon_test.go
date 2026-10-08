package daemon

import (
	"encoding/json"
	"testing"
)

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
	got, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"type":"progress","id":"abc","bytes":10,"total":100}`
	if string(got) != want {
		t.Fatalf("EventDTO JSON = %s, want %s", got, want)
	}
}
