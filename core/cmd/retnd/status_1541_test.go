package main

import (
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/model"
)

// Issue #1541: `retnd status` prints the complete-but-unreadable line under
// the set, and takes it from the running engine, which is the only process
// that ran discovery and so the only one that knows.

func TestStatusPrintsTheUnreadableWarningUnderTheSet_1541(t *testing.T) {
	age := 30 * time.Hour
	bs := health.BackupSetHealth{
		PendingUnreadableCount:      1,
		PendingUnreadableOldestAge:  &age,
		PendingUnreadableOldestPath: "gitea-dump.tar.gz",
		PendingUnreadableWarning:    "1 source file complete but unreadable for 30h, oldest gitea-dump.tar.gz; the producer has not published it",
	}
	lines := pendingUnreadableLines(bs)
	if len(lines) == 0 || !strings.Contains(lines[0], "warning: 1 source file complete but unreadable for 30h, oldest gitea-dump.tar.gz") {
		t.Errorf("lines = %q, want the warning first", lines)
	}

	bs.PendingUnreadableWarning = ""
	age = 2 * time.Hour
	lines = pendingUnreadableLines(bs)
	if len(lines) != 1 || !strings.Contains(lines[0], "waiting to be published: 1 complete source file(s) not readable yet, the oldest gitea-dump.tar.gz for 2h0m0s") {
		t.Errorf("below stale_after: lines = %q, want the quiet count", lines)
	}

	if got := pendingUnreadableLines(health.BackupSetHealth{}); got != nil {
		t.Errorf("a set with nothing waiting printed %q", got)
	}
}

func TestStatusTakesTheUnreadableCountFromTheEngine_1541(t *testing.T) {
	set, err := model.NewBackupSetID("cicd-pipeline", "forge")
	if err != nil {
		t.Fatal(err)
	}
	other, err := model.NewBackupSetID("cicd-pipeline", "registry")
	if err != nil {
		t.Fatal(err)
	}
	report := health.Report{BackupSets: []health.BackupSetHealth{{Set: set}, {Set: other}}}
	engine := apicontract.HealthResponse{BackupSets: []apicontract.BackupSetHealth{{
		BackupSetID:                       set.String(),
		PendingUnreadable:                 2,
		PendingUnreadableOldestAgeSeconds: int64((26 * time.Hour).Seconds()),
		PendingUnreadableOldestPath:       "gitea-dump.tar.gz",
		PendingUnreadableWarning:          "2 source files complete but unreadable for 26h, oldest gitea-dump.tar.gz; the producer has not published them",
	}}}

	adoptEnginePendingUnreadable(&report, engine)

	got := report.BackupSets[0]
	if got.PendingUnreadableCount != 2 || got.PendingUnreadableOldestPath != "gitea-dump.tar.gz" || !strings.HasPrefix(got.PendingUnreadableWarning, "2 source files") {
		t.Errorf("forge after adoption = %+v, want the engine's count, path and warning", got)
	}
	if got.PendingUnreadableOldestAge == nil || *got.PendingUnreadableOldestAge != 26*time.Hour {
		t.Errorf("forge oldest age = %v, want 26h", got.PendingUnreadableOldestAge)
	}
	if report.BackupSets[1].PendingUnreadableCount != 0 {
		t.Errorf("registry took a count the engine did not report for it: %+v", report.BackupSets[1])
	}
}
