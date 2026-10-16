package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/workflow"
	"github.com/retnd/retnd/core/internal/workflowlint"
	"github.com/retnd/retnd/core/internal/workflowrun"
)

// #1203 against the real thing: a config.yaml on disk, a workflow root with
// discovery's custody rules, and a SQLite journal holding the artifacts the
// patterns are judged by.

const goodValidator = "#!/usr/bin/env bash\nset -euo pipefail\ntest -s \"$RECOVERY_TEST_FILE\"\n"

func seedValidatorArtifacts(t *testing.T, svc *BackupService, names ...string) {
	t.Helper()
	ctx := context.Background()
	set := model.BackupSetID{Source: "production", Set: "alpha"}
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i, name := range names {
		a, err := model.NewArtifactID(set, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.journal.Discover(ctx, a, name+"-d", "/remote/"+name, state.RemoteIdentity{}, at.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("Discover %s: %v", name, err)
		}
	}
}

func TestCustomValidators_SuggestArtifactPatternGeneralisesDigitRuns(t *testing.T) {
	for name, want := range map[string]string{
		"MANIFEST-20260902T153521Z.txt": `^MANIFEST-\d{8}T\d{6}Z\.txt$`,
		"db-2026-09-02.sql.gz":          `^db-\d{4}-\d{2}-\d{2}\.sql\.gz$`,
		"plain.tar":                     `^plain\.tar$`,
		"a+b(1).zip":                    `^a\+b\(\d{1}\)\.zip$`,
	} {
		got := SuggestArtifactPattern(name)
		if got != want {
			t.Errorf("SuggestArtifactPattern(%q) = %q, want %q", name, got, want)
		}
		if re, err := config.CompileValidatorPattern(got); err != nil || !re.MatchString(name) {
			t.Errorf("%q does not match the name it came from: %v", got, err)
		}
	}
}

func TestCustomValidators_PatternsCountTheJournalsArtifactsMostCommonFirst(t *testing.T) {
	svc, _, _ := openWorkflowSaveService(t)
	seedValidatorArtifacts(t, svc, "MANIFEST-20260902T153521Z.txt", "MANIFEST-20260903T010203Z.txt", "db-2026-09-02.sql.gz")

	got, err := svc.CustomValidatorPatterns(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Patterns) != 2 {
		t.Fatalf("patterns = %+v", got.Patterns)
	}
	top := got.Patterns[0]
	if top.Pattern != `^MANIFEST-\d{8}T\d{6}Z\.txt$` || top.Matches != 2 || len(top.MatchSets) != 1 || top.MatchSets[0] != "production/alpha" || !strings.HasPrefix(top.Example, "MANIFEST-") {
		t.Errorf("top suggestion = %+v", top)
	}
	if _, err := svc.CustomValidatorPatterns(context.Background(), "production/nope"); !errors.Is(err, ErrBackupSetNotFound) {
		t.Errorf("an unknown set = %v", err)
	}
}

func TestCustomValidators_CreateWritesTheScriptAndTheConfigAndListsIt(t *testing.T) {
	ctx := context.Background()
	svc, configPath, root := openWorkflowSaveService(t)
	seedValidatorArtifacts(t, svc, "MANIFEST-20260902T153521Z.txt", "db-2026-09-02.sql.gz")

	v, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{
		Name: "manifest", Pattern: `^MANIFEST-\d{8}T\d{6}Z\.txt$`, BackupSetID: "production/alpha", Script: goodValidator,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !v.ScriptPresent || v.Matches != 1 || v.Scope != "backup_set" || v.BackupSetID != "production/alpha" || !v.Anchored || v.ExampleName != "MANIFEST-20260902T153521Z.txt" || v.UploadedAt.IsZero() {
		t.Errorf("created = %+v", v)
	}

	path := filepath.Join(root, config.CustomValidatorDir, "manifest.local.sh")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the script is not on disk: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("script mode = %04o, want 0640", info.Mode().Perm())
	}
	dirInfo, _ := os.Stat(filepath.Dir(path))
	if dirInfo.Mode().Perm()&0o022 != 0 {
		t.Errorf("directory mode %04o lets another account add a script", dirInfo.Mode().Perm())
	}
	// Nothing but the script is left in the stage directory or the root.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("the stage directory holds %d entries", len(entries))
	}
	rootEntries, _ := os.ReadDir(root)
	for _, e := range rootEntries {
		if strings.HasPrefix(e.Name(), ".retnd-validator-") {
			t.Errorf("a temporary file was left in the root: %s", e.Name())
		}
	}
	// Discovery accepts what was written.
	r, err := workflow.NewRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := r.ResolveStage(config.CustomValidatorDir)
	if err != nil {
		t.Fatalf("discovery refuses the directory it was written to: %v", err)
	}
	if scripts, err := workflow.Discover(dir); err != nil || len(scripts) != 1 {
		t.Errorf("discover = %v, %v", scripts, err)
	}

	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "name: manifest") || !strings.Contains(string(raw), "backup_set: production/alpha") {
		t.Errorf("config.yaml does not carry the validator:\n%s", raw)
	}
	// Hot-reloaded: the running config resolves it already.
	if got := svc.state.Load().inner.Config.Assurance.Validators; len(got) != 1 || got[0].Name != "manifest" {
		t.Errorf("running validators = %+v", got)
	}

	list, err := svc.ListCustomValidators(ctx, CustomValidatorsFilter{ArtifactID: "production/alpha/MANIFEST-20260902T153521Z.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !list.Available || len(list.Validators) != 1 || list.Resolution == nil || list.Resolution.Validator != "manifest" {
		t.Errorf("list = %+v", list)
	}
	other, _ := svc.ListCustomValidators(ctx, CustomValidatorsFilter{ArtifactID: "production/alpha/db-2026-09-02.sql.gz"})
	if other.Resolution == nil || other.Resolution.Validator != "" || other.Resolution.Fallback == "" {
		t.Errorf("an unmatched name's resolution = %+v", other.Resolution)
	}

	// The engine's own stage runner selects the uploaded script by name.
	var set config.BackupSet
	for _, src := range svc.state.Load().inner.Config.Sources {
		set = src.BackupSets[0]
	}
	dry, err := svc.RunStage(ctx, app.StageRequest{Set: set, SetDir: config.CustomValidatorDir, LocalOnly: true, Only: "manifest.local.sh", DryRun: true})
	if err != nil || len(dry.Scripts) != 1 || dry.Scripts[0].SHA256 != v.ScriptSHA256 {
		t.Errorf("RunStage over the uploaded script = %+v, %v", dry, err)
	}

	script, err := svc.CustomValidatorScript(ctx, "manifest")
	if err != nil || script.Content != goodValidator || len(script.SHA256) != 64 || script.FileName != "manifest.local.sh" {
		t.Errorf("script = %+v, %v", script, err)
	}
}

