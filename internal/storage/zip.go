package storage

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ZipDir archives src (a directory) into dstZip, storing paths relative to
// src with deflate compression. Symlinks are skipped so archives can never
// escape the source tree on extraction. Empty directories are preserved.
func ZipDir(src, dstZip string) error {
	out, err := os.OpenFile(dstZip, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, PublicFilePerm)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	zw := zip.NewWriter(out)
	err = filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			_, err := zw.CreateHeader(&zip.FileHeader{Name: rel + "/", Method: zip.Store})
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: rel, Method: zip.Deflate})
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if closeErr := zw.Close(); err == nil {
		err = closeErr
	}
	if outErr := out.Close(); err == nil {
		err = outErr
	}
	if err != nil {
		return fmt.Errorf("zip %s: %w", src, err)
	}
	return nil
}

// ZipDirToTemp archives src into <tmpdir>/<base>.zip and returns the zip
// path plus a cleanup func for the staging dir. Callers serve the zip as a
// regular single-file share; receivers get "<base>.zip".
func ZipDirToTemp(src string) (zipPath string, cleanup func(), err error) {
	base := strings.TrimSuffix(filepath.Base(filepath.Clean(src)), "/")
	if base == "" || base == "." || base == "/" {
		return "", nil, fmt.Errorf("invalid directory %q", src)
	}
	stage, err := os.MkdirTemp("", "lantern-dir-*")
	if err != nil {
		return "", nil, fmt.Errorf("stage dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(stage) }
	zipPath = filepath.Join(stage, base+".zip")
	if err := ZipDir(src, zipPath); err != nil {
		cleanup()
		return "", nil, err
	}
	return zipPath, cleanup, nil
}
