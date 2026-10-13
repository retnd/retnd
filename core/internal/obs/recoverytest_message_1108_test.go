package obs

import (
	"context"
	"strings"
	"testing"
)

// #1108: the engine's closing line for a recovery test said "recovery test
// complete" whatever the state, so `state=not_configured` read as a pass one
// line after the line that said the test did not complete. The words follow
// the state now; the fields do not change.
func TestIssue1108_TheClosingLineOfARecoveryTestIsWordedByItsState(t *testing.T) {
	for state, want := range map[string]string{
		"passed":             "recovery test passed",
		"failed":             "recovery test failed",
		"not_configured":     "recovery test not run",
		"never_run":          "recovery test not run",
		"could_not_complete": "recovery test did not complete",
		"refused_for_cost":   "recovery test did not complete",
	} {
		t.Run(state, func(t *testing.T) {
			l, buf := newRecorder(t)
			l.RecoveryTest(context.Background(), RecoveryTestFacts{Artifact: "api-server/files/a.tar.zst", Medium: "local", State: state, DigestMatched: "not_compared"})
			line := decodeLines(t, buf)[0]
			msg, _ := line["msg"].(string)
			if msg != want {
				t.Errorf("message = %q, want %q", msg, want)
			}
			if state != "passed" && strings.Contains(msg, "complete") && !strings.Contains(msg, "not complete") {
				t.Errorf("state %s says %q, which reads as a pass", state, msg)
			}
			if line["state"] != state {
				t.Errorf("state field = %v, want %q (the fields must not move)", line["state"], state)
			}
			if line["event"] != EventRecoveryTest {
				t.Errorf("event = %v, want %q", line["event"], EventRecoveryTest)
			}
		})
	}
}
