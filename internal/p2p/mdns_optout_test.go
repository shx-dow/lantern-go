package p2p

import "testing"

// WithoutMDNS must actually skip the announcement.
//
// The assertion is on whether the announcement starter was called, not on
// whether an announcement could be observed. Those are different questions: a
// runner where mDNS fails to start announces nothing either way, so a test
// watching the network passes there only by skipping, and a test watching a
// field the starter sets fails there for a reason that has nothing to do with
// the option. Neither is a test of WithoutMDNS.
//
// Nothing in this package runs in parallel, so replacing a package-level
// function for the duration of a test is safe here. If that ever changes, this
// is the test that breaks.
func TestWithoutMDNSStopsTheAnnouncement(t *testing.T) {
	calls := countMDNSStarts(t)

	if _, err := NewNode(0, []string{"none"}, nil, t.TempDir(), WithoutMDNS()); err != nil {
		t.Fatal(err)
	}
	if calls() != 0 {
		t.Errorf("the announcement starter ran %d times for a node that opted out", calls())
	}
}

// The default is unchanged: production announces exactly as it did before the
// option existed, and a test that only checked the opt-out would not notice if
// that had been broken.
func TestANodeAnnouncesByDefault(t *testing.T) {
	calls := countMDNSStarts(t)

	if _, err := NewNode(0, []string{"none"}, nil, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if calls() != 1 {
		t.Errorf("the announcement starter ran %d times for a default node, want 1", calls())
	}
}

// The option is about one node, not the process. If it applied globally, a
// suite would silently lose the discovery it depends on.
func TestWithoutMDNSAppliesToOneNodeOnly(t *testing.T) {
	calls := countMDNSStarts(t)

	if _, err := NewNode(0, []string{"none"}, nil, t.TempDir(), WithoutMDNS()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewNode(0, []string{"none"}, nil, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if calls() != 1 {
		t.Errorf("the announcement starter ran %d times for one opting out and one not, want 1", calls())
	}
}

// countMDNSStarts replaces the announcement starter for the duration of the
// test and returns a counter of how many nodes it was asked to announce.
func countMDNSStarts(t *testing.T) func() int {
	t.Helper()
	n := 0
	real := startMDNS
	startMDNS = func(*Node) { n++ }
	t.Cleanup(func() { startMDNS = real })
	return func() int { return n }
}
