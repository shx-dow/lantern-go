// Package download writes one file from a paired device to local disk.
//
// It exists so the command behind it is thin: staging, digest verification,
// inherited permissions, and publish-if-absent are one unit a test can drive,
// and none of them depend on how the destination was named on the command
// line.
//
// This is the receiving side of the read path. CONTEXT.md requires that
// nothing is placed until the digest of what arrived has been verified, so
// every chunk is checked against the digest the serving device sent, and the
// destination only appears once the whole file has arrived.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// ChunkSize is the range requested per read. The daemon caps one read at
// 8 MiB and refuses a larger request rather than clamping it, so asking for
// the cap keeps a large download to one round trip per 8 MiB.
const ChunkSize = 8 << 20

// Result describes the file that landed.
type Result struct {
	// Path is the resolved destination, after a directory destination was
	// turned into the file inside it.
	Path string
	// Bytes is how much arrived and was verified.
	Bytes int64
}

// File copies path from peer to dest.
//
// dest may be a file name, an existing directory (the remote base name is used
// inside it), or empty for the current directory. An existing destination is
// refused: a download has no overwrite request to offer, so replacing a file
// is never the answer. The parent directory must already exist; a download
// does not create the tree it lands in.
func File(ctx context.Context, c *lanternclient.Client, peer, path, dest string) (Result, error) {
	if strings.TrimSpace(path) == "" {
		return Result{}, errors.New("path on the device must not be empty")
	}
	final, err := resolveDest(dest, path)
	if err != nil {
		return Result{}, err
	}
	occupied := false
	if _, err := os.Lstat(final); err == nil {
		occupied = true
	} else if !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("check destination %s: %w", final, err)
	}
	if occupied {
		return Result{}, fmt.Errorf("%s already exists; move it aside or choose a different destination, then run the command again", final)
	}

	parent := filepath.Dir(final)
	dirInfo, err := os.Stat(parent)
	if err != nil {
		return Result{}, fmt.Errorf("destination directory %s is not available: %w", parent, err)
	}
	if !dirInfo.IsDir() {
		return Result{}, fmt.Errorf("destination %s is not a directory", parent)
	}

	// Stage in the destination directory, so publishing is a link within one
	// filesystem and the destination never names an incomplete file.
	tmp, err := os.CreateTemp(parent, ".lantern-get-*")
	if err != nil {
		return Result{}, fmt.Errorf("create a staging file in %s: %w", parent, err)
	}
	tmpName := tmp.Name()
	published := false
	defer func() {
		if published {
			return
		}
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	// Permissions are inherited from the directory the file lands in, never
	// widened and never taken from the sending device: an operator who chose
	// 0700 must not get a 0644 file because a constant won.
	if err := tmp.Chmod(inheritedFilePerm(dirInfo.Mode().Perm())); err != nil {
		return Result{}, fmt.Errorf("set permissions on the staging file: %w", err)
	}

	var written int64
	total := int64(-1)
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		chunk, err := c.Read(peer, path, written, ChunkSize)
		if err != nil {
			return Result{}, err
		}
		if chunk.SHA256 == "" {
			return Result{}, fmt.Errorf("%s: the device sent no digest, so this copy cannot be verified; upgrade Lantern on that device and try again", path)
		}
		if chunk.Offset != written {
			return Result{}, fmt.Errorf("%s: device answered for offset %d when %d was asked for", path, chunk.Offset, written)
		}
		if total < 0 {
			total = chunk.Total
		} else if chunk.Total != total {
			return Result{}, fmt.Errorf("%s: size changed from %d to %d bytes during the download; nothing was written", path, total, chunk.Total)
		}
		sum := sha256.Sum256(chunk.Data)
		if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, chunk.SHA256) {
			return Result{}, fmt.Errorf("%s: digest mismatch at offset %d: the device sent %s, the bytes arrived as %s; nothing was written", path, written, chunk.SHA256, got)
		}
		if len(chunk.Data) > 0 {
			if _, err := tmp.Write(chunk.Data); err != nil {
				return Result{}, fmt.Errorf("write %s: %w", tmpName, err)
			}
			written += int64(len(chunk.Data))
		}
		if chunk.EOF {
			if written != total {
				return Result{}, fmt.Errorf("%s: download ended at %d of %d bytes; nothing was written", path, written, total)
			}
			break
		}
		if len(chunk.Data) == 0 {
			return Result{}, fmt.Errorf("%s: device returned no bytes without saying the file ended; nothing was written", path)
		}
	}

	if err := tmp.Close(); err != nil {
		return Result{}, fmt.Errorf("finish the staging file: %w", err)
	}
	if err := publishIfAbsent(tmpName, final); err != nil {
		return Result{}, err
	}
	published = true
	return Result{Path: final, Bytes: written}, nil
}

