package app

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// #1203 review: the smaller correctness items in the recovery test's half.

// A name that is not there is never matched. A pattern such as ^$ or .* does
// match the empty string, and a set with no copy yet has no name to ask about.
func TestCustomValidator_AnEmptyNameMatchesNoPattern(t *testing.T) {
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"all.local.sh", "none.local.sh"}}}
	svc, _, bs := customValidatorService(t, stages, "",
		config.CustomValidator{Name: "all", Pattern: `.*`},
		config.CustomValidator{Name: "none", Pattern: `^$`, BackupSet: "@set"},
	)
	name, ambiguous := svc.RecoveryTestValidatorFor(bs.ID, "")
	if name != "" || len(ambiguous) != 0 {
		t.Errorf("RecoveryTestValidatorFor(\"\") = %q, %v; an empty name matches nothing", name, ambiguous)
	}
}

// injectingJournal lets a newer backup land right after the first time the
// set's backups are listed, which is the moment between "the feed names the
// validator" and "the test picks its copy".
type injectingJournal struct {
	*state.Journal
	mu   sync.Mutex
	once func()
}

func (j *injectingJournal) ListByBackupSet(ctx context.Context, set model.BackupSetID) ([]state.Record, error) {
	recs, err := j.Journal.ListByBackupSet(ctx, set)
	j.mu.Lock()
	f := j.once
	j.once = nil
	j.mu.Unlock()
	if f != nil {
		f()
	}
	return recs, err
}

func TestCustomValidator_TheCopyTheFeedNamesIsTheCopyTheTestRuns(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"manifest.local.sh", "dumps.local.sh"}}}
	svc, journal, bs := customValidatorService(t, stages, "",
		config.CustomValidator{Name: "manifest", Pattern: `^MANIFEST-`},
		config.CustomValidator{Name: "dumps", Pattern: `\.sql\.gz$`},
	)
	first := seedMovableArtifact(t, ctx, journal, bs, "MANIFEST-20260902T153521Z.txt", retentionTestNow.Add(-2*time.Hour))
	inj := &injectingJournal{Journal: journal}
	inj.once = func() {
		seedMovableArtifact(t, ctx, journal, bs, "db-2026-09-03.sql.gz", retentionTestNow.Add(-time.Hour))
	}
	svc.Journal = inj
	var stream bytes.Buffer
	svc.Logger = obs.New(&stream, obs.LevelInfo)

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.Artifact != first {
		t.Errorf("the test ran on %s after the feed named the validator for %s: the newest copy was resolved twice", res.Result.Artifact, first)
	}
	start := eventsOf(t, &stream, obs.EventRecoveryTestStart)
	if len(start) != 1 || start[0]["validator"] != "manifest" {
		t.Errorf("start events = %v", start)
	}
	if got := stages.ran(); len(got) != 1 || got[0] != config.CustomValidatorDir+"/manifest.local.sh" {
		t.Errorf("ran %v, want the validator the feed named", got)
	}
}

// A pattern matched and the test did not run it. That is a downgrade an
// operator must be able to find in the log.
func TestCustomValidator_AMatchedValidatorThatDoesNotRunLeavesAnEvent(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"archive.local.sh"}}}
	svc, journal, bs := customValidatorService(t, stages, "",
		config.CustomValidator{Name: "archive", Pattern: `\.tar$`},
	)
	seedMovableArtifact(t, ctx, journal, bs, "nightly.tar", retentionTestNow.Add(-time.Hour))
	svc.Stages = nil // no stage runner: the matched script cannot be run
	var stream bytes.Buffer
	svc.Logger = obs.New(&stream, obs.LevelInfo)

	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	evs := eventsOf(t, &stream, "recovery_test_validator_fallback")
	if len(evs) != 1 {
		t.Fatalf("recovery_test_validator_fallback events = %d, want 1\n%s", len(evs), stream.String())
	}
	if evs[0]["validator"] != "archive" || evs[0]["backup_set"] != bs.ID.String() {
		t.Errorf("event = %v", evs[0])
	}
}
