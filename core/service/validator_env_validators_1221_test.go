package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/secretref"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/workflow"
)

// #1221, per validator, at the service boundary: the env a validator is
// written with, what it is read back as, what the audit event says and what
// a validator's stage can see when a recovery test really runs it.

func TestIssue1221_ACustomValidatorIsWrittenMinimalUnlessItSaysWorkflow(t *testing.T) {
	ctx := context.Background()
	svc, configPath, _ := openWorkflowSaveService(t)

	plain, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "plain", Pattern: `^a`, Script: goodValidator})
	if err != nil {
		t.Fatal(err)
	}
	wide, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "wide", Pattern: `^b`, Script: goodValidator, Env: config.ValidatorEnvWorkflow})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Env != config.ValidatorEnvMinimal || wide.Env != config.ValidatorEnvWorkflow {
		t.Errorf("created envs = %q / %q, want minimal / workflow", plain.Env, wide.Env)
	}
	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "env: workflow") || !strings.Contains(string(raw), "env: minimal") {
		t.Errorf("config.yaml should state both choices, so a later set-level opt-in cannot widen an API-made validator:\n%s", raw)
	}
	running := svc.state.Load().inner.Config.Assurance.Validators
	if len(running) != 2 || running[0].Env != config.ValidatorEnvMinimal || running[1].Env != config.ValidatorEnvWorkflow {
		t.Errorf("running validators = %+v", running)
	}
	list, err := svc.ListCustomValidators(ctx, CustomValidatorsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Validators) != 2 || list.Validators[0].Env != config.ValidatorEnvMinimal || list.Validators[1].Env != config.ValidatorEnvWorkflow {
		t.Errorf("list envs = %+v", list.Validators)
	}
	w := CustomValidatorsWire(list)
	if w.Validators[0].Env != config.ValidatorEnvMinimal || w.Validators[1].Env != config.ValidatorEnvWorkflow {
		t.Errorf("wire envs = %q / %q", w.Validators[0].Env, w.Validators[1].Env)
	}
}

func TestIssue1221_ACustomValidatorWithAnEnvThatIsNotExactlyOneOfTheTwoIsRefusedAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	svc, configPath, root := openWorkflowSaveService(t)
	before, _ := os.ReadFile(configPath)
	for _, bad := range []string{"Workflow", "WORKFLOW", " workflow", "workflow ", "all", "secrets", "inherit", "true"} {
		_, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: `^a`, Script: goodValidator, Env: bad})
		if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "env") {
			t.Errorf("create with env %q: err = %v, want an invalid request naming env", bad, err)
		}
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Errorf("a refused env still changed config.yaml:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(root, config.CustomValidatorDir, "v.local.sh")); err == nil {
		t.Error("a refused env still wrote the script")
	}
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "ok", Pattern: `^a`, Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	keep := validatorScriptOf("ok", []byte(goodValidator)).SHA256
	if _, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "ok", Pattern: `^a`, ScriptSHA256: keep, Env: "Workflow"}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("update with a bad env: err = %v, want an invalid request", err)
	}
	if got := svc.state.Load().inner.Config.Assurance.Validators; len(got) != 1 || got[0].Env != config.ValidatorEnvMinimal {
		t.Errorf("a refused update changed the validator: %+v", got)
	}
}

func TestIssue1221_AnUpdateIsAReplacementSoOmittingEnvMeansMinimal(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: `^a`, Script: goodValidator, Env: config.ValidatorEnvWorkflow}); err != nil {
		t.Fatal(err)
	}
	keep := validatorScriptOf("v", []byte(goodValidator)).SHA256

	v, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: `^a`, ScriptSHA256: keep})
	if err != nil {
		t.Fatal(err)
	}
	if v.Env != config.ValidatorEnvMinimal {
		t.Errorf("an update that left env out kept %q, want minimal: a replacement never keeps the wider value by omission", v.Env)
	}
	v, err = svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: `^a`, ScriptSHA256: keep, Env: config.ValidatorEnvWorkflow})
	if err != nil {
		t.Fatal(err)
	}
	if v.Env != config.ValidatorEnvWorkflow {
		t.Errorf("an update that says workflow left %q", v.Env)
	}
	if got := svc.state.Load().inner.Config.Assurance.Validators; len(got) != 1 || got[0].Env != config.ValidatorEnvWorkflow {
		t.Errorf("running validators = %+v", got)
	}
}