func TestCustomValidators_CreateRefusals(t *testing.T) {
	ctx := context.Background()
	svc, configPath, root := openWorkflowSaveService(t)
	seedValidatorArtifacts(t, svc, "MANIFEST-20260902T153521Z.txt")
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "first", Pattern: `^MANIFEST-`, Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(configPath)

	cases := map[string]struct {
		req  CreateCustomValidatorRequest
		want error
		say  string
	}{
		"taken name":       {CreateCustomValidatorRequest{Name: "first", Pattern: "x", Script: goodValidator}, ErrValidatorExists, "already exists"},
		"bad name":         {CreateCustomValidatorRequest{Name: "../x", Pattern: "x", Script: goodValidator}, ErrInvalidRequest, ""},
		"bad regex":        {CreateCustomValidatorRequest{Name: "b", Pattern: "(", Script: goodValidator}, ErrInvalidRequest, "RE2"},
		"unknown set":      {CreateCustomValidatorRequest{Name: "b", Pattern: "x", BackupSetID: "production/nope", Script: goodValidator}, ErrBackupSetNotFound, ""},
		"empty script":     {CreateCustomValidatorRequest{Name: "b", Pattern: "x", Script: "  "}, ErrInvalidRequest, "empty"},
		"binary script":    {CreateCustomValidatorRequest{Name: "b", Pattern: "x", Script: "#!/bin/bash\n\x00"}, ErrInvalidRequest, "UTF-8"},
		"oversized script": {CreateCustomValidatorRequest{Name: "b", Pattern: "x", Script: "#!/usr/bin/env bash\n# " + strings.Repeat("a", MaxCustomValidatorScriptBytes)}, ErrInvalidRequest, "at most"},
		"lint refusal":     {CreateCustomValidatorRequest{Name: "b", Pattern: "x", Script: "#!/usr/bin/env bash\nrm -rf /$UNSET_DIR\n"}, ErrInvalidRequest, "BSH003"},
		"same pattern":     {CreateCustomValidatorRequest{Name: "b", Pattern: `^MANIFEST-`, Script: goodValidator}, ErrValidatorConflict, "exactly this pattern"},
		"overlap":          {CreateCustomValidatorRequest{Name: "b", Pattern: `\.txt$`, Script: goodValidator}, ErrValidatorConflict, "first also matches production/alpha/MANIFEST-20260902T153521Z.txt"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.CreateCustomValidator(ctx, tc.req)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.say) {
				t.Errorf("err = %v, want %v saying %q", err, tc.want, tc.say)
			}
		})
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Error("a refused upload changed config.yaml")
	}
	entries, _ := os.ReadDir(filepath.Join(root, config.CustomValidatorDir))
	if len(entries) != 1 {
		t.Errorf("a refused upload left %d scripts", len(entries))
	}
	// A set-scoped pattern does not collide with a global one: they are two
	// scopes and the set's wins.
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "setwide", Pattern: `\.txt$`, BackupSetID: "production/alpha", Script: goodValidator}); err != nil {
		t.Errorf("a set-scoped overlap with a global pattern was refused: %v", err)
	}
}

