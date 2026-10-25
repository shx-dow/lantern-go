package daemon

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const unpairPeerID = "12D3KooWUnpairMe000000000000000000000000000000000"

// TestUnpairFailureIsReportedAndKeepsEntry is the negative control for the
// unpair path. Remove deleted the entry in memory and ignored saveLocked's
// error, and deleteTrust returned 204 unconditionally: the caller was told the
// device was revoked while the file on disk still trusted it, so a restart
// brought the pairing back.
func TestUnpairFailureIsReportedAndKeepsEntry(t *testing.T) {
	h := newTrustHandler(t, TrustEntry{PeerID: unpairPeerID, Alias: "laptop"})
	h.daemon.Trust.save = func() error { return errors.New("disk full") }

	req := httptest.NewRequest(http.MethodDelete, "/v1/trust/laptop", nil)
	req.SetPathValue("id", "laptop")
	rec := httptest.NewRecorder()
	h.deleteTrust(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("unpair with a failed write returned %d, want 500; body: %s", rec.Code, rec.Body.String())
	}
	if _, ok := h.daemon.Trust.Get(unpairPeerID); !ok {
		t.Error("the entry was removed in memory even though the revocation was not persisted")
	}
}

// A successful unpair still reports 204, drops the entry from memory, and
// leaves nothing behind on disk. This is a regression net: it passes before and
// after, and would fail if the error path started refusing every unpair.
func TestUnpairRemovesPersistedEntry(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(TrustSpec{PeerID: unpairPeerID, Alias: "laptop"}); err != nil {
		t.Fatal(err)
	}
	d := New(nil)
	d.Trust = store
	h := NewHandler(d, "self", nil, true, 0, t.TempDir())

	req := httptest.NewRequest(http.MethodDelete, "/v1/trust/laptop", nil)
	req.SetPathValue("id", "laptop")
	rec := httptest.NewRecorder()
	h.deleteTrust(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
	if _, ok := store.Get(unpairPeerID); ok {
		t.Fatal("the entry is still in memory after a successful unpair")
	}
	reopened, err := NewTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Get(unpairPeerID); ok {
		t.Fatal("the revocation did not reach disk: a restarted daemon would trust the device again")
	}
}
