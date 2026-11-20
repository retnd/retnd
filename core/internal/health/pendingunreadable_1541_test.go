package health

import (
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
)

// Issue #1541: a source file that is complete but unreadable is counted
// beside the verdict, gets a warning once it has waited longer than the
// set's stale_after, and never moves the verdict.

func TestAnUnreadableFileYoungerThanStaleAfterIsCountedWithoutAWarning_1541(t *testing.T) {
	now := time.Now().UTC()
	in := BackupSetInputs{PendingUnreadable: []PendingUnreadable{
		{RemotePath: "run-1/gitea-dump.tar.gz", FirstSeen: now.Add(-2 * time.Hour)},
	}}
	got := ComputeBackupSetHealth(testSet, nil, nil, PlacementEvidence{}, day, in, now)

	if got.PendingUnreadableCount != 1 {
		t.Errorf("PendingUnreadableCount = %d, want 1", got.PendingUnreadableCount)
	}
	if got.PendingUnreadableOldestAge == nil || *got.PendingUnreadableOldestAge != 2*time.Hour {
		t.Errorf("PendingUnreadableOldestAge = %v, want 2h", got.PendingUnreadableOldestAge)
	}
	if got.PendingUnreadableWarning != "" {
		t.Errorf("a file waiting 2h under a 24h stale_after warned: %q", got.PendingUnreadableWarning)
	}
}

func TestAnUnreadableFileOlderThanStaleAfterWarnsWithThePathAndTheAge_1541(t *testing.T) {
	now := time.Now().UTC()
	in := BackupSetInputs{PendingUnreadable: []PendingUnreadable{
		{RemotePath: "run-2/gitea-dump-b.tar.gz", FirstSeen: now.Add(-3 * time.Hour)},
		{RemotePath: "run-1/gitea-dump-a.tar.gz", FirstSeen: now.Add(-26 * time.Hour)},
	}}
	got := ComputeBackupSetHealth(testSet, nil, nil, PlacementEvidence{}, day, in, now)

	want := "2 source files complete but unreadable for 26h, oldest run-1/gitea-dump-a.tar.gz; the producer has not published them"
	if got.PendingUnreadableWarning != want {
		t.Errorf("PendingUnreadableWarning = %q, want %q", got.PendingUnreadableWarning, want)
	}
	if got.PendingUnreadableOldestPath != "run-1/gitea-dump-a.tar.gz" {
		t.Errorf("PendingUnreadableOldestPath = %q, want the one first seen 26h ago", got.PendingUnreadableOldestPath)
	}
}

// The warning sits beside the verdict. A set with a fresh good backup stays
// HEALTHY however long a file has been waiting for its producer, which is
// the forge set's exact shape: a good backup every morning and one stuck
// dump.
func TestAnUnreadableFileDoesNotMoveTheVerdict_1541(t *testing.T) {
	now := time.Now().UTC()
	records := []state.Record{rec("good.dump", lifecycle.Complete, now.Add(-time.Hour), now.Add(-time.Hour))}
	in := BackupSetInputs{PendingUnreadable: []PendingUnreadable{
		{RemotePath: "gitea-dump.tar.gz", FirstSeen: now.Add(-16 * day)},
	}}

	without := ComputeBackupSetHealth(testSet, records, nil, PlacementEvidence{}, day, BackupSetInputs{}, now)
	with := ComputeBackupSetHealth(testSet, records, nil, PlacementEvidence{}, day, in, now)

	if with.State != without.State || with.Reason != without.Reason {
		t.Errorf("an unreadable file moved the verdict: %s (%q), want %s (%q)", with.State, with.Reason, without.State, without.Reason)
	}
	if with.State != Healthy {
		t.Errorf("State = %s, want %s for a set with a backup an hour old", with.State, Healthy)
	}
	if !strings.Contains(with.PendingUnreadableWarning, "1 source file complete but unreadable for 384h, oldest gitea-dump.tar.gz; the producer has not published it") {
		t.Errorf("PendingUnreadableWarning = %q, want the singular warning naming the file", with.PendingUnreadableWarning)
	}
}
