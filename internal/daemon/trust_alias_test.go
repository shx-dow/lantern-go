package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A PATCH that renames a pairing must persist the new alias. The handler used
// to assign the name to a local copy and return it, so the request looked
// successful while the store kept the old name and the next load lost the edit.
func TestPatchTrustAliasPersists(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const peerID = "12D3KooWTestPeerID00000000000000000000000000000000"
	if _, err := store.Add(TrustSpec{PeerID: peerID, Alias: "laptop"}); err != nil {
		t.Fatal(err)
	}
	d := New(nil)
	d.Trust = store
	h := NewHandler(d, "self", nil, true, 0, t.TempDir())

	req := httptest.NewRequest(http.MethodPatch, "/v1/trust/"+peerID,
		strings.NewReader(`{"alias":"nas"}`))
	req.SetPathValue("id", peerID)
	rec := httptest.NewRecorder()
	h.patchTrust(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if got, _ := store.Get(peerID); got.Alias != "nas" {
		t.Errorf("store kept alias %q after a successful rename; want %q", got.Alias, "nas")
	}
	reloaded, err := NewTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.Get(peerID); got.Alias != "nas" {
		t.Errorf("reloaded alias = %q, want %q", got.Alias, "nas")
	}
}
