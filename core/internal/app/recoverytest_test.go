package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// EPIC Q, #980 as the deployment sees it: Service.RecoveryTest runs the
// recovery-test runner with the operator's validator as a workflow script
// behind the stage runner, and for an incremental set it reads the existing
// ladder and runs nothing.

// validatorStages is the service layer's script runner as a test sees it. A
// request for the set's recovery-test directory is answered as the validator;
// a request for an assurance-after directory is a notification and is
// recorded. For the validator it reads the recovered file at the moment the
// script would have, because "the script was handed bytes that match" is the
// claim.
type validatorStages struct {
	mu         sync.Mutex
	configured bool
	outcome    StageScript
	seenFile   string
	seenBytes  []byte
	validated  []StageRequest
	notified   []StageRequest
	dryRuns    []StageRequest

	// delay is how long a validator takes, and beforeReturn runs once the
	// script has "finished" and before its outcome is handed back, so a test
	// can change the world while a test is in flight.
	delay        time.Duration
	beforeReturn func()
}

func (v *validatorStages) RunStage(_ context.Context, req StageRequest) (StageResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if strings.HasPrefix(req.SetDir, "assurance-after") {
		v.notified = append(v.notified, req)

		return StageResult{Configured: true, Scripts: []StageScript{{Name: "10-notify.local.sh", State: "success"}}}, nil
	}

	if !v.configured {
		return StageResult{}, nil
	}
	if req.Only != "" && req.Only != v.outcome.Name {
		return StageResult{}, fmt.Errorf("%w: %s", ErrStageScriptMissing, req.Only)
	}
	if req.DryRun {
		v.dryRuns = append(v.dryRuns, req)
		return StageResult{Configured: true, Scripts: []StageScript{{Name: v.outcome.Name, SHA256: v.outcome.SHA256}}}, nil
	}
	v.validated = append(v.validated, req)
	v.seenFile = req.Vars["RECOVERY_TEST_FILE"]
	v.seenBytes, _ = os.ReadFile(v.seenFile)
	if v.delay > 0 {
		time.Sleep(v.delay)
	}
	if v.beforeReturn != nil {
		v.beforeReturn()
	}

	return StageResult{Configured: true, Scripts: []StageScript{v.outcome}}, nil
}

func (v *validatorStages) events() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]string, 0, len(v.notified))
	for _, c := range v.notified {
		out = append(out, c.Vars["ASSURANCE_EVENT"])
	}

	return out
}

func exitCode(n int) *int { return &n }

func accepts() *validatorStages {
	return &validatorStages{configured: true, outcome: StageScript{
		Name: "10-check.local.sh", SHA256: strings.Repeat("ab", 32), Target: "local",
		State: "success", Disposition: "exited", ExitCode: exitCode(0),
	}}
}

func recoveryService(t *testing.T, stages *validatorStages) (*Service, *state.Journal, config.BackupSet) {
	t.Helper()
	bs := testBackupSet(t, t.TempDir())
	cfg := testConfig(t, testSource("production", bs))
	journal := openJournal(t)
	// Production resolves the policy when it loads the file; a recovery test
	// runs only for a set whose resolved mode asks for one.
	cfg.Sources[0].BackupSets[0].Assurance.RecoveryTest.Mode = config.RecoveryTestRequired
	svc := New(cfg, journal, newFakeTransport(), nil)
	svc.Now = fixedNow(retentionTestNow)
	svc.RecoveryStagingRoot = t.TempDir()
	if stages != nil {
		svc.Stages = stages
	}
	return svc, journal, bs
}

