package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/workflow"
)

// #1203 review: what the write path, the list and the directory do when
// things are not tidy.

func validatorEvents(t *testing.T, stream *bytes.Buffer, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stream.String()), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		if ev["event"] == name {
			out = append(out, ev)
		}
	}
	return out
}

func sha256Of(s string) string { return validatorScriptOf("x", []byte(s)).SHA256 }

// ---------------------------------------------------------------- PUT

func TestUpdateCustomValidator_APutIsAWholeReplace(t *testing.T) {
	ctx := context.Background()
	svc, configPath, root := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", BackupSetID: "production/alpha", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(configPath)
	scriptPath := filepath.Join(root, config.CustomValidatorDir, "v.local.sh")

	t.Run("neither a script nor a hash is refused and changes nothing", func(t *testing.T) {
		_, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^b"})
		if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "script_sha256") {
			t.Errorf("err = %v, want a refusal that names script_sha256", err)
		}
	})
	t.Run("both a script and a hash is refused", func(t *testing.T) {
		_, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^b", Script: goodValidator, ScriptSHA256: sha256Of(goodValidator)})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("err = %v, want ErrInvalidRequest", err)
		}
	})
	t.Run("a hash that is not the stored script's is a conflict", func(t *testing.T) {
		_, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^b", ScriptSHA256: sha256Of("something else")})
		if !errors.Is(err, ErrValidatorScriptChanged) {
			t.Errorf("err = %v, want ErrValidatorScriptChanged", err)
		}
	})
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Error("a refused edit changed config.yaml")
	}
	if body, _ := os.ReadFile(scriptPath); string(body) != goodValidator {
		t.Error("a refused edit changed the script")
	}

	t.Run("the right hash keeps the script and replaces the scope", func(t *testing.T) {
		v, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^b", ScriptSHA256: sha256Of(goodValidator)})
		if err != nil {
			t.Fatal(err)
		}
		if v.Pattern != "^b" || v.BackupSetID != "" || v.Scope != "global" || !v.ScriptPresent {
			t.Errorf("updated = %+v: omitting the set is a replacement, so it is global now", v)
		}
		if body, _ := os.ReadFile(scriptPath); string(body) != goodValidator {
			t.Error("keeping the script changed it")
		}
	})
	t.Run("a script replaces it", func(t *testing.T) {
		next := goodValidator + "echo again\n"
		if _, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^b", Script: next}); err != nil {
			t.Fatal(err)
		}
		if body, _ := os.ReadFile(scriptPath); string(body) != next {
			t.Error("the script was not replaced")
		}
	})
}

// ------------------------------------------------------------- remove

func TestRemoveCustomValidator_MovesTheScriptAsideInsteadOfDeletingIt(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	aside := filepath.Join(root, config.CustomValidatorDir+".superseded")

	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RemoveCustomValidator(ctx, "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, config.CustomValidatorDir, "v.local.sh")); !os.IsNotExist(err) {
		t.Errorf("the script is still in the stage directory: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(aside, "v.local.sh"))
	if err != nil || string(got) != goodValidator {
		t.Fatalf("the script is not in %s: %v", aside, err)
	}
	if info, _ := os.Stat(aside); info == nil || info.Mode().Perm()&0o022 != 0 {
		t.Errorf("the superseded directory = %v", info)
	}

	// The same name again: nothing already moved aside is overwritten.
	second := goodValidator + "echo second\n"
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: second}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RemoveCustomValidator(ctx, "v"); err != nil {
		t.Fatal(err)
	}
	if first, _ := os.ReadFile(filepath.Join(aside, "v.local.sh")); string(first) != goodValidator {
		t.Error("the first removed script was overwritten")
	}
	if again, err := os.ReadFile(filepath.Join(aside, "v.local.sh.1")); err != nil || string(again) != second {
		t.Errorf("the second removed script is not v.local.sh.1: %v", err)
	}
}

func TestRemovingABackupSetMovesItsValidatorScriptsAside(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "scoped", Pattern: "^a", BackupSetID: "production/alpha", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveBackupSet(ctx, "production/alpha"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, config.CustomValidatorDir+".superseded", "scoped.local.sh"))
	if err != nil || string(got) != goodValidator {
		t.Errorf("the removed set's script was deleted instead of moved aside: %v", err)
	}
}

// ------------------------------------------------------ the directory

