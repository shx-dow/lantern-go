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
		out, name, err := fetchInto(t, nil)
		if err != nil {
			t.Fatalf("iteration %d: a fetch that saved the file reported %v", i, err)
		}
		got, rerr := os.ReadFile(filepath.Join(out, name))
		if rerr != nil {
			t.Fatalf("iteration %d: the call reported success but the file is not there: %v", i, rerr)
		}
		if string(got) != "the bytes that must land" {
			t.Fatalf("iteration %d: the file holds %q", i, got)
		}
	}
}

// A fetch that fails on the read path reports why, and replaces nothing.
//
// Three properties, and the test that found them is why all three are here at
// once.
//
// It did not fail an assertion -- it panicked. A read-path fetch that failed
// handed its progress channel to the transfer path, and the read path had
// already closed it. Both routes wanted to close one channel.
//
// Then it stopped crashing and started lying: receive() treated a read-path
// failure the same as "read path unavailable" and handed it to a transfer, so
// the call returned success, nothing was placed, and the refusal arrived later
// on the event stream. A caller watching for the call's error never learns the
// fetch failed.
//
// Now the caller is told, in words it can act on:
//
//	read path started and failed: ... already exists; remove or rename it,
//	or fetch into a different directory
func TestAFailedFetchReportsAReasonAndReplacesNothing(t *testing.T) {
	const runs = 10
	for i := range runs {
		out, name, err := fetchInto(t, occupiedDestination)
		if err == nil {
			t.Fatalf("iteration %d: a fetch into an occupied destination reported success", i)
		}
		if strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("iteration %d: a permanent failure was reported as a cancellation: %v", i, err)
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("iteration %d: the refusal does not name the file it is about: %v", i, err)
		}

		got, rerr := os.ReadFile(filepath.Join(out, name))
		if rerr != nil {
			t.Fatalf("iteration %d: %v", i, rerr)
		}
		if string(got) != "mine" {
			t.Fatalf("iteration %d: the destination holds %q; a refused fetch must replace nothing", i, got)
		}
	}
}

// A share the sender will not serve over the read path is still the transfer
// path's turn. That is the other half of the distinction, and the change above
// must not swallow it: a device serving no roots cannot answer a resolve, so
// every fetch from it takes the transfer path. Refusing those outright would
// break fetching from any unpaired device.
func TestAShareTheSenderWillNotServeFallsBackToATransfer(t *testing.T) {
	shared := t.TempDir()
	const name = "gift.txt"
	const content = "served without the read path"
	if err := os.WriteFile(filepath.Join(shared, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// No read access granted: the sender will not serve this over the read
	// path, which is not a failure -- it is the transfer path's turn.
	p := NewPair(t)

	session, share, err := p.Sender.Lantern.ShareSession(Ctx(t), filepath.Join(shared, name))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	p.BridgeDiscovery(t, share.Code)

	out := t.TempDir()
	received, _, err := p.Receiver.Lantern.ReceiveSession(Ctx(t), share.Code, out)
	if err != nil {
		t.Fatalf("a sender that will not serve the read path must not fail the call: %v", err)
	}
	for range received.Events() {
	}
	received.Close()

	got, err := os.ReadFile(filepath.Join(out, name))
	if err != nil {
		t.Fatalf("the file did not arrive by the transfer path: %v", err)
	}
	if string(got) != content {
		t.Errorf("arrived as %q, want %q", got, content)
	}
}

// fetchInto moves one file into a destination prepared by mode, and returns
// the destination, the file name, and whatever the caller was told.
func fetchInto(t *testing.T, mode func(*testing.T, string, string)) (string, string, error) {
	t.Helper()

	shared := t.TempDir()
	const name = "notes.txt"
	const content = "the bytes that must land"
	if err := os.WriteFile(filepath.Join(shared, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewPair(t, shared)
	out := t.TempDir()
	if mode != nil {
		mode(t, out, name)
	}

	session, share, err := p.Sender.Lantern.ShareSession(Ctx(t), filepath.Join(shared, name))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	p.BridgeDiscovery(t, share.Code)

	received, _, err := p.Receiver.Lantern.ReceiveSession(Ctx(t), share.Code, out)
	if err == nil && received != nil {
		for range received.Events() {
		}
		received.Close()
	}
	return out, name, err
}

// occupiedDestination puts a file where the fetch would land. Both routes
// refuse to replace it, so the fetch fails for a reason that is still true
// tomorrow.
func occupiedDestination(t *testing.T, out, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(out, name), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
}
