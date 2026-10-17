package app

import (
	"context"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
)

// #1221, per validator: the environment a custom validator's script starts
// with is its own env when the entry states one, then the set's
// validator_env (the deployment's folded in), then minimal.

// envRun runs one recovery test of MANIFEST-... through a custom validator
// with the given entry and set-level setting and returns the request that
// reached the stage runner for the validator.
func envRun(t *testing.T, entry config.CustomValidator, setEnv string) StageRequest {
	t.Helper()
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"manifest.local.sh"}}}
	entry.Name, entry.Pattern, entry.BackupSet = "manifest", `^MANIFEST-`, "@set"
	svc, journal, bs := customValidatorService(t, stages, "", entry)
	svc.Config.Sources[0].BackupSets[0].Assurance.RecoveryTest.ValidatorEnv = setEnv
	seedMovableArtifact(t, ctx, journal, bs, "MANIFEST-20260902T153521Z.txt", retentionTestNow.Add(-time.Hour))
	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatalf("RecoveryTest: %v", err)
	}
	if len(stages.asked) != 1 || stages.asked[0].Only != "manifest.local.sh" {
		t.Fatalf("stage requests = %+v, want one for the custom validator", stages.asked)
	}
	return stages.asked[0]
}

func TestIssue1221_ACustomValidatorThatStatesNothingFollowsTheSet(t *testing.T) {
	if envRun(t, config.CustomValidator{}, config.ValidatorEnvMinimal).InheritWorkflowEnv {
		t.Error("an unstated validator under a minimal set got the workflow environment")
	}
	if !envRun(t, config.CustomValidator{}, config.ValidatorEnvWorkflow).InheritWorkflowEnv {
		t.Error("an unstated validator under a set that opted in did not get the workflow environment")
	}
	if envRun(t, config.CustomValidator{}, "").InheritWorkflowEnv {
		t.Error("an unstated validator under an unstated set got the workflow environment")
	}
}

func TestIssue1221_ACustomValidatorCanOptInAlone(t *testing.T) {
	req := envRun(t, config.CustomValidator{Env: config.ValidatorEnvWorkflow}, config.ValidatorEnvMinimal)
	if !req.InheritWorkflowEnv {
		t.Error("a validator that states env: workflow did not get the workflow environment")
	}
	if !req.LocalOnly {
		t.Error("a custom validator must stay a local script")
	}
}

func TestIssue1221_ACustomValidatorCanOptOutUnderASetOptIn(t *testing.T) {
	if envRun(t, config.CustomValidator{Env: config.ValidatorEnvMinimal}, config.ValidatorEnvWorkflow).InheritWorkflowEnv {
		t.Error("a validator that states env: minimal got the workflow environment from its set")
	}
}

func TestIssue1221_ACustomValidatorsOptInDoesNotReachTheSetsOwnScript(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"manifest.local.sh"}}}
	svc, journal, bs := customValidatorService(t, stages, "own",
		config.CustomValidator{Name: "manifest", Pattern: `^MANIFEST-`, BackupSet: "@set", Env: config.ValidatorEnvWorkflow})
	stages.dirs[RecoveryTestSetDir(bs.ID)] = []string{"own.local.sh"}
	seedMovableArtifact(t, ctx, journal, bs, "db-2026-09-02.sql.gz", retentionTestNow.Add(-time.Hour))
	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	if len(stages.asked) != 1 || stages.asked[0].Only != "own.local.sh" {
		t.Fatalf("stage requests = %+v, want the set's own script", stages.asked)
	}
	if stages.asked[0].InheritWorkflowEnv {
		t.Error("another validator's opt-in reached the set's own script")
	}
}

func TestIssue1221_AResolvedValidatorEnvThatIsNotExactlyWorkflowFailsClosed(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"manifest.local.sh"}}}
	svc, journal, bs := customValidatorService(t, stages, "",
		config.CustomValidator{Name: "manifest", Pattern: `^MANIFEST-`, BackupSet: "@set"})
	svc.Config.Sources[0].BackupSets[0].Assurance.RecoveryTest.ValidatorEnv = config.ValidatorEnvWorkflow
	svc.Config.Assurance.Validators[0].Env = "WORKFLOW"
	seedMovableArtifact(t, ctx, journal, bs, "MANIFEST-20260902T153521Z.txt", retentionTestNow.Add(-time.Hour))
	_, _ = svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	for _, r := range stages.asked {
		if r.InheritWorkflowEnv {
			t.Error("a validator env that is not exactly workflow fell through to the set's opt-in")
		}
	}
}