func TestRecoveryTest_PassesOnALocalCopyAndLeavesNoStagingBehind(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
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
	if p.RecoveryValidatorID != "10-check.local.sh" || p.RecoveryValidatorHash != strings.Repeat("ab", 32) {
		t.Errorf("validator = %q / %q: the identity is what the plan that ran the script reported", p.RecoveryValidatorID, p.RecoveryValidatorHash)
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

func TestRecoveryTest_TheValidatorIsAskedAsALocalSingleScriptStageAndHandedTheMatchingBytes(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	seedMovableArtifact(t, ctx, journal, bs, "what.dump", retentionTestNow.Add(-time.Hour))

	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	if len(stages.validated) != 1 {
		t.Fatalf("validator runs = %d", len(stages.validated))
	}
	req := stages.validated[0]
	if req.SetDir != "recovery-test/production/postgres-primary" || !req.LocalOnly || req.MaxScripts != 1 || req.GlobalDir != "" {
		t.Errorf("request = %+v", req)
	}
	if string(stages.seenBytes) != "the bytes of what.dump" {
		t.Errorf("the validator was handed %q", stages.seenBytes)
	}
	if req.Vars["RECOVERY_TEST_DIGEST"] == "" || req.Vars["RECOVERY_TEST_SIZE"] != "22" {
		t.Errorf("variables = %v", req.Vars)
	}
	for name := range req.Vars {
		if strings.HasPrefix(name, "RETND_") {
			t.Errorf("the request sets %s, which is the product's own namespace", name)
		}
	}
}

func TestRecoveryTest_ASetWithNoValidatorReadsNotConfigured(t *testing.T) {
	ctx := context.Background()
	for name, stages := range map[string]*validatorStages{"no stage runner": nil, "no directory": {configured: false}} {
		t.Run(name, func(t *testing.T) {
			svc, journal, bs := recoveryService(t, stages)
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
		})
	}
}

func TestRecoveryTest_ABadCopyIsAFailureEvenWithAValidatorThatAlwaysSaysYes(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
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
	if len(stages.validated) != 0 {
		t.Error("the validator was run against bytes that did not match")
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
	stages := accepts()
	svc, _, _ := recoveryService(t, stages)
	svc.Config = testConfig(t, testSource("production", bs))
	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatalf("RecoveryTest: %v", err)
	}
	if !res.ReadOnly || len(stages.validated) != 0 {
		t.Errorf("an engine: kopia set ran a test instead of reading its achieved level (readonly=%v, runs=%d)", res.ReadOnly, len(stages.validated))
	}
	if res.Claim.State != state.RecoveryNotConfigured {
		t.Errorf("claim = %q, want not_configured for a set with no drill cadence and no drill", res.Claim.State)
	}
}

func TestRecoveryTest_ATestOfAnUnconfiguredSetIsRefused(t *testing.T) {
	svc, _, _ := recoveryService(t, nil)
	other, _ := model.NewBackupSetID("production", "nope")
	if _, err := svc.RecoveryTest(context.Background(), RecoveryTestRequest{Set: other}); err == nil {
		t.Error("a recovery test of a set that is not configured succeeded")
	}
}

func TestRecoveryTestTick_RunsOnlyForSetsThatDeclareAValidatorAndAreDue(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	a := seedMovableArtifact(t, ctx, journal, bs, "tick.dump", retentionTestNow.Add(-time.Hour))

	svc.RecoveryTestTick(ctx)
	p := localCopyOf(t, journal, a)
	if p.RecoveryState != state.RecoveryPassed {
		t.Fatalf("after the tick: %q", p.RecoveryState)
	}
	first := *p.RecoveryTestedAt

	// Not due again: a second tick an hour later tests nothing.
	svc.Now = fixedNow(retentionTestNow.Add(time.Hour))
	svc.RecoveryTestTick(ctx)
	if got := localCopyOf(t, journal, a).RecoveryTestedAt; !got.Equal(first) {
		t.Errorf("a copy that was not due was tested again: %v -> %v", first, got)
	}

	// Past the cadence, the pass is marked overdue and tested again.
	svc.Now = fixedNow(retentionTestNow.Add(config.DefaultRecoveryTestCadence + time.Hour))
	svc.RecoveryTestTick(ctx)
	if got := localCopyOf(t, journal, a); !got.RecoveryTestedAt.After(first) || got.RecoveryState != state.RecoveryPassed {
		t.Errorf("after the cadence: %q tested %v", got.RecoveryState, got.RecoveryTestedAt)
	}
}

func TestRecoveryTestTick_ASetWithoutAValidatorIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	svc, journal, bs := recoveryService(t, &validatorStages{configured: false})
	a := seedMovableArtifact(t, ctx, journal, bs, "none.dump", retentionTestNow.Add(-time.Hour))
	svc.RecoveryTestTick(ctx)
	if p := localCopyOf(t, journal, a); p.RecoveryState != state.RecoveryUnknown {
		t.Errorf("a set with no validator was touched: %q", p.RecoveryState)
	}
}

func TestRecoveryTest_StagesOnTheBackupSetsOwnDiskByDefaultAndNotInMemory(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	svc.RecoveryStagingRoot = "" // the default
	seedMovableArtifact(t, ctx, journal, bs, "where.dump", retentionTestNow.Add(-time.Hour))

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.State != state.RecoveryPassed {
		t.Fatalf("State = %q (%s)", res.Result.State, res.Result.Reason)
	}
	want := filepath.Join(bs.LocalPath, ".retnd-recovery-test") + string(filepath.Separator)
	if !strings.HasPrefix(stages.seenFile, want) {
		t.Errorf("the validator was handed %q, want a path under %q: the default staging place is the set's own disk", stages.seenFile, want)
	}
	entries, _ := os.ReadDir(filepath.Join(bs.LocalPath, ".retnd-recovery-test"))
	if len(entries) != 0 {
		t.Errorf("%d entries left in the set's staging directory", len(entries))
	}
}

func TestRecoveryTestTick_SweepsAStagingDirectoryACrashLeftBehind(t *testing.T) {
	ctx := context.Background()
	svc, journal, bs := recoveryService(t, &validatorStages{configured: false})
	svc.RecoveryStagingRoot = ""
	seedMovableArtifact(t, ctx, journal, bs, "orphan.dump", retentionTestNow.Add(-time.Hour))
	root := filepath.Join(bs.LocalPath, ".retnd-recovery-test")
	orphan := filepath.Join(root, "retnd-recovery-test-crashed")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}
	svc.Now = fixedNow(time.Now())
	svc.RecoveryTestTick(ctx)
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("the orphan survived the tick: %v", err)
	}
}

