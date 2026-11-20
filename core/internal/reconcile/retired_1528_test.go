package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
)

// Issue #1528: a RETIRED row is left alone. Before the case existed,
// reconcileOne's default turned every RETIRED row into a per-cycle error.
func TestReconcileLeavesRetiredAlone_1528(t *testing.T) {
	j := openTestJournal(t)
	artifact := testArtifact(t, "retired.dump")
	size := int64(10)
	driveTo(t, j, driveParams{artifact: artifact, remote: state.RemoteIdentity{Size: &size}, stopAt: lifecycle.Discovered})
	ctx := context.Background()
	now := time.Now().UTC()
	for _, tr := range []state.Transition{
		{Artifact: artifact, Key: "f", From: "DISCOVERED", To: "FAILED", OccurredAt: now},
		{Artifact: artifact, Key: "r", From: "FAILED", To: "RETIRED", OccurredAt: now,
			Retirement: &state.RetirementUpdate{ActorKind: state.RetiredByOperator, SourceCheck: state.SourceAbsent}},
	} {
		if _, err := j.RecordTransition(ctx, tr); err != nil {
			t.Fatalf("%s -> %s: %v", tr.From, tr.To, err)
		}
	}

	tp := &fakeTransport{}
	report, err := Reconcile(ctx, Deps{Journal: j, Transport: tp}, testSource, testSet(t))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	requireNoErrors(t, report)
	f := requireOneFinding(t, report)
	if f.Changed() || f.From != lifecycle.Retired {
		t.Errorf("finding = %s -> %s, want RETIRED left alone", f.From, f.To)
	}
	if tp.statCalls != 0 {
		t.Errorf("statCalls = %d, want 0", tp.statCalls)
	}
}
