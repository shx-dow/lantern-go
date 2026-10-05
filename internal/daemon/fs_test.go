package daemon

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTrustHandler(t *testing.T, entries ...TrustEntry) *Handler {
	t.Helper()
	store, err := NewTrustStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, err := store.Add(e.PeerID, e.Alias); err != nil {
			t.Fatal(err)
		}
	}
	d := New(nil)
	d.Trust = store
	return NewHandler(d, "self", nil, true, 0, t.TempDir())
}

func TestResolveRefByAlias(t *testing.T) {
	const id = "12D3KooWTestPeerID00000000000000000000000000000000"
	h := newTrustHandler(t, TrustEntry{PeerID: id, Alias: "laptop"})

	for _, tc := range []struct{ ref, want string }{
		{"laptop", id},
		{"Laptop", id}, // case-insensitive
		{id, id},       // peer ID passes through
	} {
		got, err := h.resolveRef(tc.ref)
		if err != nil {
			t.Fatalf("%q: %v", tc.ref, err)
		}
		if got != tc.want {
			t.Fatalf("%q: got %q want %q", tc.ref, got, tc.want)
		}
	}
}

func TestResolveRefRejectsEmpty(t *testing.T) {
	h := newTrustHandler(t)
	if _, err := h.resolveRef("   "); err == nil {
		t.Fatal("expected empty device to be rejected")
	}
}

// An unknown alias must not be silently treated as a trusted peer ID; it
// falls through to peer.Decode downstream, which rejects it. This test
// pins that the trust store does not invent a match.
func TestResolveRefUnknownAliasDoesNotMatch(t *testing.T) {
	const id = "12D3KooWTestPeerID00000000000000000000000000000000"
	h := newTrustHandler(t, TrustEntry{PeerID: id, Alias: "laptop"})
	got, err := h.resolveRef("desktop")
	if err != nil {
		t.Fatal(err)
	}
	if got != "desktop" {
		t.Fatalf("unknown alias should pass through for downstream validation, got %q", got)
	}
}

func TestReadRequiresPath(t *testing.T) {
	h := newTrustHandler(t)
	rec := httptest.NewRecorder()
	h.getRemoteRead(rec, httptest.NewRequest(http.MethodGet, "/v1/peers/laptop/read", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestReadRejectsNegativeOffset(t *testing.T) {
	h := newTrustHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/peers/laptop/read?path=/x&offset=-1", nil)
	rec := httptest.NewRecorder()
	h.getRemoteRead(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestReadRejectsNonNumericOffset(t *testing.T) {
	h := newTrustHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/peers/laptop/read?path=/x&offset=abc", nil)
	rec := httptest.NewRecorder()
	h.getRemoteRead(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestStatRequiresPath(t *testing.T) {
	h := newTrustHandler(t)
	rec := httptest.NewRecorder()
	h.getRemoteStat(rec, httptest.NewRequest(http.MethodGet, "/v1/peers/laptop/stat", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestDevicesRequiresTrustStore(t *testing.T) {
	h := NewHandler(New(nil), "self", nil, true, 0, t.TempDir())
	rec := httptest.NewRecorder()
	h.getDevices(rec, httptest.NewRequest(http.MethodGet, "/v1/devices", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 when pairing is unconfigured", rec.Code)
	}
}

func TestReadContentEncoding(t *testing.T) {
	text := []byte("name: draft\nstatus: ok\n")
	enc, content := encodeContent(text)
	if enc != "utf-8" || content != string(text) {
		t.Fatalf("text: got %q %q", enc, content)
	}

	bin := []byte{'b', 'i', 'n', 0, 1, 2, ' ', 'p', 'a', 'y'}
	enc, content = encodeContent(bin)
	if enc != "base64" {
		t.Fatalf("binary should be base64, got %q", enc)
	}
	decoded, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(bin) {
		t.Fatalf("roundtrip mismatch: %q", decoded)
	}
}

func TestDevicesResponseShape(t *testing.T) {
	h := newTrustHandler(t, TrustEntry{PeerID: "12D3KooWTest", Alias: "laptop"})
	rec := httptest.NewRecorder()
	h.getDevices(rec, httptest.NewRequest(http.MethodGet, "/v1/devices", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	var body struct {
		Devices []deviceInfo `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Devices) != 1 {
		t.Fatalf("want 1 device, got %d", len(body.Devices))
	}
	if body.Devices[0].Alias != "laptop" {
		t.Fatalf("bad alias: %q", body.Devices[0].Alias)
	}
	if !strings.Contains(rec.Body.String(), "online") {
		t.Fatal("device list should report online state")
	}
	// Reachability is opt-in, so an unprobed listing must not claim it.
	if body.Devices[0].Reachable != nil {
		t.Fatalf("unprobed listing reported reachability: %v", *body.Devices[0].Reachable)
	}
}

// ?probe=1 is the only way to ask for reachability, and an unreachable peer
// must say so with the reason rather than being silently listed as fine.
func TestDevicesProbeReportsReachability(t *testing.T) {
	h := newTrustHandler(t, TrustEntry{PeerID: "12D3KooWUnreachable", Alias: "off"})
	rec := httptest.NewRecorder()
	h.getDevices(rec, httptest.NewRequest(http.MethodGet, "/v1/devices?probe=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	var body struct {
		Devices []deviceInfo `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Devices) != 1 || body.Devices[0].Reachable == nil {
		t.Fatalf("probed listing must report reachability: %+v", body.Devices)
	}
	if *body.Devices[0].Reachable {
		t.Fatal("a peer that cannot be resolved must not be reported reachable")
	}
	if body.Devices[0].Error == "" {
		t.Fatal("an unreachable peer must say why")
	}
}
