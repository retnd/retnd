package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// #1222: a validator whose entry pins a script_sha256 runs only the script
// that hashes to it. dirStages reports every script as sha256 "cd" x 32 and
// honours StageRequest.ExpectSHA256 as the stage runner does.

var pinnedScriptHash = strings.Repeat("cd", 32)

func pinnedService(t *testing.T, pin string) (*Service, *dirStages, *state.Journal, config.BackupSet, *bytes.Buffer) {
	t.Helper()
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"manifest.local.sh"}}}
	svc, journal, bs := customValidatorService(t, stages, "", config.CustomValidator{Name: "manifest", Pattern: `^MANIFEST-`, ScriptSHA256: pin})
	var stream bytes.Buffer
	svc.Logger = obs.New(&stream, obs.LevelInfo)
	return svc, stages, journal, bs, &stream
}

func TestPinnedValidator_ARunWhoseScriptMatchesThePinPassesAndAsksForThatHash(t *testing.T) {
	ctx := context.Background()
	svc, stages, journal, bs, _ := pinnedService(t, pinnedScriptHash)
	seedMovableArtifact(t, ctx, journal, bs, "MANIFEST-20260902T153521Z.txt", retentionTestNow.Add(-time.Hour))

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil || res.Result.State != state.RecoveryPassed {
		t.Fatalf("RecoveryTest = %q (%s), %v", res.Result.State, res.Result.Reason, err)
	}
	if len(stages.asked) != 1 || stages.asked[0].ExpectSHA256 != pinnedScriptHash {
		t.Errorf("the run did not ask the runner to hold the script to the pin: %+v", stages.asked)
	}
}

func TestPinnedValidator_AScriptEditedOnDiskIsRefusedBeforeItRuns(t *testing.T) {
	ctx := context.Background()
	pin := strings.Repeat("ab", 32)
	svc, stages, journal, bs, stream := pinnedService(t, pin)
	a := seedMovableArtifact(t, ctx, journal, bs, "MANIFEST-20260902T153521Z.txt", retentionTestNow.Add(-time.Hour))

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.State != state.RecoveryCouldNotComplete {
		t.Fatalf("state = %s, want could_not_complete", res.Result.State)
	}
	for _, want := range []string{"manifest", "edited on disk"} {
		if !strings.Contains(res.Result.Reason, want) {
			t.Errorf("reason %q does not say %q", res.Result.Reason, want)
		}
	}
	if got := stages.ran(); len(got) != 0 {
		t.Errorf("the edited script ran: %v", got)
	}
	if p := localCopyOf(t, journal, a); p.RecoveryState != state.RecoveryCouldNotComplete {
		t.Errorf("recorded state = %q", p.RecoveryState)
	}
	evs := eventsOf(t, stream, "recovery_test_validator_edited")
	if len(evs) != 1 || evs[0]["validator"] != "manifest" || evs[0]["pinned_sha256"] != pin || evs[0]["found_sha256"] != pinnedScriptHash {
		t.Errorf("recovery_test_validator_edited events = %v", evs)
	}
}

// An entry written before pinning carries no hash and keeps working until it
// is next saved. A set's own validator_id is not a custom validator and is
// never pinned.
func TestPinnedValidator_AnEntryWithNoPinAndASetsOwnValidatorRunUnchecked(t *testing.T) {
	ctx := context.Background()
	svc, stages, journal, bs, _ := pinnedService(t, "")
	seedMovableArtifact(t, ctx, journal, bs, "MANIFEST-20260902T153521Z.txt", retentionTestNow.Add(-time.Hour))
	if res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil || res.Result.State != state.RecoveryPassed {
		t.Fatalf("RecoveryTest = %q (%s), %v", res.Result.State, res.Result.Reason, err)
	}
	if len(stages.asked) != 1 || stages.asked[0].ExpectSHA256 != "" {
		t.Errorf("an unpinned entry was held to a hash: %+v", stages.asked)
	}

	own := &dirStages{dirs: map[string][]string{}}
	svc2, journal2, bs2 := customValidatorService(t, own, "own")
	own.dirs[RecoveryTestSetDir(bs2.ID)] = []string{"own.local.sh"}
	seedMovableArtifact(t, ctx, journal2, bs2, "nightly.tar", retentionTestNow.Add(-time.Hour))
	if _, err := svc2.RecoveryTest(ctx, RecoveryTestRequest{Set: bs2.ID}); err != nil {
		t.Fatal(err)
	}
	if len(own.asked) != 1 || own.asked[0].ExpectSHA256 != "" {
		t.Errorf("a set's own validator was held to a hash: %+v", own.asked)
	}
}
