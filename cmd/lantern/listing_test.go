package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// A listing where "archive" and "notes.md" look alike is a listing that gets
// people to ask `get` for a directory, which copies files only. The slash is
// the only signal a terminal has.
func TestRenderListingMarksDirectories(t *testing.T) {
	var buf bytes.Buffer
	renderListing(&buf, []lanternclient.FileEntry{
		{Name: "notes.md", Size: 12, ModTime: "2026-11-15T10:00:00Z"},
		{Name: "archive", Size: 0, ModTime: "2026-11-15T10:00:00Z", IsDir: true},
	})
	out := buf.String()

	if !strings.Contains(out, "notes.md 12 2026-11-15T10:00:00Z") {
		t.Errorf("a file lost its size:\n%s", out)
	}
	if !strings.Contains(out, "archive/ - 2026-11-15T10:00:00Z") {
		t.Errorf("a directory is not marked or still shows a size:\n%s", out)
	}
	if strings.Contains(out, "archive 0") {
		t.Errorf("a zero size reads as an empty file:\n%s", out)
	}
}

// The roots are absolute paths, not names; a size and timestamp beside them
// were always zeros and noise.
func TestRenderRootsPrintsPlainPaths(t *testing.T) {
	var buf bytes.Buffer
	renderRoots(&buf, []lanternclient.FileEntry{
		{Name: "/home/me/Share", ModTime: "2026-11-15T10:00:00Z", IsDir: true},
		{Name: "/srv/notes", ModTime: "2026-11-15T10:00:00Z", IsDir: true},
	})
	if got, want := buf.String(), "/home/me/Share\n/srv/notes\n"; got != want {
		t.Errorf("roots rendered as %q, want %q", got, want)
	}
}

// The command decides between "roots" and "one directory" the same way the
// daemon does, or an empty answer would be explained as the wrong thing.
func TestListsRootsMatchesTheDaemon(t *testing.T) {
	cases := []struct {
		dir  string
		want bool
	}{
		{"", true},
		{".", true},
		{"./", true},
		{"  ", true},
		{"archive", false},
		{"./archive", false},
	}
	for _, c := range cases {
		if got := listsRoots(c.dir); got != c.want {
			t.Errorf("listsRoots(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
}

// The empty answer has to name what was asked for: a missing root and an
// empty directory are different problems.
func TestFilesExplainsAnEmptyAnswerByName(t *testing.T) {
	d := newEmptyDaemon(t, `{"files":[]}`)
	out, stderr, code := drive(t, d, "files")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(out, "no shared roots") || !strings.Contains(out, "--shared-dirs") {
		t.Errorf("a daemon with no roots does not say so:\n%s", out)
	}

	out, stderr, code = drive(t, d, "files", "archive")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(out, "no files in archive") {
		t.Errorf("an empty directory is reported as no configuration:\n%s", out)
	}
}

func TestRemoteFilesExplainsAnEmptyAnswerByName(t *testing.T) {
	d := newEmptyDaemon(t, `{"files":[]}`)
	out, stderr, code := drive(t, d, "remote-files", "nas")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(out, "no shared roots") {
		t.Errorf("a peer with no roots does not say so:\n%s", out)
	}

	out, stderr, code = drive(t, d, "remote-files", "nas", "archive")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(out, "no files in archive") {
		t.Errorf("an empty remote directory is reported wrongly:\n%s", out)
	}
}
