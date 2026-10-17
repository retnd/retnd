package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/secretref"
	"github.com/retnd/retnd/core/internal/workflow"
	"github.com/retnd/retnd/core/internal/workflowexec"
	"github.com/retnd/retnd/core/internal/workflowrun"
)

// RunStage is how EPIC Q runs an operator's script that is not a backup: a
// recovery test's validator and an assurance notification. These pin that it
// is the real mechanism: the stage directory conventions and custody, the
// shell verification that gates a save, the hash of the bytes that ran, the
// resolved environment, and no run of any kind.

type stageExecutor struct {
	calls   []workflowrun.StepRequest
	exit    int
	output  string
	outcome func(workflowrun.StepRequest) (workflowrun.StepOutcome, error)
}

func (e *stageExecutor) ExecuteStep(_ context.Context, req workflowrun.StepRequest) (workflowrun.StepOutcome, error) {
	e.calls = append(e.calls, req)
	if e.output != "" && req.Sink != nil {
		_ = req.Sink.Chunk(workflowexec.Chunk{Stream: workflowexec.StreamStdout, Seq: 1, Data: []byte(e.output)})
	}
	if e.outcome != nil {
		return e.outcome(req)
	}
	code := e.exit

	return workflowrun.StepOutcome{Disposition: workflowrun.DispositionExited, ExitCode: &code}, nil
}

func stageService(t *testing.T) (*BackupService, string, config.BackupSet, *stageExecutor) {
	t.Helper()
	svc, _, root := openWorkflowSaveService(t)
	exec := &stageExecutor{}
	svc.workflow.engine.Local = exec

	var set config.BackupSet
	for _, src := range svc.state.Load().inner.Config.Sources {
		for _, bs := range src.BackupSets {
			set = bs
		}
	}

	return svc, root, set, exec
}

func TestRunStageRunsTheScriptThroughTheEnginesExecutorWithItsHashAndEnvironment(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, "recovery-test/production/alpha", "10-check.local.sh", "#!/usr/bin/env bash", "echo ok")
	exec.output = "all good\n"

	res, err := svc.RunStage(context.Background(), app.StageRequest{
		Set: set, SetDir: "recovery-test/production/alpha", LocalOnly: true, MaxScripts: 1,
		Vars: map[string]string{"RECOVERY_TEST_FILE": "/staging/artifact"},
	})
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	if !res.Succeeded() || len(res.Scripts) != 1 {
		t.Fatalf("result = %+v", res)
	}
	s := res.Scripts[0]
	if len(s.SHA256) != 64 || s.Name != "10-check.local.sh" || s.Dir != "recovery-test/production/alpha" || string(s.Output) != "all good\n" {
		t.Errorf("script = %+v", s)
	}
	env := strings.Join(exec.calls[0].Environ, "\n")
	if !strings.Contains(env, "RECOVERY_TEST_FILE=/staging/artifact") || !strings.Contains(env, "RETND_BACKUP_SET_ID=production/alpha") {
		t.Errorf("the script was not given the request's variables beside the product's facts:\n%s", env)
	}
}

func TestRunStageWithNoDirectoryIsNotConfiguredNotAFailure(t *testing.T) {
	svc, _, set, exec := stageService(t)
	res, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: "recovery-test/production/alpha"})
	if err != nil || res.Configured || res.Succeeded() || len(exec.calls) != 0 {
		t.Errorf("res=%+v err=%v calls=%d", res, err, len(exec.calls))
	}
}

func TestRunStageRefusesAScriptTheSaveGateWouldRefuse(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, "recovery-test/production/alpha", "10-bad.local.sh", "#!/usr/bin/env bash", `rm -rf /$UNSET_DIR`)
	_, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: "recovery-test/production/alpha"})
	if err == nil || !strings.Contains(err.Error(), "BSH003") {
		t.Errorf("err = %v, want the BSH003 refusal", err)
	}
	if len(exec.calls) != 0 {
		t.Error("a script with an error-severity finding was run")
	}
}

