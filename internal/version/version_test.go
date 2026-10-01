package version

import (
	"strings"
	"testing"
)

func TestGetAlwaysReportsSomething(t *testing.T) {
	i := Get()
	if i.Version == "" {
		t.Error("Version must never be empty")
	}
	if i.Go == "" || i.OS == "" || i.Arch == "" {
		t.Errorf("toolchain and platform should be reported, got %+v", i)
	}
}

func TestStringIncludesVersion(t *testing.T) {
	i := Info{Version: "0.1.0", Commit: "abc123", Date: "2026-01-01T00:00:00Z", Go: "go1.25.7", OS: "linux", Arch: "amd64"}
	s := i.String()
	for _, want := range []string{"0.1.0", "abc123", "2026-01-01", "go1.25.7", "linux", "amd64"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}

func TestStringWithoutCommitIsStillReadable(t *testing.T) {
	i := Info{Version: "dev", Go: "go1.25.7", OS: "linux", Arch: "arm64"}
	s := i.String()
	if !strings.Contains(s, "dev") || strings.Contains(s, "(") {
		t.Errorf("String() = %q, want a plain line with no empty parentheses", s)
	}
}

func TestShorten(t *testing.T) {
	if got := shorten("0123456789abcdef0123"); got != "0123456789ab" {
		t.Errorf("shorten of a long revision = %q", got)
	}
	if got := shorten("abc"); got != "abc" {
		t.Errorf("shorten of a short revision = %q, want it unchanged", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