func TestCustomValidators_WithoutAWorkflowRootIsUnavailable(t *testing.T) {
	ctx := context.Background()
	svc, configPath, _ := openWorkflowSaveService(t)
	raw, _ := os.ReadFile(configPath)
	lines := strings.Split(string(raw), "\n")
	var kept []string
	for i := 0; i < len(lines); i++ {
		if lines[i] == "workflows:" {
			i++ // and its root line
			continue
		}
		kept = append(kept, lines[i])
	}
	if err := os.WriteFile(configPath, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadAndValidate(configPath)
	if err != nil {
		t.Fatal(err)
	}
	svc.adoptConfig(cfg)

	_, err = svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "x", Pattern: "x", Script: goodValidator})
	if !errors.Is(err, ErrValidatorsUnavailable) || !strings.Contains(err.Error(), "workflows.root") {
		t.Errorf("err = %v", err)
	}
	list, err := svc.ListCustomValidators(ctx, CustomValidatorsFilter{})
	if err != nil || list.Available || list.UnavailableReason == "" {
		t.Errorf("list = %+v, %v", list, err)
	}
}

func TestCustomValidators_UpdateKeepsTheScriptWhenNoneIsSentAndReplacesItWhenOneIs(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	v, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^b", BackupSetID: "production/alpha"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if v.Pattern != "^b" || v.BackupSetID != "production/alpha" || !v.ScriptPresent {
		t.Errorf("updated = %+v", v)
	}
	body, _ := os.ReadFile(filepath.Join(root, config.CustomValidatorDir, "v.local.sh"))
	if string(body) != goodValidator {
		t.Error("an update with no script changed the script")
	}
	next := goodValidator + "echo again\n"
	if _, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^b", Script: next}); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(filepath.Join(root, config.CustomValidatorDir, "v.local.sh"))
	if string(body) != next {
		t.Error("an update with a script did not replace it")
	}
	if _, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "nope", Pattern: "x"}); !errors.Is(err, ErrValidatorNotFound) {
		t.Errorf("update of a missing validator = %v", err)
	}
}

