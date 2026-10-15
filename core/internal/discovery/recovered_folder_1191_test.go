package discovery

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/transport"
	"github.com/retnd/retnd/core/internal/transport/rclone"
)

// Issue #1191: a recovery to the source host puts the recovered files in a
// new folder under the set's own remote path, which is inside the tree the
// next run lists (the listing is recursive). Those files are the operator's
// recovered copies, not new backups: discovering them would ingest them
// again, and on a set that deletes from its source the next run would then
// delete the very file that was just recovered.
func TestDiscover_ARecoveredFolderOnTheSourceIsNeverListedAsBackups(t *testing.T) {
	for _, include := range [][]string{nil, {"*"}, {"*.dump"}} {
		root := t.TempDir()
		folder := transport.RecoveredFolderPrefix + "20261015-101500"
		mustWrite(t, filepath.Join(root, "backup.dump"), "a real payload")
		mustWrite(t, filepath.Join(root, folder, "older.dump"), "recovered by the operator")
		mustWrite(t, filepath.Join(root, "nested", folder, "deeper.dump"), "recovered into a nested set root")

		source := transport.Source{ID: "recovered-folder", Type: "local", Root: root}
		set := backupSet(t, config.Completion{Strategy: "rename"}, include)
		deps := Deps{Transport: rclone.New(), Journal: openJournal(t), Now: fixedNow(epoch)}

		res, err := Discover(context.Background(), deps, source, set)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		recovered := map[string]bool{
			folder + "/older.dump":              true,
			"nested/" + folder + "/deeper.dump": true,
		}
		found := false
		for _, rec := range res.Discovered {
			if recovered[rec.RemotePath] {
				t.Errorf("include=%v: a recovered file was discovered as a new backup: %+v", include, rec)
			}
			if rec.RemotePath == "backup.dump" {
				found = true
			}
		}
		for _, p := range res.Pending {
			if recovered[p.RemotePath] {
				t.Errorf("include=%v: a recovered file was reported Pending: %+v", include, p)
			}
		}
		for _, r := range res.Rejected {
			if recovered[r.RemotePath] {
				t.Errorf("include=%v: a recovered file was reported Rejected: %+v", include, r)
			}
		}
		if !found {
			t.Errorf("include=%v: the real payload beside the recovered folder was not discovered: %+v", include, res)
		}
	}
}