func TestRunStageRefusesARemoteScriptWhenOnlyLocalMakesSense(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, "recovery-test/production/alpha", "10-x.remote.sh", "#!/usr/bin/env bash", "true")
	_, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: "recovery-test/production/alpha", LocalOnly: true})
	// The plan refuses a remote script with no execution connection, and
	// LocalOnly refuses one that has a connection: either way it does not run.
	if err == nil || !strings.Contains(err.Error(), "local.sh") {
		t.Errorf("err = %v", err)
	}
	if len(exec.calls) != 0 {
		t.Error("a remote script ran")
	}
}

func TestRunStageRefusesMoreScriptsThanThePurposeTakes(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, "recovery-test/production/alpha", "10-a.local.sh", "#!/usr/bin/env bash", "true")
	writeHook(t, root, "recovery-test/production/alpha", "20-b.local.sh", "#!/usr/bin/env bash", "true")
	_, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: "recovery-test/production/alpha", MaxScripts: 1})
	if err == nil || len(exec.calls) != 0 {
		t.Errorf("err = %v calls = %d", err, len(exec.calls))
	}
}

func TestRunStageRunsTheSetDirectoryThenTheGlobalOneAndContinuesPastAFailure(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, "assurance-after/sets/production/alpha", "10-set.local.sh", "#!/usr/bin/env bash", "true")
	writeHook(t, root, "assurance-after/global", "10-global.local.sh", "#!/usr/bin/env bash", "true")
	exec.outcome = func(req workflowrun.StepRequest) (workflowrun.StepOutcome, error) {
		code := 0
		if req.Step.ScriptName == "10-set.local.sh" {
			code = 2
		}

		return workflowrun.StepOutcome{Disposition: workflowrun.DispositionExited, ExitCode: &code}, nil
	}

	res, err := svc.RunStage(context.Background(), app.StageRequest{
		Set: set, SetDir: "assurance-after/sets/production/alpha", GlobalDir: "assurance-after/global", ContinueOnFailure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Scripts) != 2 || res.Scripts[0].Name != "10-set.local.sh" || res.Scripts[1].Name != "10-global.local.sh" {
		t.Fatalf("order = %+v", res.Scripts)
	}
	if res.Scripts[0].State != "failed" || res.Scripts[1].State != "success" || res.Succeeded() {
		t.Errorf("states = %s / %s", res.Scripts[0].State, res.Scripts[1].State)
	}
}

func TestRunStageRefusesAReservedVariableName(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, "recovery-test/production/alpha", "10-a.local.sh", "#!/usr/bin/env bash", "true")
	_, err := svc.RunStage(context.Background(), app.StageRequest{
		Set: set, SetDir: "recovery-test/production/alpha", Vars: map[string]string{"RETND_BACKUP_STATUS": "success"},
	})
	if err == nil || len(exec.calls) != 0 {
		t.Errorf("a caller set a built-in: err=%v calls=%d", err, len(exec.calls))
	}
}

func TestRunStageWithoutAnEngineSaysSo(t *testing.T) {
	svc, root, set, _ := stageService(t)
	writeHook(t, root, "recovery-test/production/alpha", "10-a.local.sh", "#!/usr/bin/env bash", "true")
	svc.workflow.engine = nil
	_, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: "recovery-test/production/alpha"})
	if !errors.Is(err, ErrWorkflowsNotWired) {
		t.Errorf("err = %v", err)
	}
}

func TestRunStageDryRunVerifiesAndIdentifiesAndRunsNothing(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, "recovery-test/production/alpha", "10-check.local.sh", "#!/usr/bin/env bash", "echo ok")
	res, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: "recovery-test/production/alpha", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Configured || len(res.Scripts) != 1 || len(res.Scripts[0].SHA256) != 64 || res.Scripts[0].State != "" {
		t.Errorf("result = %+v", res)
	}
	if len(exec.calls) != 0 {
		t.Error("a dry run ran a script")
	}

	// And the shell verification applies to a dry run too, so "configured"
	// never means "a script that would be refused when it ran".
	writeHook(t, root, "recovery-test/production/alpha", "20-bad.local.sh", "#!/usr/bin/env bash", `rm -rf /$UNSET_DIR`)
	if _, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: "recovery-test/production/alpha", DryRun: true}); err == nil {
		t.Error("a dry run accepted a script the save gate would refuse")
	}
}

