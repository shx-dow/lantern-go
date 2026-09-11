package daemon

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func decodeJSON(t *testing.T, data []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode: %v: %s", err, data)
	}
}

func testUploadHandler(dir string) *Handler {
	return &Handler{uploadDir: dir}
}

func TestPostUploads(t *testing.T) {
	dir := t.TempDir()
	h := testUploadHandler(dir)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("hello uploads")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/uploads", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	h.postUploads(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var out uploadResponse
	decodeJSON(t, rec.Body.Bytes(), &out)
	if out.FileName != "hello.txt" || out.Size != int64(len("hello uploads")) {
		t.Fatalf("bad response: %+v", out)
	}
	data, err := os.ReadFile(out.Path)
	if err != nil || string(data) != "hello uploads" {
		t.Fatalf("stored file wrong: %v %q", err, data)
	}
	if filepath.Dir(out.Path) != filepath.Join(dir, "uploads") {
		t.Fatalf("stored outside uploads dir: %s", out.Path)
	}
}

func TestPostUploadsRejects(t *testing.T) {
	h := testUploadHandler(t.TempDir())

	// Missing file field.
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("note", "no file here")
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/uploads", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	h.postUploads(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}

	// Traversal filename is neutralized to its base name inside uploads/.
	body.Reset()
	w = multipart.NewWriter(&body)
	fw, _ := w.CreateFormFile("file", "../evil.txt")
	_, _ = fw.Write([]byte("x"))
	_ = w.Close()
	req = httptest.NewRequest(http.MethodPost, "/v1/uploads", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec = httptest.NewRecorder()
	h.postUploads(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var trav uploadResponse
	decodeJSON(t, rec.Body.Bytes(), &trav)
	if trav.FileName != "evil.txt" {
		t.Fatalf("basename not enforced: %+v", trav)
	}
	if filepath.Dir(trav.Path) != filepath.Join(h.uploadDir, "uploads") {
		t.Fatalf("escaped uploads dir: %s", trav.Path)
	}
}
