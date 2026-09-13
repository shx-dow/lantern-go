package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeDaemon is a minimal /v1 stub: it checks the bearer token and serves
// canned records so the GUI client tests never need a real p2p node.
func fakeDaemon(t *testing.T, token string) *httptest.Server {
	t.Helper()
	share := Transfer{ID: "abc", Kind: "share", Code: "abc", FileName: "photo.jpg", FileSize: 10, Total: 10, State: "running"}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			w.Header().Set("WWW-Authenticate", `Bearer realm="lanternd"`)
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing or invalid daemon token"})
			return
		}
		write := func(status int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/status":
			write(http.StatusOK, Status{PeerID: "peer123", Addrs: []string{"a"}, LANOnly: true})
		case "POST /v1/shares":
			write(http.StatusCreated, share)
		case "POST /v1/fetches":
			write(http.StatusCreated, Transfer{ID: "abc", Kind: "fetch", Code: "abc", State: "running"})
		case "GET /v1/transfers":
			write(http.StatusOK, map[string]any{"transfers": []Transfer{share}})
		case "GET /v1/transfers/abc":
			write(http.StatusOK, share)
		case "DELETE /v1/transfers/abc":
			w.WriteHeader(http.StatusNoContent)
		case "GET /v1/history":
			write(http.StatusOK, map[string]any{"history": []Transfer{}})
		case "POST /v1/uploads":
			write(http.StatusCreated, Upload{Path: "/tmp/uploads/photo.jpg", FileName: "photo.jpg", Size: 10})
		case "GET /v1/peers":
			write(http.StatusOK, map[string]any{"peers": []Peer{{ID: "peer999", Connected: true}}})
		default:
			write(http.StatusNotFound, map[string]string{"error": "transfer not found"})
		}
	}))
}

func TestClientAttachesAndShares(t *testing.T) {
	srv := fakeDaemon(t, "tok")
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	st, err := c.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.PeerID != "peer123" || !st.LANOnly {
		t.Fatalf("unexpected status: %+v", st)
	}
	rec, err := c.ShareFile("/tmp/photo.jpg")
	if err != nil {
		t.Fatalf("ShareFile: %v", err)
	}
	if rec.Code != "abc" || rec.FileName != "photo.jpg" {
		t.Fatalf("unexpected share: %+v", rec)
	}
	live, err := c.ListTransfers("")
	if err != nil || len(live) != 1 {
		t.Fatalf("ListTransfers: %v %+v", live, err)
	}
	if err := c.CancelTransfer("abc"); err != nil {
		t.Fatalf("CancelTransfer: %v", err)
	}
	hist, err := c.ListHistory()
	if err != nil || hist == nil || len(hist) != 0 {
		t.Fatalf("ListHistory: %v %+v", hist, err)
	}
	up, err := c.UploadFile("photo.jpg", []byte("0123456789"))
	if err != nil || up.FileName != "photo.jpg" || up.Size != 10 {
		t.Fatalf("UploadFile: %+v %v", up, err)
	}
	peers, err := c.ListPeers()
	if err != nil || len(peers) != 1 || peers[0].ID != "peer999" {
		t.Fatalf("ListPeers: %+v %v", peers, err)
	}
}

func TestClientRejectsBadToken(t *testing.T) {
	srv := fakeDaemon(t, "tok")
	defer srv.Close()

	c := NewClient(srv.URL, "wrong")
	_, err := c.GetStatus()
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T (%v)", err, err)
	}
	if apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", apiErr.Status)
	}
}

func TestClientSurfacesDaemonErrors(t *testing.T) {
	srv := fakeDaemon(t, "tok")
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	_, err := c.GetTransfer("missing")
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != http.StatusNotFound {
		t.Fatalf("expected 404 *APIError, got %T (%v)", err, err)
	}
	if !strings.Contains(apiErr.Body, "transfer not found") {
		t.Fatalf("expected daemon message, got %q", apiErr.Body)
	}
}

func TestClientNormalizesBaseURL(t *testing.T) {
	if got := NewClient("127.0.0.1:9999/", "").base; got != "http://127.0.0.1:9999" {
		t.Fatalf("bad normalize: %q", got)
	}
	if got := NewClient("", "").base; got != defaultDaemonURL {
		t.Fatalf("bad default: %q", got)
	}
}
