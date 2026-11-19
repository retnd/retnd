package service

import (
	"context"
	"errors"
	"testing"

	"github.com/retnd/retnd/core/internal/app"
)

// StageRequest.Only names the one script a purpose runs (a recovery test's
// validator_id, and every custom validator in the shared directory). These pin
// that RunStage honours it: only the named script is read, verified or run,
// and a directory without it is ErrStageScriptMissing and never another
// script in its place.

func TestRunStageOnlyRunsTheNamedScriptAndNoOther(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, "recovery-test/production/alpha", "10-first.local.sh", "#!/usr/bin/env bash", "echo first")
	writeHook(t, root, "recovery-test/production/alpha", "20-second.local.sh", "#!/usr/bin/env bash", "echo second")

	res, err := svc.RunStage(context.Background(), app.StageRequest{
		Set: set, SetDir: "recovery-test/production/alpha", LocalOnly: true, Only: "20-second.local.sh",
	})
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	if len(res.Scripts) != 1 || res.Scripts[0].Name != "20-second.local.sh" {
		t.Fatalf("scripts = %+v, want only 20-second.local.sh", res.Scripts)
	}
	if len(exec.calls) != 1 || exec.calls[0].Step.ScriptName != "20-second.local.sh" {
		t.Errorf("the executor ran %d scripts, want exactly the named one", len(exec.calls))
	}
}

func TestRunStageOnlyDryRunIdentifiesTheNamedScriptAlone(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, "recovery-test/production/alpha", "10-first.local.sh", "#!/usr/bin/env bash", "echo first")
	writeHook(t, root, "recovery-test/production/alpha", "20-second.local.sh", "#!/usr/bin/env bash", "echo second")

	res, err := svc.RunStage(context.Background(), app.StageRequest{
		Set: set, SetDir: "recovery-test/production/alpha", LocalOnly: true, Only: "10-first.local.sh", DryRun: true,
	})
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	if !res.Configured || len(res.Scripts) != 1 || res.Scripts[0].Name != "10-first.local.sh" {
		t.Errorf("dry run = %+v, want the named script alone", res)
	}
	if len(exec.calls) != 0 {
		t.Error("a dry run executed something")
	}
}

func TestRunStageOnlyDoesNotVerifyTheOtherScripts(t *testing.T) {
	svc, root, set, exec := stageService(t)
	// The other script would be refused by the save gate; it is not the one
	// asked for, so it is neither read nor judged.
	writeHook(t, root, "recovery-test/production/alpha", "10-bad.local.sh", "#!/usr/bin/env bash", `rm -rf /$UNSET_DIR`)
	writeHook(t, root, "recovery-test/production/alpha", "20-good.local.sh", "#!/usr/bin/env bash", "echo ok")

	res, err := svc.RunStage(context.Background(), app.StageRequest{
		Set: set, SetDir: "recovery-test/production/alpha", LocalOnly: true, Only: "20-good.local.sh",
	})
	if err != nil {
		t.Fatalf("RunStage refused the named script because of another one: %v", err)
	}
	if !res.Succeeded() || len(exec.calls) != 1 {
		t.Errorf("res = %+v calls = %d", res, len(exec.calls))
	}
}

func TestRunStageOnlyMissingIsScriptMissingNeverAnotherScript(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, "recovery-test/production/alpha", "10-first.local.sh", "#!/usr/bin/env bash", "echo first")

	_, err := svc.RunStage(context.Background(), app.StageRequest{
		Set: set, SetDir: "recovery-test/production/alpha", LocalOnly: true, Only: "99-absent.local.sh",
	})
	if !errors.Is(err, app.ErrStageScriptMissing) {
		t.Errorf("err = %v, want ErrStageScriptMissing", err)
	}
	if len(exec.calls) != 0 {
		t.Error("another script ran in the named one's place")
	}
}
