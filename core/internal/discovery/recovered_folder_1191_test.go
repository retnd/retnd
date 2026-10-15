package discovery

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/transport"
	"github.com/retnd/retnd/core/internal/transport/rclone"
)

// Issues #1191 and #1193: a recovery to the source host puts the recovered
// files in a new folder directly under the set's own remote path, which is
// inside the tree the next run lists. Those files are the operator's
// recovered copies, not new backups: discovering them would ingest them
// again, and on a set that deletes from its source the next run would then
// delete the very file that was just recovered. The skip is exactly that
// depth and nothing else, and it is counted rather than silent.
func TestDiscover_ARecoveredFolderAtDepthOneIsSkippedAndCounted(t *testing.T) {
	for _, include := range [][]string{nil, {"*"}, {"*.dump"}} {
		root := t.TempDir()
		folder := transport.RecoveredFolderPrefix + "20261015-101500-a1b2"
		mustWrite(t, filepath.Join(root, "backup.dump"), "a real payload")
		mustWrite(t, filepath.Join(root, folder, "older.dump"), "recovered by the operator")
		mustWrite(t, filepath.Join(root, folder, "manifest-op.json"), "{}")
		// The operator's own folder of that name further down, and a file
		// merely named with the prefix, are candidates like any other.
		mustWrite(t, filepath.Join(root, "nested", folder, "deeper.dump"), "the operator's own")
		mustWrite(t, filepath.Join(root, transport.RecoveredFolderPrefix+"file.dump"), "a file, not a folder")

		source := transport.Source{ID: "recovered-folder", Type: "local", Root: root}
		set := backupSet(t, config.Completion{Strategy: "rename"}, include)
		deps := Deps{Transport: rclone.New(), Journal: openJournal(t), Now: fixedNow(epoch)}

		res, err := Discover(context.Background(), deps, source, set)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		seen := map[string]bool{}
		for _, rec := range res.Discovered {
			seen[rec.RemotePath] = true
		}
		for _, p := range res.Pending {
			seen[p.RemotePath] = true
		}
		for _, r := range res.Rejected {
			seen[r.RemotePath] = true
		}
		for p := range seen {
			if strings.HasPrefix(p, folder+"/") {
				t.Errorf("include=%v: %s, in the recovered folder, was taken for a candidate", include, p)
			}
		}
		if res.SkippedRecovered != 2 {
			t.Errorf("include=%v: SkippedRecovered = %d, want 2 (older.dump and the manifest)", include, res.SkippedRecovered)
		}
		if !seen["backup.dump"] {
			t.Errorf("include=%v: the real payload beside the recovered folder was not discovered: %+v", include, res)
		}
		if !seen["nested/"+folder+"/deeper.dump"] {
			t.Errorf("include=%v: a folder of that name deeper down is the operator's, and was skipped", include)
		}
	}
}

func TestInRecoveredFolder_OnlyDepthOne(t *testing.T) {
	p := transport.RecoveredFolderPrefix
	for path, want := range map[string]bool{
		p + "x/a.dump":            true,
		p + "x/sub/a.dump":        true,
		"a/" + p + "x/b.dump":     false,
		p + "file.dump":           false,
		"plain/a.dump":            false,
		p + "x/":                  false,
		"x" + p + "y/a.dump":      false,
		"":                        false,
		p + "x/" + p + "y/z.dump": true,
	} {
		if got := InRecoveredFolder(path); got != want {
			t.Errorf("InRecoveredFolder(%q) = %v, want %v", path, got, want)
		}
	}
}

// FuzzInRecoveredFolder holds the predicate to its one rule for any input:
// true exactly when the first segment carries the prefix and something
// follows it.
func FuzzInRecoveredFolder(f *testing.F) {
	for _, seed := range []string{"retnd-recovered-x/a", "a/retnd-recovered-x/b", "retnd-recovered-", "/", "", "retnd-recovered-x/"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, p string) {
		got := InRecoveredFolder(p)
		first, rest, found := strings.Cut(p, "/")
		want := found && rest != "" && strings.HasPrefix(first, transport.RecoveredFolderPrefix)
		if got != want {
			t.Fatalf("InRecoveredFolder(%q) = %v, want %v", p, got, want)
		}
		if got && strings.Count(first, "/") != 0 {
			t.Fatalf("matched a first segment with a separator: %q", p)
		}
	})
}
