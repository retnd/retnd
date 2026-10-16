package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/obs"
)

// #1200: one scrub or recovery test read back by its operation id, from the
// durable activity record and whatever of it the ring still holds. The
// Operation page and `retnd assurance show` both draw from this, so these
// pin what it says for every way a run can end.

func (r *durableRig) runLine(event, opID string, result obs.Result, fields ...obs.Field) {
	r.svc.activity.RecordEvent(obs.Record{
		At: time.Now().UTC(), Level: obs.LevelInfo, Event: event, Message: event, Result: result,
		Fields: append([]obs.Field{{Key: "op_id", Value: opID}, {Key: "backup_set", Value: "alpha/nightly"}}, fields...),
		Action: obs.ActionRecoveryTest, ActionID: opID,
	})
}

func (r *durableRig) recoveryTest(opID string, stages []string, end obs.Result, reason string) {
	r.runLine(obs.EventRecoveryTestStart, opID, "", obs.Field{Key: "artifact", Value: "alpha/nightly/a.zst"})
	for _, s := range stages {
		r.runLine(obs.EventRecoveryStage, opID, "", obs.Field{Key: "stage", Value: s}, obs.Field{Key: "stage_label", Value: obs.StageLabel(s)})
	}
	var fields []obs.Field
	if reason != "" {
		fields = append(fields, obs.Field{Key: "reason", Value: reason})
	}
	r.runLine(obs.EventRecoveryTestEnd, opID, end, fields...)
}

func eventsNamed(rec AssuranceOperationRecord) []string {
	out := make([]string, 0, len(rec.Events))
	for _, e := range rec.Events {
		out = append(out, e.Event)
	}
	return out
}

// The store-level case: the run is gone from the ring (a restart refills the
// ring from the journal, so it is cleared here to leave only the journal)
// and the record still says what it did.
func TestAFinishedRecoveryTestIsReadBackFromTheJournalByItsID(t *testing.T) {
	r := newDurableRig(t)
	r.recoveryTest("op-pass", []string{"resolving_copy", "reading", "verifying", "validating", "complete"}, obs.ResultSuccess, "")
	r.flush()
	r.svc.activity = nil

	rec, err := r.svc.AssuranceOperation(context.Background(), "op-pass")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(eventsNamed(rec), ",")
	// The journal keeps one stage line per operation, the last one.
	if want := "recovery_test_start,recovery_stage,recovery_test_end"; got != want {
		t.Fatalf("lines = %s, want %s", got, want)
	}
	if rec.Kind != "recovery_test" || rec.State != AssuranceOpPassed || rec.Stage != "complete" || rec.BackupSetID != "alpha/nightly" {
		t.Fatalf("record = %+v", rec)
	}
	if rec.StartedAt.IsZero() || rec.EndedAt.Before(rec.StartedAt) {
		t.Fatalf("started %v ended %v", rec.StartedAt, rec.EndedAt)
	}
	for i := 1; i < len(rec.Events); i++ {
		if rec.Events[i].Sequence <= rec.Events[i-1].Sequence {
			t.Fatalf("lines are not oldest first: %d then %d", rec.Events[i-1].Sequence, rec.Events[i].Sequence)
		}
	}
}

func TestTheRingAndTheJournalAreMergedWithoutRepeatingALine(t *testing.T) {
	r := newDurableRig(t)
	r.recoveryTest("op-merge", []string{"resolving_copy", "reading"}, obs.ResultSuccess, "")
	r.flush()
	// The ring still holds every line (five), the journal three of them: the
	// record has each line once, the ring's stage lines included.
	rec, err := r.svc.AssuranceOperation(context.Background(), "op-merge")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(eventsNamed(rec), ","); got != "recovery_test_start,recovery_stage,recovery_stage,recovery_test_end" {
		t.Fatalf("lines = %s", got)
	}
}

func TestALineNotYetWrittenToTheJournalIsReadFromTheRing(t *testing.T) {
	r := newDurableRig(t)
	r.runLine(obs.EventRecoveryTestStart, "op-fresh", "")
	rec, err := r.svc.AssuranceOperation(context.Background(), "op-fresh")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Events) != 1 || rec.State != AssuranceOpUnfinished {
		t.Fatalf("record = %+v", rec)
	}
}

func TestHowARecoveryTestEndedIsReadOffItsEndLine(t *testing.T) {
	cases := []struct {
		name, reason, wantState, wantStage string
		result                             obs.Result
		stages                             []string
	}{
		{"failed", "sha256 differs", AssuranceOpFailed, "verifying", obs.ResultError, []string{"resolving_copy", "reading", "verifying"}},
		{"cancelled", app.RecoveryTestCancelledReason, AssuranceOpCancelled, "reading", obs.ResultWarn, []string{"resolving_copy", "reading"}},
		{"did not complete", "the copy could not be read", AssuranceOpDidNotComplete, "resolving_copy", obs.ResultWarn, []string{"resolving_copy"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newDurableRig(t)
			r.recoveryTest("op-x", c.stages, c.result, c.reason)
			r.flush()
			r.svc.activity = nil
			rec, err := r.svc.AssuranceOperation(context.Background(), "op-x")
			if err != nil {
				t.Fatal(err)
			}
			if rec.State != c.wantState || rec.Stage != c.wantStage || rec.Reason != c.reason {
				t.Fatalf("state %q stage %q reason %q, want %q %q %q", rec.State, rec.Stage, rec.Reason, c.wantState, c.wantStage, c.reason)
			}
		})
	}
}

