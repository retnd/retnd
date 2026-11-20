package health

import (
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
)

// Issue #1528: a RETIRED artifact is a failure nobody needs to act on any
// more. It must stop forcing FAILING, count toward neither failures nor the
// stuck list, and still be counted, apart, so nothing is hidden.
func TestRetiredIsCountedApartAndNeverFails_1528(t *testing.T) {
	now := time.Now().UTC()
	records := []state.Record{
		rec("old-1.dump", lifecycle.Retired, now.Add(-20*day), now.Add(-time.Hour)),
		rec("old-2.dump", lifecycle.Retired, now.Add(-15*day), now.Add(-time.Hour)),
		rec("new.dump", lifecycle.Complete, now.Add(-2*time.Hour), now.Add(-time.Hour)),
	}
	got := ComputeBackupSetHealth(testSet, records, nil, PlacementEvidence{}, day, BackupSetInputs{}, now)

	if got.State != Healthy {
		t.Errorf("State = %s (%s), want HEALTHY", got.State, got.Reason)
	}
	if got.Failures != 0 || len(got.StuckFailures) != 0 {
		t.Errorf("failures=%d stuck=%v, want none", got.Failures, got.StuckFailures)
	}
	if got.RetiredCount != 2 {
		t.Errorf("RetiredCount = %d, want 2", got.RetiredCount)
	}

	// Negative control: the same rows as FAILED force FAILING, so the case
	// above is green because of RETIRED and not because of the fixture.
	records[0].State, records[1].State = string(lifecycle.Failed), string(lifecycle.Failed)
	if got := ComputeBackupSetHealth(testSet, records, nil, PlacementEvidence{}, day, BackupSetInputs{}, now); got.State != Failing {
		t.Errorf("the control with FAILED rows reads %s, want FAILING", got.State)
	}
}

func TestRetiredIsNotARetainedBackup_1528(t *testing.T) {
	now := time.Now().UTC()
	records := []state.Record{
		rec("old.dump", lifecycle.Retired, now.Add(-20*day), now.Add(-time.Hour)),
		rec("new.dump", lifecycle.Complete, now.Add(-2*time.Hour), now.Add(-time.Hour)),
	}
	got := ComputeBackupSetHealth(testSet, records, nil, PlacementEvidence{}, day, BackupSetInputs{}, now)
	if got.RetainedCount != 1 {
		t.Errorf("RetainedCount = %d, want 1: a retired failure is not a backup this deployment holds", got.RetainedCount)
	}
	if n := CountRetained(records); n != 1 {
		t.Errorf("CountRetained = %d, want 1", n)
	}
}

// A retirement is a bookkeeping change, not activity on the source: a set
// whose only recent row is a retirement and whose good backup is past the
// threshold reads STALE, not "recent activity" DEGRADED (the #1536 backend
// review's S5).
func TestARetirementIsNotFreshActivity_1528(t *testing.T) {
	now := time.Now().UTC()
	records := []state.Record{
		rec("old-good.dump", lifecycle.Complete, now.Add(-10*day), now.Add(-9*day)),
		rec("failed.dump", lifecycle.Retired, now.Add(-5*day), now.Add(-time.Minute)),
	}
	got := ComputeBackupSetHealth(testSet, records, nil, PlacementEvidence{}, day, BackupSetInputs{}, now)
	if got.State != Stale {
		t.Errorf("State = %s (%s), want STALE: retiring a row a minute ago is not a backup in progress", got.State, got.Reason)
	}
}
