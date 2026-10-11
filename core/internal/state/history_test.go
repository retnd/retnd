package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// CountArtifactsReadOnly is what decides a deployment is a fresh install
// (retnd#1049), so it has to be right about the four shapes a journal path
// can be in, and it has to leave all four exactly as it found them.

func TestCountArtifactsReadOnly_NoFileIsZeroAndCreatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "state.db")
	n, exists, err := CountArtifactsReadOnly(context.Background(), path)
	if err != nil || exists || n != 0 {
		t.Fatalf("= (%d, %v, %v), want (0, false, nil)", n, exists, err)
	}
	if _, statErr := os.Stat(filepath.Dir(path)); !os.IsNotExist(statErr) {
		t.Errorf("the read created %s", filepath.Dir(path))
	}
}

func TestCountArtifactsReadOnly_AnEmptyFileHasRecordedNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	n, exists, err := CountArtifactsReadOnly(context.Background(), path)
	if err != nil || !exists || n != 0 {
		t.Fatalf("= (%d, %v, %v), want (0, true, nil)", n, exists, err)
	}
}

func TestCountArtifactsReadOnly_CountsWhatTheJournalHolds(t *testing.T) {
	j, path := openJournal(t)
	ctx := context.Background()
	if n, exists, err := CountArtifactsReadOnly(ctx, path); err != nil || !exists || n != 0 {
		t.Fatalf("migrated and empty = (%d, %v, %v), want (0, true, nil)", n, exists, err)
	}
	if _, err := j.Discover(ctx, testArtifact(t), "k1", "/incoming/a", RemoteIdentity{}, time.Now()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// The journal is still open here (WAL mode, uncheckpointed): the read
	// must see the committed row anyway.
	n, exists, err := CountArtifactsReadOnly(ctx, path)
	if err != nil || !exists || n != 1 {
		t.Fatalf("= (%d, %v, %v), want (1, true, nil)", n, exists, err)
	}
	if got, err := j.CountArtifacts(ctx); err != nil || got != 1 {
		t.Fatalf("CountArtifacts = (%d, %v), want 1", got, err)
	}
}

func TestCountArtifactsReadOnly_AFileThatIsNotAJournalIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database, it is only long enough to look like one at a glance"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CountArtifactsReadOnly(context.Background(), path); err == nil {
		t.Fatal("an unreadable journal was reported as empty, which is how a lost config gets a default written over it")
	}
}
