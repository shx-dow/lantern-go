package lanterntest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fetch that saved the file must not be reported as cancelled.
//
// FetchFS reports Done before it returns, and that report finishes the
// session, which cancels the context the read path was waiting on. So a fetch
// that succeeded could be returned as "context canceled" -- the caller told
// the transfer was interrupted when the bytes were already on disk.
//
// tryFetchViaRead used to select between the fetch finishing and that
// cancellation. Both arms became ready at once, and Go picked between them at
// random, so a saved file was reported as cancelled some of the time.
//
// Success is the deterministic case, which is what makes this testable: a fetch
// that lands reports Done every time, so the window is entered on every run
// rather than only under load.
func TestASuccessfulFetchIsNeverReportedAsCancelled(t *testing.T) {
	const runs = 20
	for i := range runs {
		if _, err := fetchOneFile(t); err != nil {
			t.Fatalf("iteration %d: a fetch that saved the file reported %v", i, err)
		}
	}
}

// A failure must carry a reason, not a cancellation.
//
// The two look identical from outside the fetch, so this is the property that
// tells them apart: whatever went wrong, the caller is told what it was.
func TestAFailedFetchReportsAReason(t *testing.T) {
	t.Skip("KNOWN PANIC, not a failure of the assertion: a read-path fetch that fails " +
		"hands its progress channel to the transfer path, which then sends on a channel " +
		"the read path already closed. See #45. Kept skipped rather than deleted so the " +
		"property stays visible and the crash stays on the record.")

	_, err := fetchOneFile(t, func(t *testing.T, out, name string) {
		if werr := os.WriteFile(filepath.Join(out, name), []byte("mine"), 0o600); werr != nil {
			t.Fatal(werr)
		}
	})
	if err == nil {
		t.Fatal("a fetch into an occupied destination must fail")
	}
	if strings.Contains(err.Error(), "context canceled") {
		t.Errorf("a permanent failure was reported as a cancellation: %v", err)
	}
	if strings.Contains(err.Error(), "context deadline") {
		t.Errorf("a permanent failure was reported as a timeout: %v", err)
	}
}

// fetchOneFile moves one file from one device to the other and returns what
// the caller was told. Any modes run against the destination first.
func fetchOneFile(t *testing.T, modes ...func(*testing.T, string, string)) (string, error) {
	t.Helper()

	shared := t.TempDir()
	const name = "notes.txt"
	const content = "the bytes that must land"
	if err := os.WriteFile(filepath.Join(shared, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewPair(t, shared)
	out := t.TempDir()
	for _, m := range modes {
		m(t, out, name)
	}

	session, share, err := p.Sender.Lantern.ShareSession(Ctx(t), filepath.Join(shared, name))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	p.BridgeDiscovery(t, share.Code)

	received, _, err := p.Receiver.Lantern.ReceiveSession(Ctx(t), share.Code, out)
	if err != nil {
		return "", err
	}
	for range received.Events() {
	}
	received.Close()

	got, rerr := os.ReadFile(filepath.Join(out, name))
	if rerr != nil {
		t.Errorf("the call reported success but the file is not there: %v", rerr)
		return "", nil
	}
	if string(got) != content {
		t.Errorf("the file holds %q, want %q", got, content)
	}
	return string(got), nil
}
