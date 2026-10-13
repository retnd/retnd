package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/state"
)

// #1111 at the service boundary: builtin_check through the patch doors, the
// effective check on a set's answer, and which check a copy's verdict came from.

func ptr[T any](v T) *T { return &v }

func TestIssue1111_BuiltinCheckResolvesToStructureAndPatchesGloballyAndPerSet(t *testing.T) {
	ctx := context.Background()
	svc, configPath := openAssuranceService(t, "")

	got, err := svc.AssuranceSettings(ctx)
	if err != nil || got.Policy.RecoveryTest.BuiltinCheck != "structure" {
		t.Fatalf("default = %q (%v), want structure", got.Policy.RecoveryTest.BuiltinCheck, err)
	}

	if _, err := svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{RecoveryTestBuiltinCheck: ptr("off")}); err != nil {
		t.Fatalf("global off: %v", err)
	}
	set, err := svc.BackupSetAssurance(ctx, inheritingSet)
	if err != nil || set.Policy.RecoveryTest.BuiltinCheck != "off" || set.Override {
		t.Errorf("the inheriting set = %+v (%v): a global change reaches it", set.Policy.RecoveryTest, err)
	}
	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "builtin_check: \"off\"") && !strings.Contains(string(raw), "builtin_check: off") {
		t.Errorf("not persisted:\n%s", raw)
	}

	pinned, err := svc.UpdateBackupSetAssurance(ctx, inheritingSet, UpdateBackupSetAssuranceRequest{RecoveryTestBuiltinCheck: ptr("structure")})
	if err != nil || !pinned.Override || pinned.Policy.RecoveryTest.BuiltinCheck != "structure" {
		t.Errorf("per-set structure = %+v (%v)", pinned, err)
	}
	// Empty clears: the set follows the global again.
	cleared, err := svc.UpdateBackupSetAssurance(ctx, inheritingSet, UpdateBackupSetAssuranceRequest{RecoveryTestBuiltinCheck: ptr("")})
	if err != nil || cleared.Policy.RecoveryTest.BuiltinCheck != "off" {
		t.Errorf("after clearing = %+v (%v)", cleared.Policy.RecoveryTest, err)
	}
}

func TestIssue1111_AnythingElseIsRefusedAndTheFileIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	svc, configPath := openAssuranceService(t, "")
	before, _ := os.ReadFile(configPath)
	if _, err := svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{RecoveryTestBuiltinCheck: ptr("deep")}); !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "builtin_check") {
		t.Errorf("global deep err = %v", err)
	}
	if _, err := svc.UpdateBackupSetAssurance(ctx, inheritingSet, UpdateBackupSetAssuranceRequest{RecoveryTestBuiltinCheck: ptr("deep")}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("set deep err = %v", err)
	}
	if after, _ := os.ReadFile(configPath); string(before) != string(after) {
		t.Error("a refused patch changed the file")
	}
}

func TestIssue1111_ModeBuiltinIsAcceptedAndRefusedWithBuiltinCheckOffNamingBoth(t *testing.T) {
	ctx := context.Background()
	svc, _ := openAssuranceService(t, "")
	got, err := svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{RecoveryTestMode: ptr("builtin")})
	if err != nil || got.Policy.RecoveryTest.Mode != "builtin" {
		t.Fatalf("mode builtin: %+v (%v)", got.Policy.RecoveryTest, err)
	}
	_, err = svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{RecoveryTestBuiltinCheck: ptr("off")})
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "builtin_check") || !strings.Contains(err.Error(), "mode") {
		t.Errorf("builtin mode with the check off err = %v, want a refusal naming both keys", err)
	}
	if _, err := svc.UpdateBackupSetAssurance(ctx, inheritingSet, UpdateBackupSetAssuranceRequest{RecoveryTestBuiltinCheck: ptr("off")}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a set turning the check off under a builtin mode err = %v", err)
	}
}

func TestIssue1111_ValidatorIDsInTheReservedPrefixAreRefused(t *testing.T) {
	ctx := context.Background()
	svc, _ := openAssuranceService(t, "")
	if _, err := svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{RecoveryTestValidatorID: ptr("retnd-foo")}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("global err = %v", err)
	}
	if _, err := svc.UpdateBackupSetAssurance(ctx, inheritingSet, UpdateBackupSetAssuranceRequest{RecoveryTestMode: ptr("custom"), RecoveryTestCadence: ptr(48 * time.Hour), RecoveryTestValidatorID: ptr("retnd-foo")}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("set err = %v", err)
	}
}

func TestIssue1111_TheSetsAnswerSaysWhichCheckARunWouldUse(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		cfg    func(c *config.Config)
		stages interface{}
		want   string
		reason string
	}{
		"nothing declared, the artifact is a .zst": {nil, noValidatorStages{}, "builtin", ""},
		"a validator in place":                     {nil, &stubStages{script: "pg-check.local.sh"}, "validator", ""},
		"nothing declared, the built-in is off": {func(c *config.Config) {
			c.Sources[0].BackupSets[0].Assurance.RecoveryTest.BuiltinCheck = "off"
		}, noValidatorStages{}, "none", "the built-in check is off"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRecoveryFixture(t, fixtureOpts{cfg: func(c *config.Config) {
				requireRecoveryTest(c)
				if tc.cfg != nil {
					tc.cfg(c)
				}
			}})
			switch s := tc.stages.(type) {
			case noValidatorStages:
				f.b.state.Load().inner.Stages = s
			case *stubStages:
				f.b.state.Load().inner.Stages = s
			}
			got, err := f.b.BackupSetAssurance(ctx, "production/postgres")
			if err != nil {
				t.Fatal(err)
			}
			if got.EffectiveCheck != tc.want || !strings.Contains(got.EffectiveCheckReason, tc.reason) || (tc.want != "none" && got.EffectiveCheckReason != "") {
				t.Errorf("effective = %q (%q), want %q / %q", got.EffectiveCheck, got.EffectiveCheckReason, tc.want, tc.reason)
			}
		})
	}
}

func TestIssue1111_ACopysVerdictSaysWhichCheckProducedIt(t *testing.T) {
	for name, tc := range map[string]struct {
		state, id, want string
	}{
		"a validator pass":             {state.RecoveryPassed, "10-check.local.sh", "validator"},
		"a built-in pass":              {state.RecoveryPassed, "retnd-builtin-structure", "builtin"},
		"a built-in failure":           {state.RecoveryFailed, "retnd-builtin-structure", "builtin"},
		"a validator failure":          {state.RecoveryFailed, "10-check.local.sh", "validator"},
		"could not complete":           {state.RecoveryCouldNotComplete, "", ""},
		"a digest failure (no id)":     {state.RecoveryFailed, "", ""},
		"nothing evaluated":            {state.RecoveryUnknown, "", ""},
		"not configured":               {state.RecoveryNotConfigured, "", ""},
		"a pass that has gone overdue": {state.RecoveryOverdue, "retnd-builtin-structure", "builtin"},
	} {
		t.Run(name, func(t *testing.T) {
			p := state.Placement{Medium: state.MediumLocal, Status: state.PlacementActive}
			p.RecoveryState, p.RecoveryValidatorID = tc.state, tc.id
			if got := toServicePlacement(p, nil).RecoveryCheck; got != tc.want {
				t.Errorf("RecoveryCheck = %q, want %q", got, tc.want)
			}
		})
	}
}
