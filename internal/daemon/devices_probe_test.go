package daemon

import "testing"

// Probing is opt-in because it opens connections. A stray or negative value
// must never turn that cost on by accident.
func TestIsTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", " true "} {
		if !isTruthy(v) {
			t.Errorf("isTruthy(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "maybe", "-1", "on"} {
		if isTruthy(v) {
			t.Errorf("isTruthy(%q) = true, want false", v)
		}
	}
}
