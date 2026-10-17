package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// #1203: a recovery test picks the validator for the copy it is about to
// check, most specific first: the set's own pattern, a global pattern, the
// set's validator_id, the one script in recovery-test/<source>/<set>, then
// the built-in. These drive Service.RecoveryTest through a stage runner
// that holds real directories of named scripts and honours Only, so the
// order is proved by which script was asked to run.

type dirStages struct {
	mu    sync.Mutex
	dirs  map[string][]string // dir -> script names
	exit  map[string]int      // script name -> exit code (0 when absent)
	asked []StageRequest
}

func (d *dirStages) RunStage(_ context.Context, req StageRequest) (StageResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if strings.HasPrefix(req.SetDir, "assurance-after") {
		return StageResult{}, nil
	}
	names, ok := d.dirs[req.SetDir]
	if !ok {
		if req.Only != "" {
			return StageResult{}, fmt.Errorf("%w: %s", ErrStageScriptMissing, req.Only)
		}
		return StageResult{}, nil
	}
	if req.Only != "" {
		found := false
		for _, n := range names {
			found = found || n == req.Only
		}
		if !found {
			return StageResult{}, fmt.Errorf("%w: %s", ErrStageScriptMissing, req.Only)
		}
		names = []string{req.Only}
	}
	if req.MaxScripts > 0 && len(names) > req.MaxScripts {
		return StageResult{}, fmt.Errorf("%d scripts and this purpose takes %d", len(names), req.MaxScripts)
	}
	out := StageResult{Configured: len(names) > 0}
	for _, n := range names {
		sc := StageScript{Dir: req.SetDir, Name: n, SHA256: strings.Repeat("cd", 32), Target: "local"}
		if !req.DryRun {
			code := d.exit[n]
			sc.State, sc.Disposition, sc.ExitCode = "success", "exited", &code
			if code != 0 {
				sc.State = "failed"
			}
		}
		out.Scripts = append(out.Scripts, sc)
	}
	if !req.DryRun {
		d.asked = append(d.asked, req)
	}
	return out, nil
}

func (d *dirStages) ran() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, r := range d.asked {
		out = append(out, r.SetDir+"/"+r.Only)
	}
	return out
}

func customValidatorService(t *testing.T, stages *dirStages, validatorID string, list ...config.CustomValidator) (*Service, *state.Journal, config.BackupSet) {
	t.Helper()
	bs := testBackupSet(t, t.TempDir())
	cfg := testConfig(t, testSource("production", bs))
	cfg.AssuranceBlock = &config.AssuranceBlock{Validators: list}
	for i := range list {
		if list[i].BackupSet == "@set" {
			cfg.AssuranceBlock.Validators[i].BackupSet = bs.ID.String()
		}
	}
	if err := cfg.ResolveAssurance(); err != nil {
		t.Fatalf("ResolveAssurance: %v", err)
	}
	cfg.Sources[0].BackupSets[0].Assurance.RecoveryTest.Mode = config.RecoveryTestRequired
	cfg.Sources[0].BackupSets[0].Assurance.RecoveryTest.ValidatorID = validatorID
	journal := openJournal(t)
	svc := New(cfg, journal, newFakeTransport(), nil)
	svc.Now = fixedNow(retentionTestNow)
	svc.RecoveryStagingRoot = t.TempDir()
	svc.Stages = stages
	return svc, journal, cfg.Sources[0].BackupSets[0]
}

func TestCustomValidator_TheSetsOwnPatternWinsOverAGlobalPatternAndTheSetsValidator(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{
		config.CustomValidatorDir: {"set-manifest.local.sh", "global-manifest.local.sh"},
	}}
	svc, journal, bs := customValidatorService(t, stages, "own",
		config.CustomValidator{Name: "global-manifest", Pattern: `^MANIFEST-`},
		config.CustomValidator{Name: "set-manifest", Pattern: `^MANIFEST-\d{8}T\d{6}Z\.txt$`, BackupSet: "@set"},
	)
	stages.dirs[RecoveryTestSetDir(bs.ID)] = []string{"own.local.sh"}
	a := seedMovableArtifact(t, ctx, journal, bs, "MANIFEST-20260902T153521Z.txt", retentionTestNow.Add(-time.Hour))

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatalf("RecoveryTest: %v", err)
	}
	if res.Result.State != state.RecoveryPassed {
		t.Fatalf("state = %s (%s)", res.Result.State, res.Result.Reason)
	}
	if got := stages.ran(); len(got) != 1 || got[0] != config.CustomValidatorDir+"/set-manifest.local.sh" {
		t.Errorf("ran %v, want only the set-scoped pattern's script", got)
	}
	p := localCopyOf(t, journal, a)
	if p.RecoveryValidatorID != "set-manifest.local.sh" {
		t.Errorf("recorded validator = %q", p.RecoveryValidatorID)
	}
	asked := stages.asked[0]
	if asked.Vars["RECOVERY_TEST_VALIDATOR"] != "set-manifest" || asked.Vars["RECOVERY_TEST_SCOPE"] != "backup_set" ||
		asked.Vars["RECOVERY_TEST_NAME"] != "MANIFEST-20260902T153521Z.txt" || asked.Vars["RECOVERY_TEST_PATTERN"] != `^MANIFEST-\d{8}T\d{6}Z\.txt$` {
		t.Errorf("the script was not told which validator it is: %v", asked.Vars)
	}
	if !asked.LocalOnly {
		t.Error("a custom validator must be a local script")
	}
}

