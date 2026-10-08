package storage

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractPerm is the set of modes a pushed tree is written with. It is passed
// in rather than hardcoded, so an expanding archive follows the same rule a
// single-file push does: permissions come from the directory the push lands in,
// never from the sender and never from a public default.
type ExtractPerm struct {
	Dir  os.FileMode
	File os.FileMode
}

// Caps on what one archive may create. A zip entry declares its uncompressed
// size, so a small archive can claim to expand to anything; without these an
// accepted push could fill the disk. Both are checked from the declared sizes
// before anything is written, so a refused archive costs nothing.
const (
	// MaxExtractBytes bounds the total uncompressed size of one archive.
	MaxExtractBytes = 32 << 30 // 32 GiB
	// MaxExtractEntries bounds how many files and directories one archive may
	// create, which bounds inode use independently of byte count.
	MaxExtractEntries = 1 << 20
)

// UnpackZip expands srcZip into dstDir.
//
// Every entry name is checked before anything is created, and each entry's
// resolved destination must sit inside dstDir. An archive is remote input — a
// peer chose every name, mode and size in it — so this function is what stands
// between a crafted archive and the receiving device's filesystem.
//
// Refused outright, rather than skipped:
//
//   - absolute names, and names that resolve outside dstDir (zip slip)
//   - names with a volume or drive component, which resolve differently on
//     Windows than they parse here
//   - symbolic links, including Windows reparse points: a pushed archive must
//     not be able to plant a link that later redirects a write
//   - anything that is neither a regular file nor a directory
//   - archives claiming more than MaxExtractBytes or MaxExtractEntries
//
// dstDir must not already exist. Callers get atomicity by expanding into a
// temporary directory alongside the destination and renaming that into place:
// renaming a directory over an existing one is not portable, and this way a
// half-expanded tree is never visible at the destination at all.
func UnpackZip(srcZip, dstDir string, perm ExtractPerm) error {
	zr, err := zip.OpenReader(srcZip)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer zr.Close()

	if err := checkArchiveLimits(zr.File, MaxExtractBytes, MaxExtractEntries); err != nil {
		return err
	}

	// Validate every name, mode and declared size first. Expanding as we go
	// would leave a partial tree behind for an archive refused three entries
	// later, and would let a declared-size bomb write until the disk filled.
	targets := make([]string, len(zr.File))
	for i, f := range zr.File {
		target, err := safeJoin(dstDir, f.Name)
		if err != nil {
			return fmt.Errorf("archive entry %q: %w", f.Name, err)
		}
		mode := f.Mode()
		switch {
		case mode&os.ModeSymlink != 0:
			// A zip records a symlink in the external attributes; a Windows
			// reparse point arrives through the mode bits the same way.
			return fmt.Errorf("archive entry %q is a symbolic link, which is not accepted", f.Name)
		case mode.IsDir():
		case mode.IsRegular():
		default:
			return fmt.Errorf("archive entry %q is neither a file nor a directory", f.Name)
		}
		targets[i] = target
	}

	if err := os.MkdirAll(dstDir, perm.Dir); err != nil {
		return fmt.Errorf("create destination: %w", err)
	}

	for i, f := range zr.File {
		if err := extractEntry(f, targets[i], perm); err != nil {
			return fmt.Errorf("archive entry %q: %w", f.Name, err)
		}
	}
	return nil
}

// checkArchiveLimits refuses archives whose declared expansion could exhaust
// disk space or inodes. Compare each uint64 size against the remaining budget
// before converting it to int64, so a zip64 size cannot overflow the sum.
func checkArchiveLimits(files []*zip.File, maxBytes int64, maxEntries int) error {
	if len(files) > maxEntries {
		return fmt.Errorf("archive holds %d entries, over the %d limit", len(files), maxEntries)
	}
	remaining := uint64(maxBytes)
	for _, f := range files {
		if !f.Mode().IsRegular() {
			continue
		}
		if f.UncompressedSize64 > remaining {
			return fmt.Errorf("archive expands to more than the %d byte limit", maxBytes)
		}
		remaining -= f.UncompressedSize64
	}
	return nil
}

func extractEntry(f *zip.File, target string, perm ExtractPerm) error {
	if f.Mode().IsDir() {
		return os.MkdirAll(target, perm.Dir)
	}
	if err := os.MkdirAll(filepath.Dir(target), perm.Dir); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	// O_EXCL so an archive cannot replace something that already exists by
	// name. Whether replacing is allowed is the caller's decision to make from
	// the operator's policy, never something the archive can ask for.
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm.File)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("destination already exists; refusing to replace it")
		}
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		_ = os.Remove(target)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(target)
		return err
	}
	// Honour the executable bit the archive recorded, but only the owner's,
	// and only as an addition. Taking the recorded mode wholesale would let a
	// peer hand itself a world-writable script; taking nothing would quietly
	// strip the bit off a script the owner meant to run.
	if f.Mode().Perm()&0o100 != 0 {
		_ = os.Chmod(target, perm.File|0o100)
	}
	return nil
}

// safeJoin resolves an archive entry name inside root, or reports why it
// cannot. It is deliberately stricter than filepath.Join, which silently
// cleans ".." segments away and would turn an escape into a harmless-looking
// in-tree path.
func safeJoin(root, name string) (string, error) {
	if name == "" {
		return "", errors.New("empty name")
	}
	// A zip uses forward slashes regardless of platform, so a backslash in a
	// name is either an escape attempt or a Windows path. Refuse both rather
	// than guess which the sender meant.
	if strings.Contains(name, `\`) {
		return "", errors.New("name contains a backslash")
	}
	// A leading slash, or a Windows volume prefix, is absolute.
	if strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		return "", errors.New("name is an absolute path")
	}
	if len(name) > 1 && name[1] == ':' {
		return "", errors.New("name carries a volume component")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", errors.New("name escapes the destination")
		}
	}
	joined := filepath.Join(root, filepath.FromSlash(name))
	// Belt and braces. On every platform tested, the name checks above are
	// what actually refuse an escape, so this does not currently carry the
	// load alone — it exists because path semantics differ between platforms
	// and string parsing, and a shared string prefix must never pass as
	// containment. TestWithinRejectsSharedPrefix is the case it exists for.
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if !within(absJoined, absRoot) {
		return "", errors.New("name escapes the destination")
	}
	return joined, nil
}

// within reports whether path is root or sits beneath it, comparing root plus a
// separator so a shared string prefix cannot pass as containment.
func within(path, root string) bool {
	if path == root {
		return true
	}
	sep := string(os.PathSeparator)
	if strings.HasSuffix(root, sep) {
		return len(path) > len(root) && strings.EqualFold(path[:len(root)], root)
	}
	if len(path) <= len(root)+len(sep) {
		return false
	}
	return strings.EqualFold(path[:len(root)+len(sep)], root+sep)
}