func TestCreateCustomValidator_MovesAnOrphanOutOfTheStageDirectory(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "kept", Pattern: "^k", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, config.CustomValidatorDir)
	// Left by an interrupted removal, an editor, or a hand copy.
	if err := os.WriteFile(filepath.Join(dir, "old.local.sh"), []byte(goodValidator), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "next", Pattern: "^n", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "kept.local.sh,next.local.sh" {
		t.Errorf("the stage directory holds %v, want only the two declared scripts", names)
	}
	aside := filepath.Join(root, config.CustomValidatorDir+".superseded")
	for _, orphan := range []string{"old.local.sh", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(aside, orphan)); err != nil {
			t.Errorf("%s was not kept in %s: %v", orphan, aside, err)
		}
	}
	// Discovery accepts what is left.
	r, _ := workflow.NewRoot(root)
	stage, err := r.ResolveStage(config.CustomValidatorDir)
	if err != nil {
		t.Fatal(err)
	}
	if scripts, err := workflow.Discover(stage); err != nil || len(scripts) != 2 {
		t.Errorf("discover = %v, %v", scripts, err)
	}
}

func TestListCustomValidators_SaysWhyTheDirectoryIsRefused(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	if l, _ := svc.ListCustomValidators(ctx, CustomValidatorsFilter{}); l.DirectoryRefused != "" {
		t.Fatalf("a healthy directory is reported refused: %q", l.DirectoryRefused)
	}
	dir := filepath.Join(root, config.CustomValidatorDir)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	l, err := svc.ListCustomValidators(ctx, CustomValidatorsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(l.DirectoryRefused, "permissions") {
		t.Errorf("DirectoryRefused = %q, want the custody refusal", l.DirectoryRefused)
	}
}

func TestCreateCustomValidator_NamesThatDifferOnlyByCaseAreOneName(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "Foo", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "foo", Pattern: "^b", Script: goodValidator + "# other\n"})
	if !errors.Is(err, ErrValidatorExists) || !strings.Contains(err.Error(), "Foo") {
		t.Errorf("err = %v, want ErrValidatorExists naming Foo", err)
	}
	if body, _ := os.ReadFile(filepath.Join(root, config.CustomValidatorDir, "Foo.local.sh")); string(body) != goodValidator {
		t.Error("the refused name overwrote Foo's script")
	}
}

// ---------------------------------------------------------- write path

func TestWriteValidatorScript_AFailedRollbackIsNotSilent(t *testing.T) {
	_, _, rootPath := openWorkflowSaveService(t)
	root, err := workflow.NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := writeValidatorScript(root, "x.local.sh", []byte(goodValidator))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(rootPath, config.CustomValidatorDir)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o750) })

	if err := restore(); err == nil {
		t.Error("restoring into a directory that cannot be written reported success")
	}
}

func TestUpdateCustomValidator_AStoredScriptTooBigToBackUpIsRefusedUntouched(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, config.CustomValidatorDir, "v.local.sh")
	big := []byte(goodValidator + "# " + strings.Repeat("x", 300<<10) + "\n")
	if err := os.WriteFile(path, big, 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator + "echo new\n"})
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "remove") {
		t.Errorf("err = %v, want a refusal that says to remove the validator and add it again", err)
	}
	if now, _ := os.ReadFile(path); len(now) != len(big) {
		t.Error("the refused edit changed the oversized script")
	}
}

func TestReadBounded_AllocatesWhatItReadsNotTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sh")
	if err := os.WriteFile(path, []byte("tiny\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	body, err := readBounded(path, 64<<20)
	runtime.ReadMemStats(&m1)
	if err != nil || string(body) != "tiny\n" {
		t.Fatalf("readBounded = %q, %v", body, err)
	}
	if grew := m1.TotalAlloc - m0.TotalAlloc; grew > 1<<20 {
		t.Errorf("reading 5 bytes allocated %d bytes: the buffer is sized by the limit", grew)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 100)), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := readBounded(path, 10); err == nil {
		t.Error("a file over the limit was read")
	}
}

