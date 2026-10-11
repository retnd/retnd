package health

import (
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
)

// A scrub that keeps failing is a backup set that cannot be trusted to
// restore, and it degrades the set's health. These are the pairs that say
// where it sits in the precedence: it can turn HEALTHY into DEGRADED and
// nothing into anything else.

func healthyRecordWithCopy(failures int, now time.Time) state.Record {
	r := rec("a.dump", lifecycle.Complete, now.Add(-2*time.Hour), now.Add(-time.Hour))
	r.Placements = []state.Placement{{
		Medium: state.MediumLocal, Status: state.PlacementActive,
		Assurance: state.Assurance{AssuranceState: state.AssuranceFailed, ConsecutiveFailures: failures},
	}}
	return r
}

func TestAScrubFailingAtTheThresholdDegradesAHealthySet(t *testing.T) {
	now := time.Now().UTC()
	records := []state.Record{healthyRecordWithCopy(ScrubFailureThreshold, now)}
	got := ComputeBackupSetHealth(testSet, records, nil, PlacementEvidence{}, day, BackupSetInputs{}, now)

	if got.State != Degraded {
		t.Fatalf("State = %s, want %s: a copy failing its scrub %d times running is not healthy", got.State, Degraded, ScrubFailureThreshold)
	}
	if got.ScrubFailingCopies != 1 {
		t.Errorf("ScrubFailingCopies = %d, want 1", got.ScrubFailingCopies)
	}
}

func TestAScrubBelowTheThresholdLeavesTheSetHealthy(t *testing.T) {
	now := time.Now().UTC()
	records := []state.Record{healthyRecordWithCopy(ScrubFailureThreshold-1, now)}
	got := ComputeBackupSetHealth(testSet, records, nil, PlacementEvidence{}, day, BackupSetInputs{}, now)
	if got.State != Healthy || got.ScrubFailingCopies != 0 {
		t.Errorf("State = %s / failing = %d: one blip below the threshold must not degrade a set", got.State, got.ScrubFailingCopies)
	}
}

func TestALaterPassClearsTheDegradation(t *testing.T) {
	now := time.Now().UTC()
	r := healthyRecordWithCopy(ScrubFailureThreshold, now)
	if ComputeBackupSetHealth(testSet, []state.Record{r}, nil, PlacementEvidence{}, day, BackupSetInputs{}, now).State != Degraded {
		t.Fatal("fixture error: not degraded to begin with")
	}
	r.Placements[0].ConsecutiveFailures = 0
	r.Placements[0].AssuranceState = state.AssurancePassed
	if got := ComputeBackupSetHealth(testSet, []state.Record{r}, nil, PlacementEvidence{}, day, BackupSetInputs{}, now); got.State != Healthy {
		t.Errorf("after a pass State = %s, want %s", got.State, Healthy)
	}
}

func TestAFailingScrubNeverSoftensAStrongerVerdict(t *testing.T) {
	now := time.Now().UTC()
	lost := rec("lost.dump", lifecycle.QuarantinedLost, now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	got := ComputeBackupSetHealth(testSet, []state.Record{lost, healthyRecordWithCopy(ScrubFailureThreshold, now)}, nil, PlacementEvidence{}, day, BackupSetInputs{}, now)
	if got.State != Failing {
		t.Errorf("State = %s, want %s: the scrub must not replace a stronger verdict", got.State, Failing)
	}
	if got.ScrubFailingCopies != 1 {
		t.Errorf("ScrubFailingCopies = %d: the count is reported whatever the verdict", got.ScrubFailingCopies)
	}
}

func TestOnlyActiveCopiesCountTowardsScrubFailure(t *testing.T) {
	now := time.Now().UTC()
	r := healthyRecordWithCopy(ScrubFailureThreshold, now)
	r.Placements[0].Status = state.PlacementGone
	if got := ComputeBackupSetHealth(testSet, []state.Record{r}, nil, PlacementEvidence{}, day, BackupSetInputs{}, now); got.ScrubFailingCopies != 0 {
		t.Errorf("a GONE copy counted: %d", got.ScrubFailingCopies)
	}
}

// --- recovery_test_required (EPIC Q, #980) ---
//
// A set whose operator said a recovery test is required is degraded when the
// newest known good copy's test failed, is overdue, or has not run. It never
// rewrites a backup that already succeeded, and it never softens a stronger
// verdict.

func recoveryRecord(now time.Time, rs string, tested, next *time.Time) state.Record {
	r := rec("r.dump", lifecycle.Complete, now.Add(-2*time.Hour), now.Add(-time.Hour))
	r.Placements = []state.Placement{{
		Medium: state.MediumLocal, Status: state.PlacementActive,
		Assurance: state.Assurance{RecoveryState: rs, RecoveryTestedAt: tested, RecoveryNextDueAt: next},
	}}
	return r
}

func TestARequiredRecoveryTestDegradesWhenFailedOverdueOrNeverRun(t *testing.T) {
	now := time.Now().UTC()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for name, tc := range map[string]struct {
		rec  state.Record
		want State
	}{
		"fresh pass":       {recoveryRecord(now, state.RecoveryPassed, &past, &future), Healthy},
		"failed":           {recoveryRecord(now, state.RecoveryFailed, &past, &future), Degraded},
		"pass gone stale":  {recoveryRecord(now, state.RecoveryPassed, &past, &past), Degraded},
		"marked overdue":   {recoveryRecord(now, state.RecoveryOverdue, &past, &past), Degraded},
		"never run":        {recoveryRecord(now, state.RecoveryUnknown, nil, nil), Degraded},
		"refused for cost": {recoveryRecord(now, state.RecoveryRefusedForCost, &past, &future), Degraded},
		"not configured":   {recoveryRecord(now, state.RecoveryNotConfigured, nil, nil), Degraded},
	} {
		t.Run(name, func(t *testing.T) {
			got := ComputeBackupSetHealth(testSet, []state.Record{tc.rec}, nil, PlacementEvidence{}, day, BackupSetInputs{RecoveryTestRequired: true}, now)
			if got.State != tc.want {
				t.Errorf("State = %s (%s), want %s", got.State, got.Reason, tc.want)
			}
		})
	}
}

func TestARecoveryTestThatIsNotRequiredNeverDegradesAnything(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	got := ComputeBackupSetHealth(testSet, []state.Record{recoveryRecord(now, state.RecoveryFailed, &past, &past)}, nil, PlacementEvidence{}, day, BackupSetInputs{}, now)
	if got.State != Healthy {
		t.Errorf("State = %s: a failed test nobody required degraded the set", got.State)
	}
}

func TestARequiredRecoveryTestNeverRewritesAnEarlierSuccessfulBackup(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	r := recoveryRecord(now, state.RecoveryFailed, &past, &past)
	got := ComputeBackupSetHealth(testSet, []state.Record{r}, nil, PlacementEvidence{}, day, BackupSetInputs{RecoveryTestRequired: true}, now)
	if got.NewestGoodBackupAt == nil || got.LastCompletedBackupAt == nil {
		t.Error("the backup that succeeded stopped being the newest good one because its recovery test failed")
	}
	lost := rec("lost.dump", lifecycle.QuarantinedLost, now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	if s := ComputeBackupSetHealth(testSet, []state.Record{lost, r}, nil, PlacementEvidence{}, day, BackupSetInputs{RecoveryTestRequired: true}, now).State; s != Failing {
		t.Errorf("a required recovery test softened FAILING to %s", s)
	}
}
