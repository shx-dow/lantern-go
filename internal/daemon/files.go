package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileEntry is one local file visible to agents. Directories are listed
// with IsDir but never served as transfer payloads in v1.
type FileEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
	IsDir   bool   `json:"is_dir"`
}

const maxFileEntries = 1000

// ListSharedFiles lists one level of dir, confined to roots. dir must
// resolve inside one of roots (symlinks evaluated); empty dir lists each
// root itself. Symlink escapes and absolute breakouts are rejected.
func ListSharedFiles(roots []string, dir string) ([]FileEntry, error) {
	clean := filepath.Clean(strings.TrimSpace(dir))
	if clean == "." || clean == "" {
		return listRoots(roots)
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return nil, fmt.Errorf("resolve dir: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve dir: %w", err)
	}
	allowed := false
	for _, r := range roots {
		rabs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		rres, err := filepath.EvalSymlinks(rabs)
		if err != nil {
			// Unresolvable root (missing dir): compare against abs path.
			rres = rabs
		}
		if resolved == rres || strings.HasPrefix(resolved, rres+string(os.PathSeparator)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("dir %q is outside shared dirs", dir)
	}
	return readDir(resolved)
}

func listRoots(roots []string) ([]FileEntry, error) {
	var out []FileEntry
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		fi, err := os.Stat(abs)
		if err != nil {
			continue
		}
		out = append(out, FileEntry{Name: abs, Size: 0, ModTime: fi.ModTime().UTC().Format(time.RFC3339), IsDir: fi.IsDir()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func readDir(abs string) ([]FileEntry, error) {
	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	out := make([]FileEntry, 0, len(ents))
	for _, e := range ents {
		if len(out) >= maxFileEntries {
			break
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileEntry{Name: e.Name(), Size: fi.Size(), ModTime: fi.ModTime().UTC().Format(time.RFC3339), IsDir: e.IsDir()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