func TestCustomValidators_TheJournalIsScannedOutsideTheConfigLock(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := openWorkflowSaveService(t)
	seedValidatorArtifacts(t, svc, "MANIFEST-20260902T153521Z.txt")
	var held []string
	var mu sync.Mutex
	validatorScanHook = func(stage string, _ model.BackupSetID) {
		if stage != "start" {
			return
		}
		if svc.configMu.TryLock() {
			svc.configMu.Unlock()
			return
		}
		mu.Lock()
		held = append(held, stage)
		mu.Unlock()
	}
	t.Cleanup(func() { validatorScanHook = nil })

	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^MANIFEST-", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^MANIFEST-", ScriptSHA256: sha256Of(goodValidator)}); err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Errorf("the journal was scanned %d time(s) while the configuration lock was held", len(held))
	}
}

// ------------------------------------------------------ list and preview

func TestPreviewCustomValidator_StopsScanningWhenTheRequestIsCancelled(t *testing.T) {
	svc, _, _ := openWorkflowSaveService(t)
	seedValidatorArtifacts(t, svc, "MANIFEST-20260902T153521Z.txt", "MANIFEST-20260903T010203Z.txt")
	ctx, cancel := context.WithCancel(context.Background())
	validatorScanHook = func(stage string, _ model.BackupSetID) {
		if stage == "listed" {
			cancel()
		}
	}
	t.Cleanup(func() { validatorScanHook = nil })

	if _, err := svc.PreviewCustomValidator(ctx, `^MANIFEST-`, "", ""); !errors.Is(err, context.Canceled) {
		t.Errorf("preview after the request went away = %v, want context.Canceled", err)
	}
}

const validatorTwoSetConfig = "poll_interval: 15m\n" +
	"state:\n  database: DB\n" +
	"workflows:\n  root: ROOT\n" +
	"sources:\n  - id: production\n    backup_sets:\n" +
	"      - id: alpha\n        remote:\n          type: local\n        remote_path: R1\n        local_path: L1\n        include:\n          - \"*.dump\"\n        completion:\n          strategy: rename\n        stale_after: 24h\n" +
	"      - id: beta\n        remote:\n          type: local\n        remote_path: R2\n        local_path: L2\n        include:\n          - \"*.dump\"\n        completion:\n          strategy: rename\n        stale_after: 24h\n" +
	"retention:\n  timezone: UTC\n  week_starts_on: monday\n"

func openTwoSetValidatorService(t *testing.T) *BackupService {
	t.Helper()
	dir := symlinkFreeTempDir(t)
	root := filepath.Join(dir, "workflows")
	for _, d := range []string{root, filepath.Join(dir, "r1"), filepath.Join(dir, "l1"), filepath.Join(dir, "r2"), filepath.Join(dir, "l2")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	text := strings.NewReplacer("DB", filepath.Join(dir, "state.db"), "ROOT", root,
		"R1", filepath.Join(dir, "r1"), "L1", filepath.Join(dir, "l1"), "R2", filepath.Join(dir, "r2"), "L2", filepath.Join(dir, "l2")).Replace(validatorTwoSetConfig)
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, cleanup, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(); _ = cleanup() })
	return svc
}

