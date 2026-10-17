package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/google/uuid"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/workflow"
	"github.com/retnd/retnd/core/internal/workflowlint"
	"github.com/retnd/retnd/core/internal/workflowrun"
)

// RunStage is app.StageRunner over EPIC L's engine (EPIC Q, #978 and #980).
//
// It builds a plan the way a backup's does (snapshotWorkflow): the stage
// directories are resolved inside the approved root and refused if they are
// symlinks, writable by another account or outside it, every script is
// discovered by the same naming rule, read once and hashed, and the plan is
// the only authority for what runs. It then runs the scripts through
// workflowrun.Engine.RunHooks, which is the same executors and the same
// resolved environment but no run: no lock on the set, no cleanup
// obligation, no row.
//
// Two things are added that a backup's snapshot does not do. The scripts are
// verified before anything runs, by the same workflowlint rules that gate a
// workflow save: a script with a parse error or an error-severity finding is
// refused here as it would be refused there, because a hook that would not
// have been allowed to be saved is not one to run at 3am on a copy of the
// operator's data. And the plan's spool is a throwaway under the state
// directory's own spool root, removed when the stage ends.
func (b *BackupService) RunStage(ctx context.Context, req app.StageRequest) (app.StageResult, error) {
	st := b.state.Load()
	if st == nil {
		return app.StageResult{}, app.ErrStageNotConfigured
	}
	cfg := st.inner.Config

	if cfg.Workflows.Root == "" {
		return app.StageResult{}, app.ErrStageNotConfigured
	}
	root, err := workflow.NewRoot(cfg.Workflows.Root)
	if err != nil {
		return app.StageResult{}, err
	}

	var stages []workflow.StageSpec
	dirOf := map[workflow.Scope]string{}
	for _, c := range []struct {
		scope workflow.Scope
		dir   string
	}{{workflow.ScopeSet, req.SetDir}, {workflow.ScopeGlobal, req.GlobalDir}} {
		if c.dir == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root.Path(), c.dir)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			return app.StageResult{}, fmt.Errorf("service: the stage directory %q could not be examined: %w", c.dir, err)
		}
		stages = append(stages, workflow.StageSpec{Scope: c.scope, Phase: workflow.PhaseAfter, Dir: c.dir})
		dirOf[c.scope] = c.dir
	}
	if len(stages) == 0 {
		return app.StageResult{}, nil
	}

	spoolRoot := cfg.WorkflowSpoolDir()
	if spoolRoot == "" {
		return app.StageResult{}, errors.New("service: scripts are configured and no state database is, so there is nowhere to keep the captured copy of them")
	}
	if err := os.MkdirAll(spoolRoot, 0o700); err != nil {
		return app.StageResult{}, fmt.Errorf("service: preparing a workflow spool at %s: %w", spoolRoot, err)
	}
	spool, err := os.MkdirTemp(spoolRoot, "stage-")
	if err != nil {
		return app.StageResult{}, fmt.Errorf("service: preparing a workflow spool at %s: %w", spoolRoot, err)
	}
	defer os.RemoveAll(spool) //nolint:errcheck // a leftover spool is inert and the next start sweeps the spool root

	env, err := stageEnvironment(req)
	if err != nil {
		return app.StageResult{}, err
	}

	var execRef string
	if req.Set.Workflow != nil {
		execRef = req.Set.Workflow.RemoteExecConnectionRef
	}

	plan, err := workflow.Snapshot(workflow.SnapshotRequest{
		RunID:                   "stage_" + uuid.New().String(),
		BackupSetID:             req.Set.ID,
		Root:                    root,
		Stages:                  stages,
		Env:                     env,
		Timeout:                 cfg.EffectiveScriptTimeout(&req.Set),
		RemoteExecConnectionRef: execRef,
		MaxScriptSize:           cfg.EffectiveMaxScriptSize(),
		SpoolRoot:               spool,
		Only:                    req.Only,
	})
	if err != nil {
		return app.StageResult{}, err
	}

	steps := plan.Steps()
	if req.Only != "" && len(steps) == 0 {
		// A purpose that was told which script to run never falls back to
		// another, and never reads an empty plan as "nothing configured"
		// (#1204).
		return app.StageResult{}, fmt.Errorf("%w: %s", app.ErrStageScriptMissing, req.Only)
	}
	if req.MaxScripts > 0 && len(steps) > req.MaxScripts {
		return app.StageResult{}, fmt.Errorf("service: %d scripts were found and this purpose takes at most %d; which one is meant is not something to guess", len(steps), req.MaxScripts)
	}
	for _, step := range steps {
		if req.LocalOnly && step.Target != workflow.TargetLocal {
			return app.StageResult{}, fmt.Errorf("service: %s is a %s script and this purpose runs against a file on this machine, so only NAME.local.sh is accepted", step.ScriptName, step.Target)
		}
		script, err := plan.OpenScript(step.ID)
		if err != nil {
			return app.StageResult{}, err
		}
		if report := workflowlint.Report(ctx, step.ScriptName, script.Body); report.Blocks() {
			return app.StageResult{}, fmt.Errorf("service: %s was refused before it ran: %s", step.ScriptName, describeBlocking(report))
		}
	}

	if req.DryRun {
		out := app.StageResult{Configured: true}
		for _, step := range steps {
			out.Scripts = append(out.Scripts, app.StageScript{
				Dir: dirOf[step.Scope], Name: step.ScriptName, SHA256: step.ScriptSHA256, Target: string(step.Target),
			})
		}

		return out, nil
	}

	rt := b.runtime()
	if rt == nil || rt.engine == nil {
		return app.StageResult{}, ErrWorkflowsNotWired
	}
	res, err := rt.engine.RunHooks(ctx, plan, workflowrun.HookRunRequest{
		ContinueOnFailure: req.ContinueOnFailure,
		MaxOutputBytes:    req.MaxOutputBytes,
		Facts:             workflowFacts(req.Set),
	})
	if err != nil {
		return app.StageResult{}, err
	}

	out := app.StageResult{Configured: true}
	for _, r := range res.Steps {
		out.Scripts = append(out.Scripts, app.StageScript{
			Dir:             dirOf[r.Step.Scope],
			Name:            r.Step.ScriptName,
			SHA256:          r.Step.ScriptSHA256,
			Target:          string(r.Step.Target),
			State:           string(r.State),
			ExitCode:        r.Outcome.ExitCode,
			Disposition:     string(r.Outcome.Disposition),
			Termination:     string(r.Outcome.Certainty),
			Detail:          r.Outcome.Detail,
			Output:          r.Output,
			OutputTruncated: r.OutputTruncated,
		})
	}

	return out, nil
}