func TestARunWithNoEndIsRunningOnlyWhileThisProcessRunsIt(t *testing.T) {
	r := newDurableRig(t)
	r.runLine(obs.EventRecoveryTestStart, "op-live", "")
	r.runLine(obs.EventRecoveryStage, "op-live", "", obs.Field{Key: "stage", Value: "reading"})
	r.svc.ops.mu.Lock()
	r.svc.ops.add(&assuranceOp{id: "op-live", kind: opKindRecoveryTest, set: "alpha/nightly", cancel: func() {}})
	r.svc.ops.mu.Unlock()
	rec, err := r.svc.AssuranceOperation(context.Background(), "op-live")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != AssuranceOpRunning || rec.Stage != "reading" || !rec.EndedAt.IsZero() {
		t.Fatalf("record = %+v", rec)
	}
	r.svc.ops.finish("op-live")
	if rec, _ = r.svc.AssuranceOperation(context.Background(), "op-live"); rec.State != AssuranceOpUnfinished {
		t.Fatalf("a run nothing is running and that wrote no end is %q, want unfinished", rec.State)
	}
}

func TestAScrubIsReadBackByTheOpIDOnItsPassLine(t *testing.T) {
	r := newDurableRig(t)
	scrub := func(event string, result obs.Result, fields ...obs.Field) {
		r.svc.activity.RecordEvent(obs.Record{
			At: time.Now().UTC(), Level: obs.LevelInfo, Event: event, Message: event, Result: result,
			Fields: append([]obs.Field{{Key: "backup_set", Value: "alpha/nightly"}}, fields...),
		})
	}
	scrub(obs.EventScrubFailure, obs.ResultWarn, obs.Field{Key: "artifact", Value: "alpha/nightly/a.zst"}, obs.Field{Key: "op_id", Value: "op-scrub"})
	scrub(obs.EventScrubPass, obs.ResultWarn, obs.Field{Key: "trigger", Value: "manual"}, obs.Field{Key: "scope", Value: "set alpha/nightly"},
		obs.Field{Key: "op_id", Value: "op-scrub"}, obs.Field{Key: "checked", Value: "2"}, obs.Field{Key: "failed", Value: "1"})
	// A scheduled pass of the same set carries no op_id and is not this one.
	scrub(obs.EventScrubPass, obs.ResultSuccess, obs.Field{Key: "trigger", Value: "schedule"}, obs.Field{Key: "scope", Value: "set alpha/nightly"})
	r.flush()
	r.svc.activity = nil
	rec, err := r.svc.AssuranceOperation(context.Background(), "op-scrub")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(eventsNamed(rec), ","); got != "scrub_failure,scrub_pass" {
		t.Fatalf("lines = %s", got)
	}
	if rec.Kind != "scrub" || rec.State != AssuranceOpFailed || rec.Stage != "" || rec.BackupSetID != "alpha/nightly" {
		t.Fatalf("record = %+v", rec)
	}
}

func TestScrubPassStates(t *testing.T) {
	pass := func(fields ...LiveActivityField) []LiveActivityEvent {
		return []LiveActivityEvent{{Sequence: 1, Event: obs.EventScrubPass, Fields: append([]LiveActivityField{{Key: "scope", Value: "set a/b"}}, fields...)}}
	}
	for want, evs := range map[string][]LiveActivityEvent{
		AssuranceOpPassed:         pass(LiveActivityField{Key: "failed", Value: "0"}),
		AssuranceOpFailed:         pass(LiveActivityField{Key: "failed", Value: "2"}),
		AssuranceOpDidNotComplete: pass(LiveActivityField{Key: "could_not_complete", Value: "1"}),
		AssuranceOpCancelled:      pass(LiveActivityField{Key: "cancelled", Value: "true"}, LiveActivityField{Key: "failed", Value: "1"}),
	} {
		if got := summariseAssuranceOperation("op", evs, false); got.State != want || got.BackupSetID != "a/b" {
			t.Errorf("%v: state %q set %q, want %q", evs[0].Fields, got.State, got.BackupSetID, want)
		}
	}
}

func TestNothingOnRecordIsNotFound(t *testing.T) {
	r := newDurableRig(t)
	r.recoveryTest("op-other", nil, obs.ResultSuccess, "")
	r.flush()
	_, err := r.svc.AssuranceOperation(context.Background(), "op-pruned")
	if !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("err = %v, want ErrOperationNotFound", err)
	}
	if _, err := r.svc.AssuranceOperation(context.Background(), ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("an empty id: err = %v, want ErrInvalidRequest", err)
	}
}
