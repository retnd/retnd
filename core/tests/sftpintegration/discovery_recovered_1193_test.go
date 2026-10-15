package sftpintegration_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/discovery"
	"github.com/retnd/retnd/core/internal/transport"
	"github.com/retnd/retnd/core/internal/transport/rclone"
	"github.com/retnd/retnd/core/tests/machines"
)

// Issue #1193 (M13): the discovery skip, through the SFTP lister rather than
// the local one. A folder a source-host recovery created directly under the
// remote path is skipped and counted; one of the same name deeper down is
// the operator's, and its files are candidates.
func TestSFTPDiscovery1193_SkipsTheRecoveredFolderAtDepthOneOnly(t *testing.T) {
	f := machines.Start(t).Source(t)
	root := "discover-recovered"
	folder := transport.RecoveredFolderPrefix + "20261015-101500-a1b2"
	for rel, body := range map[string]string{
		"backup.dump":                       "a real payload",
		folder + "/older.dump":              "recovered by the operator",
		folder + "/manifest-op.json":        "{}",
		"nested/" + folder + "/deeper.dump": "the operator's own folder",
	} {
		p := filepath.Join(f.UploadDir, root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set := mustSetID(t, "recovered-source", "set")
	bs := config.BackupSet{Name: "set", ID: set, Completion: config.Completion{Strategy: "rename"}}
	res, err := discovery.Discover(f.Context(), discovery.Deps{Transport: rclone.New(), Journal: openJournal(t)}, f.TransportSource("recovered", root), bs)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got := map[string]bool{}
	for _, r := range res.Discovered {
		got[r.RemotePath] = true
	}
	if !got["backup.dump"] || !got["nested/"+folder+"/deeper.dump"] || len(got) != 2 {
		t.Errorf("discovered %v, want backup.dump and the deeper file only", got)
	}
	if res.SkippedRecovered != 2 {
		t.Errorf("SkippedRecovered = %d, want 2", res.SkippedRecovered)
	}
}
