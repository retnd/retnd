package webhost

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/retnd/retnd/core/service"
)

// Issue #1541's wire half: the complete-but-unreadable count reaches GET
// /api/v1/system/health with its age, path and warning, and a set with
// nothing waiting carries the count as zero and the other three not at all.

func TestSystemHealthCarriesPendingUnreadable_1541(t *testing.T) {
	got := toBackupSetHealthResponse(service.BackupSetHealth{
		PendingUnreadable:           2,
		PendingUnreadableOldestAge:  26 * time.Hour,
		PendingUnreadableOldestPath: "run-1/gitea-dump.tar.gz",
		PendingUnreadableWarning:    "2 source files complete but unreadable for 26h, oldest run-1/gitea-dump.tar.gz; the producer has not published them",
	})
	if got.PendingUnreadable != 2 || got.PendingUnreadableOldestAgeSeconds != int64((26*time.Hour).Seconds()) ||
		got.PendingUnreadableOldestPath != "run-1/gitea-dump.tar.gz" || got.PendingUnreadableWarning == "" {
		t.Errorf("projection = %+v, want the count, the age in seconds, the path and the warning", got)
	}

	raw, err := json.Marshal(toBackupSetHealthResponse(service.BackupSetHealth{}))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if v, ok := wire["pending_unreadable"]; !ok || v != float64(0) {
		t.Errorf("pending_unreadable = %v (present %v), want a real zero: absence would read as a build that does not know", v, ok)
	}
	for _, k := range []string{"pending_unreadable_oldest_age_seconds", "pending_unreadable_oldest_path", "pending_unreadable_warning"} {
		if _, ok := wire[k]; ok {
			t.Errorf("%s is on the wire for a set with nothing waiting; it describes a file, and there is none", k)
		}
	}
}
