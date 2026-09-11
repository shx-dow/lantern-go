package daemon

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/shx-dow/lantern-go/internal/storage"
)

// maxUploadSize caps one browser upload at 1 GiB.
const maxUploadSize = 1 << 30

// uploadResponse describes a stored upload ready to be shared.
type uploadResponse struct {
	Path     string `json:"path"`
	FileName string `json:"file_name"`
	Size     int64  `json:"size"`
}

// postUploads stores one multipart `file` under the daemon data dir so the
// browser drop zone can share files without knowing daemon-side paths.
// The caller then shares the returned path via POST /v1/shares.
func (h *Handler) postUploads(w http.ResponseWriter, r *http.Request) {
	base := h.uploadDir
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "uploads")
	if err := os.MkdirAll(dir, 0700); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("parse upload (max 1 GiB): %w", err))
		return
	}
	src, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("field 'file' is required"))
		return
	}
	defer src.Close()

	name := filepath.Base(hdr.Filename)
	if err := storage.CheckFileName(name); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// Store under the original name so shares advertise a clean
	// filename; deduplicate with a numeric suffix on collision.
	var dst *os.File
	var dstPath string
	for i := 0; ; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", name, i+1)
		}
		dstPath = filepath.Join(dir, candidate)
		var err error
		dst, err = os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			name = candidate
			break
		}
		if !os.IsExist(err) || i > 99 {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	n, err := io.Copy(dst, src)
	cerr := dst.Close()
	if err != nil {
		_ = os.Remove(dstPath)
		writeError(w, http.StatusBadRequest, fmt.Errorf("store upload: %w", err))
		return
	}
	if cerr != nil {
		_ = os.Remove(dstPath)
		writeError(w, http.StatusInternalServerError, cerr)
		return
	}

	writeJSON(w, http.StatusCreated, uploadResponse{Path: dstPath, FileName: name, Size: n})
}
