package p2p

import "testing"

// WithoutMDNS must actually stop the announcement.
//
// The first version of this queried the LAN over multicast and looked for an
// answer. That is the real behaviour, but it is not a test: multicast is
// unavailable on plenty of runners, so it skips exactly where it matters, and
// distinguishing which node answered a multicast packet is a parsing problem
// this file should not also be solving. It could have reported "it skipped",
// which is not the same as "it worked".
//
// So the assertion is on the thing the option controls — whether this node
// started an announcement — which needs one unexported field. That is a seam,
// and it is worth naming: it exists only so this assertion can be made without
// a network.
func TestWithoutMDNSStopsTheAnnouncement(t *testing.T) {
	announced, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer announced.Close()

	if !announced.mdnsStarted {
		t.Fatal("the default node started no announcement, so there is nothing to compare against")
	}

	quiet, err := NewNode(0, []string{"none"}, nil, t.TempDir(), WithoutMDNS())
	if err != nil {
		t.Fatal(err)
	}
	defer quiet.Close()

	if quiet.mdnsStarted {
		t.Error("a node built WithoutMDNS still started announcing, so the option did nothing")
	}
}

// The option is about one node, not the process. If it were applied globally,
// a suite would silently lose the discovery it depends on.
func TestWithoutMDNSAppliesToOneNodeOnly(t *testing.T) {
	optOut, err := NewNode(0, []string{"none"}, nil, t.TempDir(), WithoutMDNS())
	if err != nil {
		t.Fatal(err)
	}
	defer optOut.Close()

	other, err := NewNode(0, []string{"none"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	if other.mdnsStarted != true {
		t.Error("a node that did not opt out stopped announcing: the option leaked")
	}
}