func TestCustomValidators_RemoveTakesTheEntryAndTheScript(t *testing.T) {
	ctx := context.Background()
	svc, configPath, root := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.RemoveCustomValidator(ctx, "v")
	if err != nil || got.Name != "v" {
		t.Fatalf("Remove = %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, config.CustomValidatorDir, "v.local.sh")); !os.IsNotExist(err) {
		t.Errorf("the script is still there: %v", err)
	}
	raw, _ := os.ReadFile(configPath)
	if strings.Contains(string(raw), "assurance:") {
		t.Errorf("an assurance block that names nothing was left behind:\n%s", raw)
	}
	if _, err := svc.RemoveCustomValidator(ctx, "v"); !errors.Is(err, ErrValidatorNotFound) {
		t.Errorf("a second remove = %v", err)
	}
}

func TestCustomValidators_RemovingTheBackupSetDropsItsValidators(t *testing.T) {
	ctx := context.Background()
	svc, configPath, root := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "scoped", Pattern: "^a", BackupSetID: "production/alpha", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "everywhere", Pattern: "^z", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveBackupSet(ctx, "production/alpha"); err != nil {
		t.Fatalf("RemoveBackupSet: %v", err)
	}
	if _, err := config.LoadAndValidate(configPath); err != nil {
		t.Fatalf("the file no longer loads after removing the set: %v", err)
	}
	vs := svc.state.Load().inner.Config.Assurance.Validators
	if len(vs) != 1 || vs[0].Name != "everywhere" {
		t.Errorf("validators left = %+v", vs)
	}
	if _, err := os.Stat(filepath.Join(root, config.CustomValidatorDir, "scoped.local.sh")); !os.IsNotExist(err) {
		t.Error("the removed set's validator script is still there")
	}
}

func TestCustomValidators_PreviewJudgesWithRE2AndReportsConflicts(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := openWorkflowSaveService(t)
	seedValidatorArtifacts(t, svc, "MANIFEST-20260902T153521Z.txt", "MANIFEST-20260903T010203Z.txt", "db.sql.gz")
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "first", Pattern: `\.gz$`, Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	p, err := svc.PreviewCustomValidator(ctx, `^MANIFEST-\d{8}T\d{6}Z\.txt$`, "", "")
	if err != nil || !p.Valid || p.Matches != 2 || len(p.Sample) != 2 || !p.Anchored || len(p.Conflicts) != 0 {
		t.Errorf("preview = %+v, %v", p, err)
	}
	// A lookahead is valid JavaScript and not RE2: the server is the judge.
	p, err = svc.PreviewCustomValidator(ctx, `^(?=M)`, "", "")
	if err != nil || p.Valid || !strings.Contains(p.Error, "RE2") {
		t.Errorf("preview of a lookahead = %+v, %v", p, err)
	}
	p, _ = svc.PreviewCustomValidator(ctx, `^db`, "", "")
	if len(p.Conflicts) != 1 || p.Conflicts[0].Validator != "first" || p.Anchored {
		t.Errorf("preview conflicts = %+v", p)
	}
	// Previewing first's own replacement is not a conflict with itself.
	p, _ = svc.PreviewCustomValidator(ctx, `^db`, "", "first")
	if len(p.Conflicts) != 0 {
		t.Errorf("a validator conflicted with itself: %+v", p.Conflicts)
	}
}

func TestCustomValidators_TheDefaultScriptPassesTheSaveGateAndDocumentsItsContract(t *testing.T) {
	s := DefaultCustomValidatorScript()
	if s.FileName != DefaultValidatorFileName || len(s.SHA256) != 64 || s.SizeBytes != int64(len(s.Content)) {
		t.Errorf("identity = %+v", s)
	}
	if report := workflowlint.Report(context.Background(), s.FileName, []byte(s.Content)); report.Blocks() {
		t.Fatalf("the default script would be refused by the save gate: %s", describeBlocking(report))
	}
	if int64(len(s.Content)) > MaxCustomValidatorScriptBytes {
		t.Error("the default script is larger than an upload may be")
	}
	for _, must := range []string{
		"RECOVERY_TEST_FILE", "RECOVERY_TEST_NAME", "RECOVERY_TEST_ARTIFACT", "RECOVERY_TEST_MEDIUM",
		"RECOVERY_TEST_DIGEST", "RECOVERY_TEST_SIZE", "RECOVERY_TEST_VALIDATOR", "RECOVERY_TEST_PATTERN", "RECOVERY_TEST_SCOPE",
		"16 KiB", "200 lines", "1024 bytes", "32 KiB", "--hook-mount", "--hook-image", "script_timeout",
		"PASSED", "FAILED", "could not complete", "ADAPT HERE",
	} {
		if !strings.Contains(s.Content, must) {
			t.Errorf("the default script does not document %q", must)
		}
	}
}

// The default script's documentation states numbers the engine owns. If one
// of them moves, this says so here rather than leaving the script wrong.
func TestCustomValidators_TheDefaultScriptsNumbersAreTheEnginesNumbers(t *testing.T) {
	s := DefaultCustomValidatorScript().Content
	if state.MaxRecoveryOutputRecords != 200 || state.MaxRecoveryOutputLineBytes != 1024 || state.MaxRecoveryOutputBytes != 32<<10 {
		t.Error("the stored-output bounds changed; update the default script's WHAT HAPPENS TO ITS OUTPUT section")
	}
	if workflowrun.DefaultHookOutputBytes != 16<<10 || !strings.Contains(s, "first 16 KiB") {
		t.Error("the hook output capture changed; update the default script's WHAT HAPPENS TO ITS OUTPUT section")
	}
	if workflow.DefaultStepTimeout != 5*time.Minute || !strings.Contains(s, "5 minutes") {
		t.Error("the default step timeout changed; update the default script's WHERE IT RUNS section")
	}
}
