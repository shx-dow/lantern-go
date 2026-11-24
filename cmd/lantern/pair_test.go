package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The whole point of `lantern pair` is that the invitation is the only thing a
// person has to move. The CLI has to print it so it can be copied, and say what
// to do with it on the other side.
func TestPairPrintsAnInvitationToPaste(t *testing.T) {
	d := newFakeDaemon(t)

	stdout, stderr, code := d.runAgainst(t, "pair")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"lantern-pair1.invitation", "lantern pair <invitation>", "Single use"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("pair output is missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(strings.Join(d.called(), " "), "POST /v1/pairing/invitations") {
		t.Errorf("pair did not ask the daemon for an invitation: %v", d.called())
	}
}

// A redeemed invitation names who was paired, so the next command can use the
// alias instead of a 52-character peer ID.
func TestPairRedeemingNamesTheOtherDevice(t *testing.T) {
	d := newFakeDaemon(t)

	stdout, stderr, code := d.runAgainst(t, "pair", "lantern-pair1.invitation")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"paired with laptop", "12D3kooLAPTOP", "read", "lantern remote-files laptop"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("redeem output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestPairJSONIsOneObjectEitherWay(t *testing.T) {
	d := newFakeDaemon(t)

	stdout, stderr, code := d.runAgainst(t, "--json", "pair")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var inv map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &inv); err != nil {
		t.Fatalf("--json pair is not one JSON object: %v\n%s", err, stdout)
	}
	if inv["invitation"] == "" || inv["expires_at"] == "" {
		t.Errorf("--json pair lost a field: %v", inv)
	}

	stdout, stderr, code = d.runAgainst(t, "--json", "pair", "lantern-pair1.invitation")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("--json redeem is not one JSON object: %v\n%s", err, stdout)
	}
	if res["peer_id"] != "12D3kooLAPTOP" || res["tier"] != "read" {
		t.Errorf("--json redeem lost a field: %v", res)
	}
}

// A pasted share code, a peer ID, or two invitations is a usage problem, and it
// must be named rather than sent to the daemon as an invitation.
func TestPairRefusesExtraArguments(t *testing.T) {
	d := newFakeDaemon(t)
	_, stderr, code := d.runAgainst(t, "pair", "one", "two")
	if code == 0 {
		t.Fatal("two invitations were accepted")
	}
	if !strings.Contains(stderr, "usage: lantern pair") {
		t.Errorf("refusal does not show the usage:\n%s", stderr)
	}
}

// --ttl is a duration, so "0" or a negative value is refused by the CLI rather
// than turned into an invitation that is already dead.
func TestPairRefusesANonPositiveTTL(t *testing.T) {
	d := newFakeDaemon(t)
	for _, ttl := range []string{"-5m", "500ms"} {
		_, stderr, code := d.runAgainst(t, "pair", "--ttl", ttl)
		if code == 0 {
			t.Errorf("--ttl %s was accepted", ttl)
			continue
		}
		if !strings.Contains(stderr, "--ttl") {
			t.Errorf("--ttl %s failed with %q, which does not name the flag", ttl, stderr)
		}
	}
}
