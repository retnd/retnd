package obs

import (
	"context"
	"strings"
	"testing"
	"time"
)

// EPIC Q, Q1.4 (#982): FR-23 catalog additions for scrubbing, recovery and
// recovery testing. Additions only; the wire names below are a contract.

func TestAssuranceEventNamesAreStable(t *testing.T) {
	for name, pair := range map[string][2]string{
		"EventRecoveryStart":                 {EventRecoveryStart, "recovery_start"},
		"EventRecoveryStage":                 {EventRecoveryStage, "recovery_stage"},
		"EventRecoveryEnd":                   {EventRecoveryEnd, "recovery_end"},
		"EventRecoveryTestStart":             {EventRecoveryTestStart, "recovery_test_start"},
		"EventRecoveryTestEnd":               {EventRecoveryTestEnd, "recovery_test_end"},
		"EventRecoveryTestValidatorFallback": {EventRecoveryTestValidatorFallback, "recovery_test_validator_fallback"},
		"ActionRecovery":                     {ActionRecovery, "recovery"},
		"ActionRecoveryTest":                 {ActionRecoveryTest, "recovery_test"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q (renaming a catalog name is a breaking change)", name, pair[0], pair[1])
		}
	}
}

// The stage sequence is one list, so a CLI --follow and a browser follow
// cannot report different names or a different order.
func TestRecoveryStagesAreTheNamedSequenceInOrder(t *testing.T) {
	want := []string{
		"Resolving copy", "Restoring archive", "Reading", "Verifying", "Decrypting",
		"Writing target", "Validating", "Cleaning temporary files", "Complete",
	}
	if len(RecoveryStages) != len(want) {
		t.Fatalf("%d stages, want %d", len(RecoveryStages), len(want))
	}
	ids := map[string]bool{}
	for i, s := range RecoveryStages {
		if s.Label != want[i] {
			t.Errorf("stage %d label = %q, want %q", i, s.Label, want[i])
		}
		if s.ID == "" || s.ID != strings.ToLower(s.ID) || strings.Contains(s.ID, " ") || ids[s.ID] {
			t.Errorf("stage %d id %q must be unique lower_snake_case", i, s.ID)
		}
		ids[s.ID] = true
	}
}

func TestEveryCompletionStatesAResultAndPairsWithItsStart(t *testing.T) {
	l, buf := newRecorder(t)
	ctx := context.Background()

	l.RecoveryStart(ctx, "op1", "prod/db", "prod/db/a.dump")
	l.RecoveryStage(ctx, "op1", "prod/db", "reading")
	l.RecoveryEnd(ctx, "op1", "prod/db", "prod/db/a.dump", 12, time.Second, ResultError, "no readable copy")
	l.RecoveryTestStart(ctx, "t1", "prod/db", "prod/db/a.dump", "pg-restore-check")
	l.RecoveryTestEnd(ctx, "t1", "prod/db", "prod/db/a.dump", "pg-restore-check", time.Second, ResultWarn, "validator failed")

	lines := decodeLines(t, buf)
	if len(lines) != 5 {
		t.Fatalf("%d lines, want 5", len(lines))
	}
	pairs := map[string]map[string]bool{}
	for _, ln := range lines {
		ev, _ := ln["event"].(string)
		id, _ := ln["action_id"].(string)
		res, _ := ln["result"].(string)
		isEnd := strings.HasSuffix(ev, "_end")
		if isEnd && res == "" {
			t.Errorf("%s states no result", ev)
		}
		if !isEnd && res != "" {
			t.Errorf("%s is a start or note but states %q", ev, res)
		}
		if ev == EventRecoveryStage {
			if id != "op1" || ln["action"] != ActionRecovery || ln["stage"] != "reading" || ln["stage_label"] != "Reading" {
				t.Errorf("stage line = %v", ln)
			}
			continue
		}
		if pairs[id] == nil {
			pairs[id] = map[string]bool{}
		}
		pairs[id][ev] = true
	}
	for id, evs := range pairs {
		if len(evs) != 2 {
			t.Errorf("action %s has %v, want a start and an end", id, evs)
		}
	}
}

// A validator id is a name. Anything with a separator or control byte is
// withheld rather than echoed into the feed.
func TestRecoveryTestEventsNeverCarryAPathOrControlBytes(t *testing.T) {
	l, buf := newRecorder(t)
	l.RecoveryTestStart(context.Background(), "t1", "prod/db", "prod/db/a", "/usr/bin/evil\x1b[2J")
	out := buf.String()
	if strings.Contains(out, "evil") || strings.Contains(out, `\u001b`) {
		t.Errorf("validator reference leaked into the line: %s", out)
	}
}

// Review finding: a reason reaches a terminal, so control bytes are stripped
// and the length is bounded before it is logged.
func TestFeedReasonsCarryNoControlBytesAndAreBounded(t *testing.T) {
	l, buf := newRecorder(t)
	l.RecoveryEnd(context.Background(), "r", "prod/db", "prod/db/a", 0, time.Second, ResultError, "bad\x1b]0;pwned\x07 news\n"+strings.Repeat("x", 5000))
	l.RecoveryEnd(context.Background(), "o", "prod/db", "prod/db/a", 0, time.Second, ResultError, "\x1b[2Jboom")
	for _, ln := range decodeLines(t, buf) {
		reason, _ := ln["reason"].(string)
		if strings.ContainsAny(reason, "\x1b\x07\n") || len(reason) > 300 {
			t.Errorf("reason %q is not bounded plain text", reason)
		}
	}
}

// #1111: the reserved identity of the built-in check is a bare name, so the
// feed carries it and the UI can word the line from it.
func TestIssue1111_TheBuiltInIdentityReachesTheFeedUnwithheld(t *testing.T) {
	if got := validatorRef("retnd-builtin-structure"); got != "retnd-builtin-structure" {
		t.Errorf("validatorRef = %q", got)
	}
}
