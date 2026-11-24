package download

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/shx-dow/lantern-go/internal/daemon"
	"github.com/shx-dow/lantern-go/internal/lanterntest"
	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// The fake-daemon tests pin the download's own rules. This one proves the
// chain those rules sit on: a real paired device serves the bytes, the real
// daemon answers the read route, and the file that lands is the one the other
// device holds. Without it a change to the read envelope would only fail in
// production.
func TestFileFromARealPairedDevice(t *testing.T) {
	root := t.TempDir()
	const content = "the real thing"
	local := filepath.Join(root, "notes.md")
	if err := os.WriteFile(local, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	pair := lanterntest.NewPair(t, root)
	store, err := daemon.NewTrustStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Pairing without a tier means read, which is all a download needs.
	if _, err := store.Add(daemon.TrustSpec{PeerID: pair.Sender.ID, Alias: "laptop"}); err != nil {
		t.Fatal(err)
	}
	d := daemon.New(pair.Receiver.Lantern)
	d.Trust = store
	handler := daemon.NewHandler(d, pair.Receiver.ID, nil, true, 0, t.TempDir())
	mux := http.NewServeMux()
	handler.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "notes.md")
	res, err := File(lanterntest.Ctx(t), lanternclient.New(srv.URL, ""), "laptop", local, dest)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != dest || res.Bytes != int64(len(content)) {
		t.Fatalf("result = %+v", res)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("file holds %q, want %q", got, content)
	}
}
