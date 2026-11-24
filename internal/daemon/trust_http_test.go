package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// A peer ID that does not decode names a device that cannot exist. Accepting
// it produced a paired device that no dial could ever reach, which is worse
// than a refusal: the person believes the pairing worked.
func TestPostTrustRejectsAnInvalidPeerID(t *testing.T) {
	store, err := NewTrustStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := New(nil)
	d.Trust = store
	h := NewHandler(d, "self", nil, true, 0, t.TempDir())

	body := `{"peer_id":"12D3KooWNotARealPeerIdAtAll","alias":"laptop"}`
	rec := httptest.NewRecorder()
	h.postTrust(rec, httptest.NewRequest(http.MethodPost, "/v1/trust", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not a libp2p peer ID") {
		t.Errorf("refusal does not say what is wrong: %s", rec.Body.String())
	}
	if got := store.List(); len(got) != 0 {
		t.Errorf("an invalid peer ID was stored anyway: %+v", got)
	}
}

// The store is keyed by the string libp2p reports at transfer time. Another
// valid encoding of the same ID would be stored under a different key and
// then never match, so the handler stores the canonical form.
func TestPostTrustStoresTheCanonicalPeerID(t *testing.T) {
	const canonical = "QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N"
	id, err := peer.Decode(canonical)
	if err != nil {
		t.Fatalf("test fixture is not a peer ID: %v", err)
	}
	other := peer.ToCid(id).String()
	if other == canonical {
		t.Fatalf("fixture has no second encoding: %s", other)
	}

	store, err := NewTrustStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := New(nil)
	d.Trust = store
	h := NewHandler(d, "self", nil, true, 0, t.TempDir())

	body := `{"peer_id":"` + other + `","alias":"laptop"}`
	rec := httptest.NewRecorder()
	h.postTrust(rec, httptest.NewRequest(http.MethodPost, "/v1/trust", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	var entry TrustEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.PeerID != canonical {
		t.Errorf("stored peer ID = %q, want the canonical %q", entry.PeerID, canonical)
	}
	if !store.Trusted(canonical) {
		t.Errorf("the canonical ID is not the key the store can answer for")
	}
}

// An empty field has its own answer, because "paste the other device's ID"
// is unhelpful when the field arrived blank.
func TestPostTrustRejectsAnEmptyPeerID(t *testing.T) {
	store, err := NewTrustStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := New(nil)
	d.Trust = store
	h := NewHandler(d, "self", nil, true, 0, t.TempDir())

	rec := httptest.NewRecorder()
	h.postTrust(rec, httptest.NewRequest(http.MethodPost, "/v1/trust", strings.NewReader(`{"peer_id":"  ","alias":"x"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "must not be empty") {
		t.Errorf("refusal does not name the empty field: %s", rec.Body.String())
	}
}
