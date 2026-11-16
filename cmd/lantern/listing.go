package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// listsRoots reports whether dir asks for the shared roots rather than one
// directory. It mirrors the daemon's own rule (clean "." or empty), so the
// rendering and the empty message always agree with what was actually listed.
func listsRoots(dir string) bool {
	return filepath.Clean(strings.TrimSpace(dir)) == "."
}

// renderListing prints one directory's entries. A directory gets a trailing
// slash and no size, because `lantern get` copies files and not directories:
// "notes" and "notes/" are different things, and this listing is the only
// place a person can tell which one they are about to ask for. A dash where
// the size goes is the same convention ls uses.
func renderListing(w io.Writer, entries []lanternclient.FileEntry) {
	for _, f := range entries {
		if f.IsDir {
			fmt.Fprintf(w, "%s/ - %s\n", f.Name, f.ModTime)
			continue
		}
		fmt.Fprintf(w, "%s %d %s\n", f.Name, f.Size, f.ModTime)
	}
}

// renderRoots prints the shared roots themselves. They are absolute paths
// rather than names inside a directory, so a size and a timestamp would be
// noise; the path is the whole answer.
func renderRoots(w io.Writer, entries []lanternclient.FileEntry) {
	for _, f := range entries {
		fmt.Fprintln(w, f.Name)
	}
}
