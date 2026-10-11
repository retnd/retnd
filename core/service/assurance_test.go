package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// EPIC Q, Q1.4 (#982): the service half of the assurance configuration.

// openAssuranceService opens a real file-backed deployment with two sets:
// "postgres-primary" inherits the deployment's assurance policy and "pinned"
// carries its own scrub cadence.
func openAssuranceService(t *testing.T, top string) (*BackupService, string) {
	t.Helper()
	configPath := writeTestConfigFileWithRetention(t, "")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(configPath)
	body := string(raw) +
		"      - id: pinned\n" +
		"        remote:\n          type: local\n" +
		"        remote_path: " + filepath.Join(dir, "remote") + "\n" +
		"        local_path: " + filepath.Join(dir, "local-pinned") + "\n" +
		"        include:\n          - \"*.dump\"\n" +
		"        completion:\n          strategy: rename\n" +
		"        stale_after: 24h\n" +
		"        assurance:\n          scrub:\n            cadence: 5h\n"
	// The top-level block has to sit before "sources:".
	body = strings.Replace(body, "sources:\n", top+"sources:\n", 1)
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, cleanup, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })
	return svc, configPath
}

const (
	inheritingSet = "production/postgres-primary"
	pinnedSet     = "production/pinned"
)

func TestAssuranceSettings_ReportTheResolvedDefaultsAndTheWrittenNoPerHostKnob(t *testing.T) {
	svc, _ := openAssuranceService(t, "")
	got, err := svc.AssuranceSettings(context.Background())
	if err != nil {
		t.Fatalf("AssuranceSettings: %v", err)
	}
	if got.Policy.Scrub.Cadence != 7*24*time.Hour || got.Policy.Scrub.Strategy != "auto" {
		t.Errorf("scrub = %+v, want the 7 day auto default", got.Policy.Scrub)
	}
	if got.Configured {
		t.Error("a config with no assurance block must report configured=false")
	}
	want := map[string]bool{"staging_root": true, "staging_ttl": true, "max_concurrent_full_reads": true, "max_per_destination": true}
	for _, k := range got.HostKnobs {
		if k.PerSet || strings.TrimSpace(k.Reason) == "" {
			t.Errorf("host knob %s must say no per set and carry its reason: %+v", k.Name, k)
		}
		delete(want, k.Name)
	}
	if len(want) > 0 {
		t.Errorf("host knobs missing a written no: %v", want)
	}
}

// The retention-override trap, at the service boundary: the patch must reach
// the set that inherits, with no restart, and leave the pinned one alone.
func TestUpdateAssuranceSettings_MovesAnInheritingSetAndNotAPinnedOne(t *testing.T) {
	svc, configPath := openAssuranceService(t, "")
	ctx := context.Background()
	before, _ := svc.BackupSetAssurance(ctx, inheritingSet)
	if before.Policy.Scrub.Cadence != 7*24*time.Hour {
		t.Fatalf("precondition: inheriting cadence = %s", before.Policy.Scrub.Cadence)
	}

	twelve := 12 * time.Hour
	if _, err := svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{ScrubCadence: &twelve}); err != nil {
		t.Fatalf("UpdateAssuranceSettings: %v", err)
	}
	after, err := svc.BackupSetAssurance(ctx, inheritingSet)
	if err != nil {
		t.Fatal(err)
	}
	if after.Policy.Scrub.Cadence != twelve || after.Override {
		t.Errorf("inheriting set = %+v, want 12h and no override", after)
	}
	pinned, _ := svc.BackupSetAssurance(ctx, pinnedSet)
	if pinned.Policy.Scrub.Cadence != 5*time.Hour || !pinned.Override {
		t.Errorf("pinned set = %+v, a global change must not move it", pinned)
	}

	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "cadence: 12h0m0s") && !strings.Contains(string(raw), "cadence: 12h") {
		t.Errorf("the patch was not persisted:\n%s", raw)
	}
	if strings.Contains(string(raw), "staging_ttl") {
		t.Errorf("the patch froze unrelated defaults into the operator's file:\n%s", raw)
	}
}

func TestUpdateAssuranceSettings_RefusesWhatValidateRefusesAndLeavesTheFileAlone(t *testing.T) {
	svc, configPath := openAssuranceService(t, "")
	before, _ := os.ReadFile(configPath)
	tiny := time.Minute
	_, err := svc.UpdateAssuranceSettings(context.Background(), UpdateAssuranceSettingsRequest{ScrubCadence: &tiny})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Error("a refused patch changed the file")
	}
	if _, err := svc.UpdateAssuranceSettings(context.Background(), UpdateAssuranceSettingsRequest{}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("an empty patch err = %v, want ErrInvalidRequest", err)
	}
}

func TestUpdateBackupSetAssurance_PatchThenResetReturnsTheSetToInheritance(t *testing.T) {
	svc, configPath := openAssuranceService(t, "")
	ctx := context.Background()

	required := "required"
	if _, err := svc.UpdateBackupSetAssurance(ctx, inheritingSet, UpdateBackupSetAssuranceRequest{RecoveryTestMode: &required}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("required with no validator err = %v, want ErrInvalidRequest", err)
	}
	validator := "trailer-marker"
	got, err := svc.UpdateBackupSetAssurance(ctx, inheritingSet, UpdateBackupSetAssuranceRequest{RecoveryTestMode: &required, RecoveryTestValidatorID: &validator})
	if err != nil {
		t.Fatalf("UpdateBackupSetAssurance: %v", err)
	}
	if !got.Override || got.Policy.RecoveryTest.Mode != "required" || got.Policy.RecoveryTest.ValidatorID != validator {
		t.Errorf("after patch = %+v", got)
	}

	reset, err := svc.ResetBackupSetAssurance(ctx, inheritingSet)
	if err != nil {
		t.Fatalf("ResetBackupSetAssurance: %v", err)
	}
	if reset.Override || reset.Policy.RecoveryTest.Mode != "off" {
		t.Errorf("after reset = %+v, want inheriting and off", reset)
	}
	raw, _ := os.ReadFile(configPath)
	if strings.Count(string(raw), "assurance:") != 1 {
		t.Errorf("only the pinned set should still carry a block:\n%s", raw)
	}
	if _, err := svc.BackupSetAssurance(ctx, "production/nope"); !errors.Is(err, ErrBackupSetNotFound) {
		t.Errorf("unknown set err = %v", err)
	}
}

