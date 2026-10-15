package health

import (
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
)

// Issue #1186 (review of #1187): the sidebar's Backups badge reads one number
// off the health report instead of downloading the whole backups list. That
// number is every backup the journal holds outside quarantine, so it is the
// Backups page's rows less the ones the Quarantine badge already counts. A
// quarantined backup counted in both badges would be counted twice.
func TestIssue1186_RetainedCountIsEveryBackupOutsideQuarantine(t *testing.T) {
	now := time.Now().UTC()
	records := []state.Record{
		rec("a.dump", lifecycle.Complete, now.Add(-3*day), now.Add(-3*day)),
		rec("b.dump", lifecycle.Committed, now.Add(-2*day), now.Add(-2*day)),
		rec("c.dump", lifecycle.RemoteRetained, now.Add(-2*day), now.Add(-2*day)),
		rec("d.dump", lifecycle.Transferring, now.Add(-time.Hour), now.Add(-time.Minute)),
		withRetry(rec("e.dump", lifecycle.Failed, now.Add(-time.Hour), now.Add(-time.Minute)), now.Add(time.Hour)),
		rec("f.dump", lifecycle.Quarantined, now.Add(-4*day), now.Add(-day)),
		rec("g.dump", lifecycle.QuarantinedLost, now.Add(-5*day), now.Add(-day)),
	}
	got := ComputeBackupSetHealth(testSet, records, nil, PlacementEvidence{}, day, BackupSetInputs{}, now)
	if got.RetainedCount != 5 {
		t.Errorf("RetainedCount = %d, want 5 (seven rows, two of them quarantined)", got.RetainedCount)
	}
	if got.QuarantinedCount != 2 {
		t.Errorf("control: QuarantinedCount = %d, want 2", got.QuarantinedCount)
	}
}

func TestIssue1186_RetainedCountOfAnEmptySetIsZero(t *testing.T) {
	got := ComputeBackupSetHealth(testSet, nil, nil, PlacementEvidence{}, day, BackupSetInputs{}, time.Now().UTC())
	if got.RetainedCount != 0 {
		t.Errorf("RetainedCount = %d, want 0", got.RetainedCount)
	}
}

func TestIssue1186_RetainedCountHelperAgreesWithTheSetComputation(t *testing.T) {
	now := time.Now().UTC()
	records := []state.Record{
		rec("a.dump", lifecycle.Complete, now, now),
		rec("b.dump", lifecycle.Quarantined, now, now),
	}
	if n := CountRetained(records); n != 1 {
		t.Errorf("CountRetained = %d, want 1", n)
	}
}