func TestListCustomValidators_AnArtifactQueryReadsOnlyThatArtifactsSet(t *testing.T) {
	ctx := context.Background()
	svc := openTwoSetValidatorService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "g", Pattern: "^g", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	var listed []string
	validatorScanHook = func(stage string, set model.BackupSetID) {
		if stage == "listed" {
			listed = append(listed, set.String())
		}
	}
	t.Cleanup(func() { validatorScanHook = nil })

	l, err := svc.ListCustomValidators(ctx, CustomValidatorsFilter{ArtifactID: "production/beta/g-1.dump"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0] != "production/beta" {
		t.Errorf("the journal was read for %v, want production/beta alone", listed)
	}
	if l.Resolution == nil || l.Resolution.Validator != "g" {
		t.Errorf("resolution = %+v", l.Resolution)
	}
	if _, err := svc.ListCustomValidators(ctx, CustomValidatorsFilter{BackupSetID: "production/alpha", ArtifactID: "production/beta/g-1.dump"}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a backup_set that disagrees with the artifact's = %v, want ErrInvalidRequest", err)
	}
}

func TestListCustomValidators_HashesAScriptOnceUntilItsTimeOrSizeChange(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	v, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, config.CustomValidatorDir, "v.local.sh")
	info, _ := os.Stat(path)

	// Same size, same modification time, different bytes: the identity the
	// cache keys on has not moved, so the hash is not recomputed.
	same := []byte(strings.Replace(goodValidator, "test -s", "test -e", 1))
	if len(same) != len(goodValidator) {
		t.Fatal("fixture sizes differ")
	}
	if err := os.WriteFile(path, same, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	l, _ := svc.ListCustomValidators(ctx, CustomValidatorsFilter{})
	if len(l.Validators) != 1 || l.Validators[0].ScriptSHA256 != v.ScriptSHA256 {
		t.Errorf("the hash was recomputed for a file whose size and time did not change: %+v", l.Validators)
	}

	// A different size is a different file.
	longer := append(same, []byte("# more\n")...)
	if err := os.WriteFile(path, longer, 0o640); err != nil {
		t.Fatal(err)
	}
	l, _ = svc.ListCustomValidators(ctx, CustomValidatorsFilter{})
	if l.Validators[0].ScriptSHA256 != validatorScriptOf("v", longer).SHA256 {
		t.Errorf("a file of a new size kept its old hash: %+v", l.Validators[0])
	}
}

func TestListCustomValidators_ListsBackupsTwoPatternsBothMatch(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := openWorkflowSaveService(t)
	for _, req := range []CreateCustomValidatorRequest{
		{Name: "a", Pattern: `^A-`, Script: goodValidator},
		{Name: "b", Pattern: `\.txt$`, Script: goodValidator},
	} {
		if _, err := svc.CreateCustomValidator(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if l, _ := svc.ListCustomValidators(ctx, CustomValidatorsFilter{}); len(l.Ambiguities) != 0 {
		t.Fatalf("ambiguities with nothing in the journal: %+v", l.Ambiguities)
	}
	// A later backup lands that both patterns match: creation could not have
	// seen it coming.
	seedValidatorArtifacts(t, svc, "A-1.txt", "plain.dump")

	l, err := svc.ListCustomValidators(ctx, CustomValidatorsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Ambiguities) != 1 {
		t.Fatalf("ambiguities = %+v, want one", l.Ambiguities)
	}
	got := l.Ambiguities[0]
	if got.BackupSetID != "production/alpha" || strings.Join(got.Validators, ",") != "a,b" || got.Example != "production/alpha/A-1.txt" {
		t.Errorf("ambiguity = %+v", got)
	}
	// The wire carries it.
	w := CustomValidatorsWire(l)
	if len(w.Ambiguities) != 1 || w.Ambiguities[0].Example != "production/alpha/A-1.txt" {
		t.Errorf("wire ambiguities = %+v", w.Ambiguities)
	}
	if w2 := CustomValidatorsWire(CustomValidators{DirectoryRefused: "no"}); w2.DirectoryRefused != "no" {
		t.Errorf("wire directory_refused = %q", w2.DirectoryRefused)
	}
}

// --------------------------------------------------------------- audit

func TestCustomValidators_EveryWriteLeavesAnEventWithoutTheScript(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := openWorkflowSaveService(t)
	var stream bytes.Buffer
	svc.logger = obs.New(&stream, obs.LevelInfo)
	secret := goodValidator + "# SCRIPTBODYMARKER\n"

	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: `^a`, BackupSetID: "production/alpha", Script: secret}); err != nil {
		t.Fatal(err)
	}
	next := secret + "echo two\n"
	if _, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: `^b`, Script: next}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RemoveCustomValidator(ctx, "v"); err != nil {
		t.Fatal(err)
	}

	for event, want := range map[string]struct{ pattern, scope, sha string }{
		"validator_uploaded": {`^a`, "backup_set", sha256Of(secret)},
		"validator_updated":  {`^b`, "global", sha256Of(next)},
		"validator_removed":  {`^b`, "global", sha256Of(next)},
	} {
		evs := validatorEvents(t, &stream, event)
		if len(evs) != 1 {
			t.Errorf("%s events = %d, want 1\n%s", event, len(evs), stream.String())
			continue
		}
		ev := evs[0]
		if ev["name"] != "v" || ev["pattern"] != want.pattern || ev["scope"] != want.scope || ev["script_sha256"] != want.sha {
			t.Errorf("%s = %v, want name v pattern %s scope %s sha %s", event, ev, want.pattern, want.scope, want.sha[:8])
		}
		if _, ok := ev["script_size_bytes"]; !ok {
			t.Errorf("%s carries no script size: %v", event, ev)
		}
	}
	if strings.Contains(stream.String(), "SCRIPTBODYMARKER") {
		t.Error("a script's body reached the log")
	}
}