// #1221: a stage that does not ask for the workflow environment runs with the
// small one, and a secret in the set's environment is neither resolved nor
// handed over. The resolution matters as much as the hand-over: a
// command-sourced secret would otherwise run for a script that never sees it.

func stageSetWithSecrets(t *testing.T, set config.BackupSet) config.BackupSet {
	t.Helper()
	secretFile := filepath.Join(symlinkFreeTempDir(t), "token")
	if err := os.WriteFile(secretFile, []byte("s3cr3t-token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := workflow.NewEnvironment(workflow.SanitizedBaseline(), []workflow.EnvVar{
		{Name: "BACKUP_DB_PASSWORD", Value: "hunter2-literal"},
		{Name: "BACKUP_API_TOKEN", Secret: secretref.Ref{File: secretFile}},
	})
	if err != nil {
		t.Fatal(err)
	}
	set.WorkflowEnvironment = env

	return set
}

func TestIssue1221_ADefaultStageDoesNotSeeTheSetsWorkflowVariablesOrSecrets(t *testing.T) {
	svc, root, set, exec := stageService(t)
	set = stageSetWithSecrets(t, set)
	writeHook(t, root, "recovery-test/production/alpha", "10-check.local.sh", "#!/usr/bin/env bash", "true")

	_, err := svc.RunStage(context.Background(), app.StageRequest{
		Set: set, SetDir: "recovery-test/production/alpha", LocalOnly: true, MaxScripts: 1,
		Vars: map[string]string{"RECOVERY_TEST_FILE": "/staging/artifact"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(exec.calls[0].Environ, "\n")
	for _, leaked := range []string{"BACKUP_DB_PASSWORD", "hunter2-literal", "BACKUP_API_TOKEN", "s3cr3t-token-value"} {
		if strings.Contains(env, leaked) {
			t.Errorf("the default stage environment contains %q:\n%s", leaked, env)
		}
	}
	for _, want := range []string{"RECOVERY_TEST_FILE=/staging/artifact", "RETND_BACKUP_SET_ID=production/alpha", "PATH="} {
		if !strings.Contains(env, want) {
			t.Errorf("the default stage environment is missing %q:\n%s", want, env)
		}
	}
}

func TestIssue1221_TheOptInStageSeesTheWorkflowVariablesAndSecrets(t *testing.T) {
	svc, root, set, exec := stageService(t)
	set = stageSetWithSecrets(t, set)
	writeHook(t, root, "recovery-test/production/alpha", "10-check.local.sh", "#!/usr/bin/env bash", "true")

	_, err := svc.RunStage(context.Background(), app.StageRequest{
		Set: set, SetDir: "recovery-test/production/alpha", LocalOnly: true, MaxScripts: 1,
		InheritWorkflowEnv: true,
		Vars:               map[string]string{"RECOVERY_TEST_FILE": "/staging/artifact"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(exec.calls[0].Environ, "\n")
	for _, want := range []string{"BACKUP_DB_PASSWORD=hunter2-literal", "BACKUP_API_TOKEN=s3cr3t-token-value", "RECOVERY_TEST_FILE=/staging/artifact", "RETND_BACKUP_SET_ID=production/alpha"} {
		if !strings.Contains(env, want) {
			t.Errorf("the opted-in stage environment is missing %q:\n%s", want, env)
		}
	}
}

func TestIssue1221_ADefaultStageNeverResolvesASecretItDoesNotHandOver(t *testing.T) {
	svc, root, set, _ := stageService(t)
	marker := filepath.Join(symlinkFreeTempDir(t), "resolver-ran")
	env, err := workflow.NewEnvironment(workflow.SanitizedBaseline(), []workflow.EnvVar{
		{Name: "FROM_COMMAND", Secret: secretref.Ref{Command: []string{"/bin/sh", "-c", "touch " + marker + "; echo x"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	set.WorkflowEnvironment = env
	writeHook(t, root, "recovery-test/production/alpha", "10-check.local.sh", "#!/usr/bin/env bash", "true")

	if _, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: "recovery-test/production/alpha", LocalOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a secret's command ran for a stage that was never given its value")
	}
}