// --- telling the operator ---

func TestRecoveryTest_AFailureRunsTheAssuranceScriptOnceAndARecoveryRunsItOnceMore(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	seedMovableArtifact(t, ctx, journal, bs, "ep.dump", retentionTestNow.Add(-time.Hour))

	stages.outcome.ExitCode = exitCode(7)
	stages.outcome.State = "failed"
	for i := 0; i < 3; i++ {
		if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if got := stages.events(); len(got) != 1 || got[0] != AssuranceRecoveryTestFailed {
		t.Fatalf("scripts run = %v, want exactly one recovery_test_failed across three failures", got)
	}
	call := stages.notified[0]
	if !strings.Contains(call.Vars["ASSURANCE_SUMMARY"], "did not pass its recovery test") || call.Vars["ASSURANCE_ARTIFACT"] == "" {
		t.Errorf("variables = %v", call.Vars)
	}

	stages.outcome.ExitCode = exitCode(0)
	stages.outcome.State = "success"
	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	if got := stages.events(); len(got) != 2 || got[1] != AssuranceRecoveryTestRecovered {
		t.Errorf("scripts run = %v", got)
	}
}

func TestRecoveryTest_ARefusalOrAnIncompleteAttemptIsNotAnnouncedAsAFailure(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	stages.outcome.Disposition = "transport_lost"
	stages.outcome.ExitCode = nil
	svc, journal, bs := recoveryService(t, stages)
	seedMovableArtifact(t, ctx, journal, bs, "inc.dump", retentionTestNow.Add(-time.Hour))
	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.State != state.RecoveryCouldNotComplete {
		t.Fatalf("State = %q", res.Result.State)
	}
	if got := stages.events(); len(got) != 0 {
		t.Errorf("an attempt that could not complete was announced as %v: nothing is known about the copy", got)
	}
}

// #1221: a validator stage starts with the small environment. Only the
// operator's validator_env: workflow hands it the set's workflow variables.

func TestIssue1221_TheValidatorDoesNotInheritTheWorkflowEnvironmentByDefault(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	seedMovableArtifact(t, ctx, journal, bs, "env.dump", retentionTestNow.Add(-time.Hour))

	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	if len(stages.validated) != 1 {
		t.Fatalf("validator runs = %d", len(stages.validated))
	}
	req := stages.validated[0]
	if req.InheritWorkflowEnv {
		t.Error("the validator was asked to run with the workflow environment without an opt-in")
	}
	if req.Vars["RECOVERY_TEST_FILE"] == "" || req.Vars["RECOVERY_TEST_ARTIFACT"] == "" || req.Vars["RECOVERY_TEST_DIGEST"] == "" {
		t.Errorf("the RECOVERY_TEST_* variables are still sent: %v", req.Vars)
	}
}

func TestIssue1221_TheOptInHandsTheValidatorTheWorkflowEnvironment(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	svc.Config.Sources[0].BackupSets[0].Assurance.RecoveryTest.ValidatorEnv = config.ValidatorEnvWorkflow
	seedMovableArtifact(t, ctx, journal, bs, "env.dump", retentionTestNow.Add(-time.Hour))

	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	if len(stages.validated) != 1 || !stages.validated[0].InheritWorkflowEnv {
		t.Fatalf("validator runs = %+v, want one with the opt-in", stages.validated)
	}
}

func TestIssue1221_TheDryRunAndTheRealRunAgreeOnTheEnvironment(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	seedMovableArtifact(t, ctx, journal, bs, "env.dump", retentionTestNow.Add(-time.Hour))
	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	if len(stages.dryRuns) == 0 {
		t.Fatal("no dry run was made")
	}
	for _, r := range stages.dryRuns {
		if r.InheritWorkflowEnv {
			t.Error("the dry run was asked for the workflow environment without an opt-in")
		}
	}
}

func TestIssue1221_AnUnknownValidatorEnvOnTheResolvedPolicyFailsClosed(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	svc.Config.Sources[0].BackupSets[0].Assurance.RecoveryTest.ValidatorEnv = "WORKFLOW"
	seedMovableArtifact(t, ctx, journal, bs, "env.dump", retentionTestNow.Add(-time.Hour))

	_, _ = svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	for _, r := range stages.validated {
		if r.InheritWorkflowEnv {
			t.Error("a value that is not exactly workflow opened the workflow environment")
		}
	}
}

func TestIssue1221_TheAssuranceNotificationKeepsTheHooksEnvironment(t *testing.T) {
	stages := accepts()
	svc, _, bs := recoveryService(t, stages)
	svc.notifyAssurance(context.Background(), bs, assuranceFacts{Event: "scrub_failing", Summary: "x"})
	if len(stages.notified) != 1 || !stages.notified[0].InheritWorkflowEnv {
		t.Errorf("notified = %+v: a notification script is a hook and keeps the workflow environment", stages.notified)
	}
}
