package service

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
)

// #1221 at the service boundary: validator_env is a file-only key for now, so
// the patch doors that rewrite the file must carry it through untouched, and a
// file that spells it wrongly must not load.

func TestIssue1221_ThePatchDoorsKeepAValidatorEnvTheyDoNotExpose(t *testing.T) {
	ctx := context.Background()
	svc, configPath := openAssuranceService(t, "assurance:\n  recovery_test:\n    validator_env: workflow\n")

	if _, err := svc.UpdateAssuranceSettings(ctx, UpdateAssuranceSettingsRequest{ScrubCadence: ptr(48 * time.Hour)}); err != nil {
		t.Fatalf("global patch: %v", err)
	}
	if _, err := svc.UpdateBackupSetAssurance(ctx, inheritingSet, UpdateBackupSetAssuranceRequest{ScrubCadence: ptr(6 * time.Hour)}); err != nil {
		t.Fatalf("set patch: %v", err)
	}
	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "validator_env: workflow") {
		t.Errorf("a patch dropped the operator's opt-in:\n%s", raw)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Sources[0].BackupSets[0].Assurance.RecoveryTest.ValidatorEnv; got != config.ValidatorEnvWorkflow {
		t.Errorf("the reloaded set resolves validator_env to %q", got)
	}
}

func TestIssue1221_AMisspeltValidatorEnvInTheFileDoesNotLoad(t *testing.T) {
	configPath := writeTestConfigFileWithRetention(t, "")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(raw), "sources:\n", "assurance:\n  recovery_test:\n    validator_env: Workflow\nsources:\n", 1)
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(configPath); err == nil || !strings.Contains(err.Error(), "validator_env") {
		t.Errorf("Load err = %v, want a refusal naming validator_env", err)
	}
}