func TestCustomValidator_AGlobalPatternWinsOverTheSetsValidatorID(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"global-manifest.local.sh"}}}
	svc, journal, bs := customValidatorService(t, stages, "own",
		config.CustomValidator{Name: "global-manifest", Pattern: `^MANIFEST-`},
	)
	stages.dirs[RecoveryTestSetDir(bs.ID)] = []string{"own.local.sh"}
	seedMovableArtifact(t, ctx, journal, bs, "MANIFEST-20260902T153521Z.txt", retentionTestNow.Add(-time.Hour))

	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	if got := stages.ran(); len(got) != 1 || got[0] != config.CustomValidatorDir+"/global-manifest.local.sh" {
		t.Errorf("ran %v, want the global pattern's script", got)
	}
}

func TestCustomValidator_NoPatternMatchFallsBackToTheSetsValidatorID(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"global-manifest.local.sh"}}}
	svc, journal, bs := customValidatorService(t, stages, "own",
		config.CustomValidator{Name: "global-manifest", Pattern: `^MANIFEST-`},
	)
	stages.dirs[RecoveryTestSetDir(bs.ID)] = []string{"own.local.sh", "other.local.sh"}
	seedMovableArtifact(t, ctx, journal, bs, "db-20260902.sql.zst", retentionTestNow.Add(-time.Hour))

	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	if got := stages.ran(); len(got) != 1 || got[0] != RecoveryTestSetDir(bs.ID)+"/own.local.sh" {
		t.Errorf("ran %v, want the set's validator_id", got)
	}
}

func TestCustomValidator_TwoPatternsAtOneScopeAreCouldNotCompleteAndRunNothing(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"a.local.sh", "b.local.sh"}}}
	svc, journal, bs := customValidatorService(t, stages, "",
		config.CustomValidator{Name: "a", Pattern: `^MANIFEST-`},
		config.CustomValidator{Name: "b", Pattern: `\.txt$`},
	)
	seedMovableArtifact(t, ctx, journal, bs, "MANIFEST-20260902T153521Z.txt", retentionTestNow.Add(-time.Hour))

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.State != state.RecoveryCouldNotComplete || !strings.Contains(res.Result.Reason, "a, b") || !strings.Contains(res.Result.Reason, "not something to guess") {
		t.Errorf("state = %s reason = %q", res.Result.State, res.Result.Reason)
	}
	if got := stages.ran(); len(got) != 0 {
		t.Errorf("an ambiguous match ran %v", got)
	}
}

func TestCustomValidator_AMatchingPatternWhoseScriptIsGoneIsCouldNotCompleteNeverTheFallback(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{}}
	svc, journal, bs := customValidatorService(t, stages, "",
		config.CustomValidator{Name: "manifest", Pattern: `^MANIFEST-`},
	)
	stages.dirs[RecoveryTestSetDir(bs.ID)] = []string{"own.local.sh"}
	seedMovableArtifact(t, ctx, journal, bs, "MANIFEST-20260902T153521Z.txt", retentionTestNow.Add(-time.Hour))

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.State != state.RecoveryCouldNotComplete || !strings.Contains(res.Result.Reason, "manifest.local.sh was not found in recovery-test-validators") {
		t.Errorf("state = %s reason = %q", res.Result.State, res.Result.Reason)
	}
	if got := stages.ran(); len(got) != 0 {
		t.Errorf("the set's own script ran in the missing custom validator's place: %v", got)
	}
}

func TestCustomValidator_ASetScopedPatternIsOutOfScopeInAnotherSet(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"elsewhere.local.sh"}}}
	bs := testBackupSet(t, t.TempDir())
	other := testBackupSet(t, t.TempDir())
	other.Name = "other"
	other.ID = model.BackupSetID{Source: bs.ID.Source, Set: "other"}
	cfg := testConfig(t, testSource("production", bs, other))
	cfg.AssuranceBlock = &config.AssuranceBlock{Validators: []config.CustomValidator{{Name: "elsewhere", Pattern: `.`, BackupSet: other.ID.String()}}}
	if err := cfg.ResolveAssurance(); err != nil {
		t.Fatal(err)
	}
	svc := New(cfg, openJournal(t), newFakeTransport(), nil)
	svc.Stages = stages
	if name, _ := svc.RecoveryTestValidatorFor(bs.ID, "anything"); name != "" {
		t.Errorf("a validator scoped to %s applied in %s: %q", other.ID, bs.ID, name)
	}
	if name, _ := svc.RecoveryTestValidatorFor(other.ID, "anything"); name != "elsewhere" {
		t.Errorf("in its own set = %q", name)
	}
	_ = ctx
}

func TestCustomValidator_DeclaredAndEffectiveCheckFollowTheNewestCopysName(t *testing.T) {
	ctx := context.Background()
	stages := &dirStages{dirs: map[string][]string{config.CustomValidatorDir: {"manifest.local.sh"}}}
	svc, journal, bs := customValidatorService(t, stages, "",
		config.CustomValidator{Name: "manifest", Pattern: `^MANIFEST-`},
	)
	// No copy yet: no name to match, and no script in the set's directory.
	if declared, err := svc.RecoveryTestValidatorDeclared(ctx, bs.ID); err != nil || declared {
		t.Errorf("declared = %v, %v before any copy exists", declared, err)
	}
	seedMovableArtifact(t, ctx, journal, bs, "MANIFEST-20260902T153521Z.txt", retentionTestNow.Add(-time.Hour))
	if declared, err := svc.RecoveryTestValidatorDeclared(ctx, bs.ID); err != nil || !declared {
		t.Errorf("declared = %v, %v with a matching newest copy", declared, err)
	}
	if check, _, err := svc.RecoveryTestEffectiveCheck(ctx, bs.ID); err != nil || check != CheckValidator {
		t.Errorf("effective check = %q, %v", check, err)
	}
}
