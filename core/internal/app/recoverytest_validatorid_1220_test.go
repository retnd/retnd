package app

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// #1220: a custom validator is named by its NAME everywhere a test leaves a
// record of it, the activity feed and the journal alike. The journal used to
// hold the script file that ran (manifest.local.sh) while the feed held the
// name (manifest), so a surface joining the two had to know both spellings.

func TestCustomValidatorID_TheFeedAndTheJournalNameTheSameValidator(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"manifest.local.sh"}}}
	svc, journal, bs := customValidatorService(t, stages, "", config.CustomValidator{Name: "manifest", Pattern: `^MANIFEST-`})
	a := seedMovableArtifact(t, ctx, journal, bs, "MANIFEST-20260902T153521Z.txt", retentionTestNow.Add(-time.Hour))
	var stream bytes.Buffer
	svc.Logger = obs.New(&stream, obs.LevelInfo)

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil || res.Result.State != state.RecoveryPassed {
		t.Fatalf("RecoveryTest = %q, %v", res.Result.State, err)
	}
	if got := localCopyOf(t, journal, a).RecoveryValidatorID; got != "manifest" {
		t.Errorf("journal validator = %q, want the validator's name", got)
	}
	for _, ev := range []string{obs.EventRecoveryTestStart, obs.EventRecoveryTestEnd} {
		got := eventsOf(t, &stream, ev)
		if len(got) != 1 || got[0]["validator"] != "manifest" {
			t.Errorf("%s events = %v, want validator manifest", ev, got)
		}
	}
	if got := stages.ran(); len(got) != 1 || got[0] != config.CustomValidatorDir+"/manifest.local.sh" {
		t.Errorf("ran %v: the script file is still what is selected, only the recorded id changed", got)
	}
}

// A run that no custom validator decided keeps the spelling it has always had,
// so the history already written reads the same as the history written next.
func TestCustomValidatorID_ASetsOwnValidatorIDIsRecordedAsItAlwaysWas(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{}}
	svc, journal, bs := customValidatorService(t, stages, "own", config.CustomValidator{Name: "manifest", Pattern: `^MANIFEST-`})
	stages.dirs[RecoveryTestSetDir(bs.ID)] = []string{"own.local.sh"}
	a := seedMovableArtifact(t, ctx, journal, bs, "nightly.tar", retentionTestNow.Add(-time.Hour))
	var stream bytes.Buffer
	svc.Logger = obs.New(&stream, obs.LevelInfo)

	if res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil || res.Result.State != state.RecoveryPassed {
		t.Fatalf("RecoveryTest = %q, %v", res.Result.State, err)
	}
	if got := localCopyOf(t, journal, a).RecoveryValidatorID; got != "own.local.sh" {
		t.Errorf("journal validator = %q, want the file name as before", got)
	}
	if got := eventsOf(t, &stream, obs.EventRecoveryTestStart); len(got) != 1 || got[0]["validator"] != "own" {
		t.Errorf("start events = %v, want validator own as before", got)
	}
}