func describeBlocking(r workflowlint.ScriptReport) string {
	if r.ParseError != nil {
		return "it does not parse as a shell program"
	}
	blocking := r.Blocking()
	if len(blocking) == 0 {
		return "it is not a shell program this product would run"
	}

	return fmt.Sprintf("%s: %s", blocking[0].Code, blocking[0].Message)
}

// stageEnvironment builds a stage's environment. It starts from the sanitized
// baseline alone and layers the set's merged workflow environment over it
// only when the request asks (InheritWorkflowEnv), so a stage that did not
// ask never has a workflow variable, and a secret's file or command is never
// resolved on its behalf. The request's own variables go on top. The names
// are validated by the environment model, so a RETND_ name is refused here as
// it is in configuration.
func stageEnvironment(req app.StageRequest) (workflow.Environment, error) {
	layers := [][]workflow.EnvVar{workflow.SanitizedBaseline()}
	if req.InheritWorkflowEnv {
		layers = [][]workflow.EnvVar{req.Set.WorkflowEnvironment.Vars()}
	}

	if len(req.Vars) > 0 {
		names := make([]string, 0, len(req.Vars))
		for name := range req.Vars {
			names = append(names, name)
		}
		sort.Strings(names)

		extra := make([]workflow.EnvVar, 0, len(names))
		for _, name := range names {
			extra = append(extra, workflow.EnvVar{Name: name, Value: req.Vars[name]})
		}
		layers = append(layers, extra)
	}

	return workflow.NewEnvironment(layers...)
}