// resolveDest turns the caller's destination into a file path.
//
// A remote path is interpreted by the device that served it, so its separator
// may not be this machine's: a peer on Windows sharing `C:\notes.md` still
// has to produce `notes.md` here. Splitting on both separators is what keeps
// the base name meaningful across platforms.
func resolveDest(dest, remotePath string) (string, error) {
	base := remoteBase(remotePath)
	if dest == "" {
		dest = "."
	}
	if info, err := os.Stat(dest); err == nil && info.IsDir() {
		if base == "" {
			return "", fmt.Errorf("cannot tell a file name from %q; pass a destination path", remotePath)
		}
		return filepath.Join(dest, base), nil
	}
	if strings.HasSuffix(dest, "/") || strings.HasSuffix(dest, string(os.PathSeparator)) {
		return "", fmt.Errorf("destination directory %s does not exist", dest)
	}
	return filepath.Clean(dest), nil
}

// remoteBase returns the last path element of a path written by the serving
// device, accepting either separator.
func remoteBase(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	if p == "." || p == ".." || p == "" {
		return ""
	}
	return p
}

// inheritedFilePerm derives a file mode from the mode of the directory the
// file lands in: the directory's bits, without execute, plus owner read. A
// 0755 directory yields 0644, a 0700 directory yields 0600.
func inheritedFilePerm(dir os.FileMode) os.FileMode {
	return dir&^0o111 | 0o400
}

// publishIfAbsent moves src to dst only if dst does not exist.
//
// os.Rename replaces dst unconditionally, so checking first and renaming second
// leaves a window in which anything that creates dst is silently overwritten.
// A hard link is the portable atomic form of "create if absent": link(2)
// either creates dst or fails with EEXIST, with no gap between. This is the
// same rule the transfer path applies to a fetched file.
func publishIfAbsent(src, dst string) error {
	err := os.Link(src, dst)
	if err == nil {
		if rmErr := os.Remove(src); rmErr != nil {
			return fmt.Errorf("file landed at %s but the staged copy %s could not be removed: %w", dst, src, rmErr)
		}
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists; move it aside or choose a different destination, then run the command again", dst)
	}
	if !linkUnsupported(err) {
		return fmt.Errorf("publish %s: %w", dst, err)
	}

	// No hard links on this filesystem. Fall back, and accept that the check
	// and the rename are no longer atomic against each other.
	if _, statErr := os.Lstat(dst); statErr == nil {
		return fmt.Errorf("%s already exists; move it aside or choose a different destination, then run the command again", dst)
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("check destination %s: %w", dst, statErr)
	}
	if rerr := os.Rename(src, dst); rerr != nil {
		return fmt.Errorf("finalize %s: %w", dst, rerr)
	}
	return nil
}

// linkUnsupported reports whether err means "this filesystem cannot make hard
// links" rather than a real failure. FAT, some network mounts, and Windows
// without the hard-link privilege refuse link(2); those fall back to a
// check-then-rename.
func linkUnsupported(err error) bool {
	return errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.EXDEV)
}
