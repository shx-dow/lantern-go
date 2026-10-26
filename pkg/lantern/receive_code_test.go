package lantern

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestReceiveRejectsMalformedCodeBeforeDiscovery is the negative control for
// share-code validation. A code that is not 32 hex characters cannot name a
// share, but receive used to search for it until discovery timed out (~30s)
// and then report "no peers found", as if the sender were merely offline.
//
// The deadline is what makes the pre-fix failure visible without waiting 30s:
// before the fix the context expires, after it the code is refused with the
// context still live.
func TestReceiveRejectsMalformedCodeBeforeDiscovery(t *testing.T) {
	ln, err := New(Config{DataDir: t.TempDir(), NoMDNS: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err = ln.ReceiveSession(ctx, "not-a-code", t.TempDir())
	if err == nil {
		t.Fatal("a malformed share code was accepted")
	}
	if ctx.Err() != nil {
		t.Fatalf("the code was not refused before discovery timed out: %v", err)
	}
	if !strings.Contains(err.Error(), "32 hex characters") {
		t.Fatalf("error does not say the expected shape: %v", err)
	}
}
