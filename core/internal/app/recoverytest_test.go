package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// EPIC Q, #980 as the deployment sees it: Service.RecoveryTest runs the
// recovery-test runner over the set's own configuration, and for an
// incremental set it reads the existing ladder and runs nothing.

func recoveryService(t *testing.T, validator string) (*Service, *state.Journal, config.BackupSet) {
	t.Helper()
	bs := testBackupSet(t, t.TempDir())
	if validator != "" {
		bs.Revalidation = config.Revalidation{
			Interval: config.Duration(24 * time.Hour), MaxPerCycle: 10, Hash: true,
			Command: &config.Command{Executable: validator, Timeout: config.Duration(5 * time.Second)},
		}
	}
	cfg := testConfig(t, testSource("production", bs))
	journal := openJournal(t)
	svc := New(cfg, journal, newFakeTransport(), nil)
	svc.Now = fixedNow(retentionTestNow)
	svc.RecoveryStagingRoot = t.TempDir()
	return svc, journal, bs
}

func writeValidator(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "check.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRecoveryTest_PassesOnALocalCopyAndLeavesNoStagingBehind(t *testing.T) {
	ctx := context.Background()
	svc, journal, bs := recoveryService(t, writeValidator(t, "test -s \"$1\"\n"))
	a := seedMovableArtifact(t, ctx, journal, bs, "rt.dump", retentionTestNow.Add(-time.Hour))

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatalf("RecoveryTest: %v", err)
	}
	if res.Result.State != state.RecoveryPassed {
		t.Fatalf("result = %q (%s)", res.Result.State, res.Result.Reason)
	}
	p := localCopyOf(t, journal, a)
	if p.RecoveryState != state.RecoveryPassed || p.RecoveryDigestMatched == nil || !*p.RecoveryDigestMatched {
		t.Errorf("record = %q / %v", p.RecoveryState, p.RecoveryDigestMatched)
	}
	entries, _ := os.ReadDir(svc.RecoveryStagingRoot)
	if len(entries) != 0 {
		t.Errorf("%d entries left in the staging root after a successful test", len(entries))
	}
	// The scrub claim is untouched: nothing here ran a scrub.
	if p.AssuranceState != state.AssuranceUnknown {
		t.Errorf("a recovery test moved the scrub claim to %q", p.AssuranceState)
	}
}

func TestRecoveryTest_ASetWithNoValidatorReadsNotConfigured(t *testing.T) {
	ctx := context.Background()
	svc, journal, bs := recoveryService(t, "")
	a := seedMovableArtifact(t, ctx, journal, bs, "nv.dump", retentionTestNow.Add(-time.Hour))
	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.State != state.RecoveryNotConfigured {
		t.Errorf("State = %q", res.Result.State)
	}
	if p := localCopyOf(t, journal, a); p.RecoveryTestedAt != nil {
		t.Errorf("an age appeared on a claim that never ran: %v", p.RecoveryTestedAt)
	}
}

