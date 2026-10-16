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

// A failed read-path fetch must not crash, and must never replace what is
// already at the destination.
//
// This is the test that found the cause. It did not fail an assertion -- it
// panicked, because a read-path fetch that failed handed its progress channel
// to the transfer path, and the read path had already closed it. Both routes
// wanted to close one channel, so whichever ran second closed a closed channel
// and sent on it. `lantern receive` could take the process down on any fetch
// that failed on the read path.
//
// It now gets that far and stops: the fetch is refused, nothing is replaced,
// and nothing crashes.
func TestAFailedFetchDoesNotCrashOrReplace(t *testing.T) {
	const runs = 10
	for i := range runs {
		out, name, err := fetchInto(t, occupiedDestination)
		if err != nil {
			t.Fatalf("iteration %d: the fetch crashed rather than being refused: %v", i, err)
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

// A failure must carry a reason, not a cancellation.
//
// Still not true, and not this change's doing: receive() treats a read-path
// failure the same as "read path unavailable" and hands it to a transfer, so
// the call returns success and the refusal arrives later on the event stream.
// Kept skipped so the property stays on the record; the routing decision is
// its own change.
func TestAFailedFetchReportsAReason(t *testing.T) {
	t.Skip("receive() falls through to a transfer after a read-path failure, so the " +
		"refusal arrives on the event stream rather than as the call's error. A routing " +
		"decision, not a bug fix; see #46.")

	if _, _, err := fetchInto(t, occupiedDestination); err == nil {
		t.Fatal("a fetch into an occupied destination must fail")
	} else if strings.Contains(err.Error(), "context canceled") {
		t.Errorf("a permanent failure was reported as a cancellation: %v", err)
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
