package workflowrun

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/workflow"
	"github.com/retnd/retnd/core/internal/workflowexec"
)

// RunHooks runs a plan's scripts outside a five-stage run (EPIC Q, #978 and
// #980): no obligation, no run row, no set lock, because what it runs is not
// a backup and there is nothing to unwind. It runs the same verified bytes,
// through the same executors, with the same resolved environment, and it
// redacts and bounds what comes back.

func hooksHarness(t *testing.T, scripts ...string) (*Engine, *recorder, *fakeExecutor, workflow.Plan) {
	t.Helper()

	rec := &recorder{}
	local, remote := newExecutors(rec)
	tr := newTree(t, map[stage][]string{setAfter: scripts})
	return &Engine{Local: local, Remote: remote, Now: newClock().Now}, rec, local, tr.snapshot(t, "hooks-run")
}

func TestRunHooksRunsEveryScriptInPlanOrderThroughTheLocalExecutor(t *testing.T) {
	eng, rec, _, plan := hooksHarness(t, "10-second.local.sh", "05-first.local.sh")

	res, err := eng.RunHooks(context.Background(), plan, HookRunRequest{})
	if err != nil {
		t.Fatalf("RunHooks: %v", err)
	}
	if got := rec.dispatched(); len(got) != 2 || got[0] != "local:05-first.local.sh" || got[1] != "local:10-second.local.sh" {
		t.Errorf("dispatched %v, want bytewise plan order", got)
	}
	if len(res.Steps) != 2 || !res.AllSucceeded() {
		t.Errorf("result = %+v", res)
	}
	for _, s := range res.Steps {
		if s.Step.ScriptSHA256 == "" {
			t.Errorf("step %s carries no hash of the bytes that ran", s.Step.ScriptName)
		}
	}
}

func TestRunHooksStopsAtTheFirstFailureUnlessAskedToContinue(t *testing.T) {
	eng, rec, local, plan := hooksHarness(t, "01-fails.local.sh", "02-after.local.sh")
	local.outcomes["01-fails.local.sh"] = failing(3)

	res, err := eng.RunHooks(context.Background(), plan, HookRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.dispatched(); len(got) != 1 {
		t.Errorf("a failed script was followed by %v", got)
	}
	if res.AllSucceeded() || len(res.Steps) != 2 || res.Steps[1].State != workflow.StateSkipped {
		t.Errorf("result = %+v: the script after a failure is recorded as skipped, not absent", res)
	}

	eng2, rec2, local2, plan2 := hooksHarness(t, "01-fails.local.sh", "02-after.local.sh")
	local2.outcomes["01-fails.local.sh"] = failing(3)
	res2, err := eng2.RunHooks(context.Background(), plan2, HookRunRequest{ContinueOnFailure: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := rec2.dispatched(); len(got) != 2 {
		t.Errorf("with ContinueOnFailure dispatched %v", got)
	}
	if res2.Steps[0].State != workflow.StateFailed || res2.Steps[1].State != workflow.StateSuccess {
		t.Errorf("states = %v / %v", res2.Steps[0].State, res2.Steps[1].State)
	}
}

func TestRunHooksTellsTheScriptTheFactsAndNothingElseOfTheRun(t *testing.T) {
	eng, rec, _, plan := hooksHarness(t, "01-env.local.sh")
	if _, err := eng.RunHooks(context.Background(), plan, HookRunRequest{}); err != nil {
		t.Fatal(err)
	}
	env := rec.envOf(t, "01-env.local.sh")
	for name, want := range map[string]string{
		"RETND": "1", "RETND_RECOVERY": "0", "RETND_PHASE": "after", "RETND_STEP_TARGET": "local",
		"RETND_BACKUP_STATUS": "unknown", "RETND_BACKUP_SET_ID": plan.BackupSetID().String(),
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q", name, env[name], want)
		}
	}
	if env["RETND_RUN_ID"] != "hooks-run" {
		t.Errorf("RETND_RUN_ID = %q", env["RETND_RUN_ID"])
	}
}

func TestRunHooksCollectsRedactedBoundedOutputAndSaysWhenItCutIt(t *testing.T) {
	eng, _, local, plan := hooksHarness(t, "01-noisy.local.sh")
	local.output["01-noisy.local.sh"] = strings.Repeat("x", 100)

	res, err := eng.RunHooks(context.Background(), plan, HookRunRequest{MaxOutputBytes: 40})
	if err != nil {
		t.Fatal(err)
	}
	out := res.Steps[0]
	if len(out.Output) != 40 || !out.OutputTruncated {
		t.Errorf("output = %d bytes, truncated=%v; want 40 and truncated", len(out.Output), out.OutputTruncated)
	}
}

func TestRunHooksAnExecutorThatCannotAttemptIsAFailureNotAPass(t *testing.T) {
	eng, _, local, plan := hooksHarness(t, "01-x.local.sh")
	local.outcomes["01-x.local.sh"] = func(context.Context, StepRequest) (StepOutcome, error) {
		return StepOutcome{}, errors.New("the runner socket is not configured")
	}
	res, err := eng.RunHooks(context.Background(), plan, HookRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.AllSucceeded() || res.Steps[0].Outcome.Disposition != DispositionNotAttempted {
		t.Errorf("result = %+v", res.Steps[0])
	}
}

func TestRunHooksReportsATimeoutAndItsCertaintyRatherThanAnExit(t *testing.T) {
	eng, _, local, plan := hooksHarness(t, "01-hang.local.sh")
	local.outcomes["01-hang.local.sh"] = func(context.Context, StepRequest) (StepOutcome, error) {
		return StepOutcome{Disposition: DispositionTimedOut, Certainty: workflowexec.TerminationConfirmed}, nil
	}
	res, err := eng.RunHooks(context.Background(), plan, HookRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Steps[0]
	if s.State != workflow.StateTimedOut || s.Outcome.Certainty != workflowexec.TerminationConfirmed || res.AllSucceeded() {
		t.Errorf("step = %+v", s)
	}
}

func TestRunHooksNeedsAnExecutorForTheTargetBeforeItRunsAnything(t *testing.T) {
	eng, rec, _, plan := hooksHarness(t, "01-a.local.sh")
	eng.Local = nil
	if _, err := eng.RunHooks(context.Background(), plan, HookRunRequest{}); err == nil {
		t.Error("a plan with a local script ran on an engine with no local executor")
	}
	if len(rec.dispatched()) != 0 {
		t.Error("something ran")
	}
}

func TestRunHooksRefusesAZeroPlan(t *testing.T) {
	if _, err := (&Engine{}).RunHooks(context.Background(), workflow.Plan{}, HookRunRequest{}); err == nil {
		t.Error("a zero plan was accepted")
	}
}