func TestRecoveryTest_ABadCopyIsAFailureEvenWithAValidatorThatAlwaysSaysYes(t *testing.T) {
	ctx := context.Background()
	svc, journal, bs := recoveryService(t, writeValidator(t, "exit 0\n"))
	a := seedMovableArtifact(t, ctx, journal, bs, "bad.dump", retentionTestNow.Add(-time.Hour))
	if err := os.WriteFile(filepath.Join(bs.LocalPath, "bad.dump"), []byte("rotted"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.State != state.RecoveryFailed {
		t.Errorf("State = %q: the digest gate did not stop a yes-man validator", res.Result.State)
	}
	if p := localCopyOf(t, journal, a); p.RecoveryState != state.RecoveryFailed || p.RecoveryValidatorID != "" {
		t.Errorf("record = %q / validator %q", p.RecoveryState, p.RecoveryValidatorID)
	}
}

func TestRecoveryTest_AnIncrementalSetReadsItsExistingLevelAndRunsNothing(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	bs.Engine = model.EngineKopia
	bs.UUID = "6f9619ff-8b86-d011-b42d-00c04fc964ff"
	svc, _, _ := recoveryService(t, "")
	svc.Config = testConfig(t, testSource("production", bs))
	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatalf("RecoveryTest: %v", err)
	}
	if !res.ReadOnly {
		t.Error("an engine: kopia set ran a test instead of reading its achieved level")
	}
	if res.Claim.State != state.RecoveryNotConfigured {
		t.Errorf("claim = %q, want not_configured for a set with no drill cadence and no drill", res.Claim.State)
	}
}

func TestRecoveryTest_ATestOfAnUnconfiguredSetIsRefused(t *testing.T) {
	svc, _, _ := recoveryService(t, "")
	other, _ := model.NewBackupSetID("production", "nope")
	if _, err := svc.RecoveryTest(context.Background(), RecoveryTestRequest{Set: other}); err == nil {
		t.Error("a recovery test of a set that is not configured succeeded")
	}
}

func TestRecoveryTestTick_RunsOnlyForSetsThatDeclareAValidatorAndAreDue(t *testing.T) {
	ctx := context.Background()
	svc, journal, bs := recoveryService(t, writeValidator(t, "exit 0\n"))
	a := seedMovableArtifact(t, ctx, journal, bs, "tick.dump", retentionTestNow.Add(-time.Hour))

	svc.RecoveryTestTick(ctx)
	p := localCopyOf(t, journal, a)
	if p.RecoveryState != state.RecoveryPassed {
		t.Fatalf("after the tick: %q", p.RecoveryState)
	}
	first := *p.RecoveryTestedAt

	// Not due again: a second tick at the same instant tests nothing.
	svc.Now = fixedNow(retentionTestNow.Add(time.Hour))
	svc.RecoveryTestTick(ctx)
	if got := localCopyOf(t, journal, a).RecoveryTestedAt; !got.Equal(first) {
		t.Errorf("a copy that was not due was tested again: %v -> %v", first, got)
	}

	// Past the cadence, the pass is marked overdue and tested again.
	svc.Now = fixedNow(retentionTestNow.Add(DefaultRecoveryTestCadence + time.Hour))
	svc.RecoveryTestTick(ctx)
	if got := localCopyOf(t, journal, a); !got.RecoveryTestedAt.After(first) || got.RecoveryState != state.RecoveryPassed {
		t.Errorf("after the cadence: %q tested %v", got.RecoveryState, got.RecoveryTestedAt)
	}
}

func TestRecoveryTestTick_ASetWithoutAValidatorIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	svc, journal, bs := recoveryService(t, "")
	a := seedMovableArtifact(t, ctx, journal, bs, "none.dump", retentionTestNow.Add(-time.Hour))
	svc.RecoveryTestTick(ctx)
	if p := localCopyOf(t, journal, a); p.RecoveryState != state.RecoveryUnknown {
		t.Errorf("a set with no validator was touched: %q", p.RecoveryState)
	}
}

func TestRecoveryTest_StagesOnTheBackupSetsOwnDiskByDefaultAndNotInMemory(t *testing.T) {
	ctx := context.Background()
	seen := filepath.Join(t.TempDir(), "path-seen-by-the-validator")
	svc, journal, bs := recoveryService(t, writeValidator(t, "printf '%s' \"$1\" > "+seen+"\n"))
	svc.RecoveryStagingRoot = "" // the default
	seedMovableArtifact(t, ctx, journal, bs, "where.dump", retentionTestNow.Add(-time.Hour))

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.State != state.RecoveryPassed {
		t.Fatalf("State = %q (%s)", res.Result.State, res.Result.Reason)
	}
	raw, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("the validator never recorded its argument: %v", err)
	}
	want := filepath.Join(bs.LocalPath, ".retnd-recovery-test") + string(filepath.Separator)
	if !strings.HasPrefix(string(raw), want) {
		t.Errorf("the validator was handed %q, want a path under %q: the default staging place is the set's own disk", raw, want)
	}
	entries, _ := os.ReadDir(filepath.Join(bs.LocalPath, ".retnd-recovery-test"))
	if len(entries) != 0 {
		t.Errorf("%d entries left in the set's staging directory", len(entries))
	}
}