func TestAssuranceDue_NextDueFollowsTheResolvedCadenceAndUnknownIsDue(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	verified := now.Add(-10 * time.Hour)
	day := 24 * time.Hour

	item := assuranceDueFor(now, "s/a", dueSubject{ID: "a1", Name: "a.dump", LastAssured: verified, Basis: DueBasisVerifiedAt}, day)
	if want := verified.Add(day); !item.NextDueAt.Equal(want) || item.Due || item.Overdue {
		t.Errorf("fresh copy = %+v, want next due %s and not due", item, want)
	}
	item = assuranceDueFor(now, "s/a", dueSubject{ID: "a1", LastAssured: verified}, 6*time.Hour)
	if !item.Due || item.Overdue {
		t.Errorf("a shorter cadence must make the same copy due but not overdue: %+v", item)
	}
	item = assuranceDueFor(now, "s/a", dueSubject{ID: "a1", LastAssured: now.Add(-3 * day)}, day)
	if !item.Due || !item.Overdue {
		t.Errorf("a copy a whole cadence past due is overdue: %+v", item)
	}
	item = assuranceDueFor(now, "s/a", dueSubject{ID: "a1"}, day)
	if !item.Due || !item.Overdue || item.Basis != DueBasisNever {
		t.Errorf("a copy nobody has looked at is due and overdue, never green: %+v", item)
	}
}

// Panel finding C3: validator_id names a recovery-test script by file name,
// so a name the registry has never heard of is fine and a name that could
// never be a script's file name is refused at save time.
func TestAssuranceWrites_TakeAScriptNameAndRefuseWhatCouldNotBeOne(t *testing.T) {
	svc, configPath := openAssuranceService(t, "")
	ctx := context.Background()
	mode := "required"

	ok := "nightly-restore-check"
	if _, err := svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{RecoveryTestMode: &mode, RecoveryTestValidatorID: &ok}); err != nil {
		t.Fatalf("a script name nobody registered must save: %v", err)
	}
	if _, err := svc.UpdateBackupSetAssurance(ctx, inheritingSet, UpdateBackupSetAssuranceRequest{RecoveryTestMode: &mode, RecoveryTestValidatorID: &ok}); err != nil {
		t.Fatalf("a per-set script name must save: %v", err)
	}

	before, _ := os.ReadFile(configPath)
	for _, bad := range []string{"nightly.local.sh", "-nightly", ".nightly", "a b", "a/b"} {
		bad := bad
		if _, err := svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{RecoveryTestMode: &mode, RecoveryTestValidatorID: &bad}); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("global write of %q err = %v, want ErrInvalidRequest", bad, err)
		}
		if _, err := svc.UpdateBackupSetAssurance(ctx, inheritingSet, UpdateBackupSetAssuranceRequest{RecoveryTestMode: &mode, RecoveryTestValidatorID: &bad}); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("per-set write of %q err = %v, want ErrInvalidRequest", bad, err)
		}
	}
	if after, _ := os.ReadFile(configPath); string(before) != string(after) {
		t.Error("a refused write changed the file")
	}
}

// Review finding: a hand-built Config that never ran Validate has a zero
// resolved cadence, and a zero must not read as "continuously due".
func TestAssuranceDue_AZeroResolvedCadenceFallsBackToTheDefault(t *testing.T) {
	if got := effectiveScrubCadence(0); got != 7*24*time.Hour {
		t.Errorf("effectiveScrubCadence(0) = %s, want the 7 day default", got)
	}
	if got := effectiveScrubCadence(3 * time.Hour); got != 3*time.Hour {
		t.Errorf("a resolved cadence must pass through, got %s", got)
	}
}

// Panel finding C3: the staging root is live now (the engine creates files in
// it), so a PATCH may not point it at a place whose custody the product could
// not vouch for.
func TestUpdateAssuranceSettings_RefusesAStagingRootThatIsNotOurs(t *testing.T) {
	svc, configPath := openAssuranceService(t, "")
	ctx := context.Background()
	dir := t.TempDir()

	open := filepath.Join(dir, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	before, _ := os.ReadFile(configPath)
	for name, bad := range map[string]string{
		"world-writable": open,
		"a file":         file,
		"a symlink":      link,
		"dot-dot":        filepath.Join(dir, "real", "..", "real"),
		"under a file":   filepath.Join(file, "stage"),
	} {
		bad := bad
		if _, err := svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{StagingRoot: &bad}); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	if after, _ := os.ReadFile(configPath); string(before) != string(after) {
		t.Error("a refused write changed the file")
	}

	creatable := filepath.Join(real, "recovery-staging")
	if _, err := svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{StagingRoot: &creatable}); err != nil {
		t.Errorf("a creatable root under a private directory must save: %v", err)
	}
	if _, err := svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{StagingRoot: &real}); err != nil {
		t.Errorf("an existing private directory must save: %v", err)
	}
	empty := ""
	if _, err := svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{StagingRoot: &empty}); err != nil {
		t.Errorf("clearing the root must save: %v", err)
	}
}
