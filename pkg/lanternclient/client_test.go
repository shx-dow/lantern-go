package lanternclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeDaemon() *httptest.Server {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /v1/shares", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		write(w, Record{ID: "c1", Kind: "share", Code: "c1", FileName: "a.bin", FileSize: 3, Total: 3, State: "running"})
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		write(w, Status{PeerID: "p1", DeviceName: "laptop", LANOnly: true})
	})
	mux.HandleFunc("GET /v1/peers", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"peers": []PeerInfo{{ID: "p9", Connected: true}}})
	})
	mux.HandleFunc("GET /v1/trust", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"trusted": []TrustEntry{{PeerID: "p9", Alias: "desk"}}})
	})
	mux.HandleFunc("GET /v1/files", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"files": []FileEntry{{Name: "a.txt", Size: 1}}})
	})
	mux.HandleFunc("DELETE /v1/transfers/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		write(w, map[string]string{"error": "transfer not found"})
	})
	mux.HandleFunc("DELETE /v1/transfers/c1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return httptest.NewServer(mux)
}

func TestClientRoundTrip(t *testing.T) {
	srv := fakeDaemon()
	defer srv.Close()
	c := New(srv.URL, "")

	rec, err := c.Share("/tmp/a.bin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != "c1" {
		t.Fatalf("bad record: %+v", rec)
	}
	st, peers, err := c.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if st.DeviceName != "laptop" || len(peers) != 1 || peers[0].ID != "p9" {
		t.Fatalf("bad discover: %+v %+v", st, peers)
	}
	trusted, err := c.TrustList()
	if err != nil || len(trusted) != 1 || trusted[0].Alias != "desk" {
		t.Fatalf("bad trust: %+v %v", trusted, err)
	}
	files, err := c.Files("")
	if err != nil || len(files) != 1 || files[0].Name != "a.txt" {
		t.Fatalf("bad files: %+v %v", files, err)
	}
	if err := c.Cancel("c1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Cancel("missing"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestNewFromEnv(t *testing.T) {
	t.Setenv("LANTERND_URL", "http://example:1")
	c := NewFromEnv()
	if c.base != "http://example:1" {
		t.Fatalf("bad base: %q", c.base)
	}
}
