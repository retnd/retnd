package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/app"
)

// #1222: a stage request that names the hash the script has to have is held to
// it against the bytes the engine spooled for the run, so there is no gap
// between "the file looked right" and "that file ran".

func TestRunStageHeldToAHashRunsOnlyThatScript(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, "recovery-test/production/alpha", "10-check.local.sh", "#!/usr/bin/env bash", "echo ok")
	dry, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: "recovery-test/production/alpha", DryRun: true})
	if err != nil || len(dry.Scripts) != 1 {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	pin := dry.Scripts[0].SHA256

	for _, dryRun := range []bool{true, false} {
		res, err := svc.RunStage(context.Background(), app.StageRequest{
			Set: set, SetDir: "recovery-test/production/alpha", Only: "10-check.local.sh", ExpectSHA256: pin, DryRun: dryRun,
		})
		if err != nil || !res.Configured {
			t.Errorf("dryRun=%v matching pin = %+v, %v", dryRun, res, err)
		}
	}
	if len(exec.calls) != 1 {
		t.Fatalf("calls = %d, want the one real run", len(exec.calls))
	}

	for _, dryRun := range []bool{true, false} {
		_, err := svc.RunStage(context.Background(), app.StageRequest{
			Set: set, SetDir: "recovery-test/production/alpha", Only: "10-check.local.sh", ExpectSHA256: strings.Repeat("0", 64), DryRun: dryRun,
		})
		if !errors.Is(err, app.ErrStageScriptChanged) || !strings.Contains(err.Error(), pin) {
			t.Errorf("dryRun=%v mismatching pin = %v, want ErrStageScriptChanged naming the hash found", dryRun, err)
		}
	}
	if len(exec.calls) != 1 {
		t.Errorf("a script that did not match its pin ran (calls = %d)", len(exec.calls))
	}
}
