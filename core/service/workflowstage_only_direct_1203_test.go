package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/core/internal/app"
)

// #1203 review: a purpose that names its script resolves that file and does
// not hold the rest of the directory to the name rule or to the 64 entry
// limit. In the shared validator directory one stray file would otherwise
// refuse every validator in it.

const onlyDir = "recovery-test-validators"

func TestRunStageOnlyIgnoresAStrayFileInTheDirectory(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, onlyDir, "good.local.sh", "#!/usr/bin/env bash", "echo ok")
	if err := os.WriteFile(filepath.Join(root, onlyDir, "README.md"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: onlyDir, LocalOnly: true, Only: "good.local.sh"})
	if err != nil {
		t.Fatalf("a stray README.md refused the named validator: %v", err)
	}
	if !res.Succeeded() || len(res.Scripts) != 1 || len(exec.calls) != 1 {
		t.Errorf("res = %+v calls = %d", res, len(exec.calls))
	}
}

func TestRunStageOnlyIsNotBoundByTheDirectorysEntryLimit(t *testing.T) {
	svc, root, set, exec := stageService(t)
	for i := 0; i < 70; i++ {
		writeHook(t, root, onlyDir, fmt.Sprintf("v%02d.local.sh", i), "#!/usr/bin/env bash", "echo ok")
	}

	res, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: onlyDir, LocalOnly: true, Only: "v33.local.sh"})
	if err != nil {
		t.Fatalf("70 files in the directory refused the one that was named: %v", err)
	}
	if len(res.Scripts) != 1 || res.Scripts[0].Name != "v33.local.sh" || len(exec.calls) != 1 {
		t.Errorf("res = %+v calls = %d", res, len(exec.calls))
	}
}

// The relaxed listing does not relax custody: the named file itself is still
// held to every rule a captured script is.
func TestRunStageOnlyStillRefusesANamedSymlink(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, onlyDir, "real.local.sh", "#!/usr/bin/env bash", "echo ok")
	if err := os.Symlink(filepath.Join(root, onlyDir, "real.local.sh"), filepath.Join(root, onlyDir, "link.local.sh")); err != nil {
		t.Fatal(err)
	}

	_, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: onlyDir, LocalOnly: true, Only: "link.local.sh"})
	if err == nil {
		t.Fatal("a symbolic link was run as a validator")
	}
	if len(exec.calls) != 0 {
		t.Error("the executor ran something behind a refused link")
	}
}

func TestRunStageOnlyNeedsAScriptName(t *testing.T) {
	svc, root, set, exec := stageService(t)
	writeHook(t, root, onlyDir, "good.local.sh", "#!/usr/bin/env bash", "echo ok")

	for _, only := range []string{"../good.local.sh", "sub/good.local.sh", "not-a-script.txt"} {
		if _, err := svc.RunStage(context.Background(), app.StageRequest{Set: set, SetDir: onlyDir, LocalOnly: true, Only: only}); err == nil {
			t.Errorf("Only=%q was accepted", only)
		}
	}
	if len(exec.calls) != 0 {
		t.Error("something ran")
	}
}