func TestIssue1221_TheListShowsTheEnvAValidatorThatStatesNothingInherits(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := openWorkflowSaveService(t)
	for _, req := range []CreateCustomValidatorRequest{
		{Name: "global", Pattern: `^g`, Script: goodValidator},
		{Name: "scoped", Pattern: `^s`, BackupSetID: "production/alpha", Script: goodValidator},
		{Name: "own", Pattern: `^o`, BackupSetID: "production/alpha", Script: goodValidator},
	} {
		if _, err := svc.CreateCustomValidator(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	cfg := svc.state.Load().inner.Config
	cfg.Assurance.Validators[0].Env, cfg.Assurance.Validators[1].Env = "", ""
	cfg.Assurance.Validators[2].Env = config.ValidatorEnvMinimal
	cfg.Assurance.RecoveryTest.ValidatorEnv = config.ValidatorEnvWorkflow
	cfg.Sources[0].BackupSets[0].Assurance.RecoveryTest.ValidatorEnv = config.ValidatorEnvWorkflow

	list, err := svc.ListCustomValidators(ctx, CustomValidatorsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range list.Validators {
		got[v.Name] = v.Env
	}
	if got["global"] != config.ValidatorEnvWorkflow || got["scoped"] != config.ValidatorEnvWorkflow || got["own"] != config.ValidatorEnvMinimal {
		t.Errorf("listed envs = %v, want the deployment's, the set's and the validator's own", got)
	}
}

func TestIssue1221_EveryValidatorEventCarriesTheEnv(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := openWorkflowSaveService(t)
	var stream bytes.Buffer
	svc.logger = obs.New(&stream, obs.LevelInfo)

	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: `^a`, Script: goodValidator, Env: config.ValidatorEnvWorkflow}); err != nil {
		t.Fatal(err)
	}
	keep := validatorScriptOf("v", []byte(goodValidator)).SHA256
	if _, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: `^a`, ScriptSHA256: keep}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RemoveCustomValidator(ctx, "v"); err != nil {
		t.Fatal(err)
	}
	for event, want := range map[string]string{
		"validator_uploaded": config.ValidatorEnvWorkflow,
		"validator_updated":  config.ValidatorEnvMinimal,
		"validator_removed":  config.ValidatorEnvMinimal,
	} {
		evs := validatorEvents(t, &stream, event)
		if len(evs) != 1 {
			t.Errorf("%s events = %d, want 1\n%s", event, len(evs), stream.String())
			continue
		}
		if evs[0]["env"] != want {
			t.Errorf("%s env = %v, want %s: %v", event, evs[0]["env"], want, evs[0])
		}
	}
}

// What a validator's stage can see, through a real recovery test: the stub
// runner hands back the request the engine built, and stageEnvironment is the
// function the real runner builds the environment with.

func envNamesOfValidatorRun(t *testing.T, validatorEnv, setEnv string) []string {
	t.Helper()
	secretFile := filepath.Join(symlinkFreeTempDir(t), "token")
	if err := os.WriteFile(secretFile, []byte("s3cr3t-token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wfEnv, err := workflow.NewEnvironment(workflow.SanitizedBaseline(), []workflow.EnvVar{
		{Name: "BACKUP_DB_PASSWORD", Value: "hunter2-literal"},
		{Name: "BACKUP_API_TOKEN", Secret: secretref.Ref{File: secretFile}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true, cfg: func(c *config.Config) {
		requireRecoveryTest(c)
		c.Sources[0].BackupSets[0].WorkflowEnvironment = wfEnv
		c.Sources[0].BackupSets[0].Assurance.RecoveryTest.ValidatorEnv = setEnv
		c.Assurance.Validators = []config.ResolvedCustomValidator{{
			Name: "dumps", Pattern: `\.zst$`, Regexp: regexp.MustCompile(`\.zst$`), ScriptFile: "dumps.local.sh", Env: validatorEnv,
		}}
	}})
	stages := &stubStages{script: "dumps.local.sh"}
	withStages(f, stages)
	set, _ := model.NewBackupSetID("production", "postgres")
	a := seedComplete(t, f, "away.zst", state.MediumLocal)
	res, err := f.b.RunRecoveryTest(context.Background(), set, RecoveryTestOptions{ArtifactID: a.String()})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != state.RecoveryPassed {
		t.Fatalf("State = %q (%s), want the test to reach the validator", res.State, res.Reason)
	}
	if len(stages.reqs) != 1 {
		t.Fatalf("validator runs = %d, want 1", len(stages.reqs))
	}
	env, err := stageEnvironment(stages.reqs[0])
	if err != nil {
		t.Fatal(err)
	}
	return env.Names()
}

func TestIssue1221_AMinimalValidatorCannotSeeTheBackupSecretsAndAWorkflowOneCan(t *testing.T) {
	for name, tc := range map[string]struct {
		validatorEnv, setEnv string
		wantSecrets          bool
	}{
		"unstated validator, minimal set":             {"", config.ValidatorEnvMinimal, false},
		"minimal validator, minimal set":              {config.ValidatorEnvMinimal, config.ValidatorEnvMinimal, false},
		"workflow validator, minimal set":             {config.ValidatorEnvWorkflow, config.ValidatorEnvMinimal, true},
		"minimal validator under a set opt-in":        {config.ValidatorEnvMinimal, config.ValidatorEnvWorkflow, false},
		"unstated validator under a set opt-in":       {"", config.ValidatorEnvWorkflow, true},
		"a misspelt validator under a set opt-in":     {"Workflow", config.ValidatorEnvWorkflow, false},
		"workflow validator, misspelt set":            {config.ValidatorEnvWorkflow, "all", true},
		"unstated validator, misspelt set":            {"", "all", false},
		"unstated validator, nothing says anything":   {"", "", false},
		"minimal validator, nothing else says a word": {config.ValidatorEnvMinimal, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			names := envNamesOfValidatorRun(t, tc.validatorEnv, tc.setEnv)
			for _, secret := range []string{"BACKUP_DB_PASSWORD", "BACKUP_API_TOKEN"} {
				if got := slices.Contains(names, secret); got != tc.wantSecrets {
					t.Errorf("%s visible = %v, want %v (names %v)", secret, got, tc.wantSecrets, names)
				}
			}
			if !slices.Contains(names, "RECOVERY_TEST_FILE") || !slices.Contains(names, "RECOVERY_TEST_VALIDATOR") || !slices.Contains(names, "PATH") {
				t.Errorf("the validator lost the variables it always gets: %v", names)
			}
		})
	}
}
