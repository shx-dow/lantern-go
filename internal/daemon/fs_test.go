package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func newTrustHandler(t *testing.T, entries ...TrustEntry) *Handler {
	t.Helper()
	store, err := NewTrustStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, err := store.Add(TrustSpec{PeerID: e.PeerID, Alias: e.Alias}); err != nil {
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

// newPeerID returns a syntactically valid peer ID that no test has paired.
func newPeerID(t *testing.T) string {
	t.Helper()
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

// A valid peer ID does not have to be paired locally. Push is gated by the
// receiver's trust and pairings are one-directional, so a sender that has
// paired nothing addresses the receiver by raw peer ID. #73 rejected every
// unpaired ref, which made that push a local 404 that never dialled.
func TestResolveRefAcceptsUnpairedPeerID(t *testing.T) {
	h := newTrustHandler(t, TrustEntry{PeerID: refTestPeerID, Alias: "laptop"})
	unpaired := newPeerID(t)

	got, err := h.resolveRef(unpaired)
	if err != nil {
		t.Fatalf("an unpaired but valid peer ID must resolve: %v", err)
	}
	if got != unpaired {
		t.Fatalf("resolveRef(%q) = %q, want the ID unchanged", unpaired, got)
	}
}

func TestResolveRefRejectsEmpty(t *testing.T) {
	h := newTrustHandler(t)
	if _, err := h.resolveRef("   "); err == nil {
		t.Fatal("expected empty device to be rejected")
	}
}

// An unknown alias is not a peer ID. It used to be passed downstream, where
// peer.Decode answered with a parse error about the syntax; the useful answer
// names the device the caller typed and points at the pairing list.
func TestResolveRefUnknownAliasNamesTheCause(t *testing.T) {
	const id = "12D3KooWTestPeerID00000000000000000000000000000000"
	h := newTrustHandler(t, TrustEntry{PeerID: id, Alias: "laptop"})
	_, err := h.resolveRef("desktop")
	if err == nil {
		t.Fatal("an unpaired name must not resolve")
	}
	if !strings.Contains(err.Error(), `no paired device named "desktop"`) {
		t.Fatalf("error does not name the device: %v", err)
	}
	if !strings.Contains(err.Error(), "lantern trust list") {
		t.Fatalf("error does not point at the pairing list: %v", err)
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

// TestDevicesWithoutProbeDoesNotDial is the negative control for the probe
// gate. getDevices used to start one ProbeDevice goroutine per paired device
// whether or not ?probe was set: only the wait and the use of the results were
// behind `if probe`. Listing devices therefore dialed every peer, the
// connection Lantern promises not to open merely to learn who is there.
func TestDevicesWithoutProbeDoesNotDial(t *testing.T) {
	h := newTrustHandler(t, TrustEntry{PeerID: "12D3KooWDialMe", Alias: "laptop"})

	dialed := make(chan string, 1)
	release := make(chan struct{})
	defer close(release)
	h.probeDevice = func(_ context.Context, ref string) error {
		select {
		case dialed <- ref:
		default:
		}
		<-release
		return errors.New("unreachable")
	}

	rec := httptest.NewRecorder()
	h.getDevices(rec, httptest.NewRequest(http.MethodGet, "/v1/devices", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}

	// The old code starts its dials in goroutines it never waits for, so give
	// a stray one time to announce itself rather than racing the assertion.
	select {
	case ref := <-dialed:
		t.Fatalf("listing devices without ?probe dialed %q", ref)
	case <-time.After(time.Second):
	}
}

// ?probe=1 must still dial each device exactly once. This is a regression net
// for the gate: it passes before and after, and would fail if the fix silently
// stopped probing at all.
func TestDevicesWithProbeDials(t *testing.T) {
	h := newTrustHandler(t, TrustEntry{PeerID: "12D3KooWDialMe", Alias: "laptop"})

	var (
		mu   sync.Mutex
		refs []string
	)
	h.probeDevice = func(_ context.Context, ref string) error {
		mu.Lock()
		refs = append(refs, ref)
		mu.Unlock()
		return nil
	}

	rec := httptest.NewRecorder()
	h.getDevices(rec, httptest.NewRequest(http.MethodGet, "/v1/devices?probe=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	mu.Lock()
	got := append([]string(nil), refs...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "12D3KooWDialMe" {
		t.Fatalf("probe calls = %v, want exactly the paired device", got)
	}
	var body struct {
		Devices []deviceInfo `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Devices) != 1 || body.Devices[0].Reachable == nil || !*body.Devices[0].Reachable {
		t.Fatalf("probed listing must report the probe's verdict: %+v", body.Devices)
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
