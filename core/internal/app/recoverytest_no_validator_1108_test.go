package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// #1108 at the seam the browser and `retnd recovery-test --follow` both read:
// the events the service writes for a recovery test of a set that declares no
// validator. The refusal stays (not_configured, with its reason); what moves is
// the wording around it.
func TestIssue1108_ARecoveryTestOfASetWithNoValidatorSaysItWasNotRunAndNeverCompleted(t *testing.T) {
	ctx := context.Background()
	var stream bytes.Buffer
	svc, journal, bs := recoveryService(t, nil)
	svc.Logger = obs.New(&stream, obs.LevelInfo)
	seedMovableArtifact(t, ctx, journal, bs, "nv.dump", retentionTestNow.Add(-time.Hour))

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}

	// The refusal itself stays.
	if res.Result.State != state.RecoveryNotConfigured || !strings.Contains(res.Result.Reason, "no recovery-test validator is declared") {
		t.Fatalf("state = %q, reason = %q: the refusal must stay as it is", res.Result.State, res.Result.Reason)
	}

	var stages []string
	for _, ev := range eventsOf(t, &stream, obs.EventRecoveryStage) {
		stages = append(stages, ev["stage"].(string))
	}
	for _, s := range stages {
		if s == "complete" {
			t.Errorf("a recovery test that ran nothing emitted the complete stage: %v", stages)
		}
	}

	summary := eventsOf(t, &stream, obs.EventRecoveryTest)
	if len(summary) != 1 {
		t.Fatalf("recovery_test summary lines = %d, want 1\n%s", len(summary), stream.String())
	}
	if msg, _ := summary[0]["msg"].(string); msg != "recovery test not run" {
		t.Errorf("summary message = %q, want %q: \"recovery test complete\" above state=not_configured reads as a pass", msg, "recovery test not run")
	}
	if summary[0]["state"] != state.RecoveryNotConfigured {
		t.Errorf("summary state = %v: the fields must not move", summary[0]["state"])
	}

	end := eventsOf(t, &stream, obs.EventRecoveryTestEnd)
	if len(end) != 1 || end[0]["result"] != "warn" {
		t.Errorf("recovery_test_end = %v, want one warn line (the refusal stays a warning)", end)
	}
}
