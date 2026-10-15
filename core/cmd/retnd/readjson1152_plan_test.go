package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// #1152, the retention preview (with its plan_id) and catalog rebuild.

// aDeploymentWithOneBackup runs one real cycle, so the journal, the local
// copy and the recovery sidecar all exist.
func aDeploymentWithOneBackup(t *testing.T) string {
	t.Helper()
	configPath := writeTestConfig(t)
	if got := run([]string{"run", "--config", configPath}); got != exitOK {
		t.Fatalf("run = %d", got)
	}
	return configPath
}

func TestRetentionJSON_IsTheRetentionPlanWithItsPlanID_1152(t *testing.T) {
	configPath := aDeploymentWithOneBackup(t)

	out, code := cliJSON(t, "retention", "--json", "--config", configPath, cliSet)
	if code != exitOK {
		t.Fatalf("code = %d:\n%s", code, out)
	}
	var plan apicontract.RetentionPlan
	strictDecode(t, out, &plan)
	if !strings.HasPrefix(plan.PlanID, "retplan_") {
		t.Errorf("plan_id = %q, want the id the apply step takes", plan.PlanID)
	}
	if plan.BackupSetID != cliSet || plan.ConfigRevision == "" || plan.InventoryRevision == "" || plan.ExpiresAt == "" {
		t.Errorf("plan = %+v", plan)
	}
	if len(plan.Verdicts) != 1 || plan.Verdicts[0].Action != "KEEP" || !strings.HasSuffix(plan.Verdicts[0].Artifact, "backup.dump") {
		t.Fatalf("verdicts = %+v", plan.Verdicts)
	}
	if plan.KeepCount != 1 || plan.DeleteCount != 0 {
		t.Errorf("keep=%d delete=%d", plan.KeepCount, plan.DeleteCount)
	}
	if plan.Retention.Timezone != "UTC" || len(plan.Retention.Tiers) == 0 || plan.RetentionIsOverride {
		t.Errorf("retention = %+v override=%v", plan.Retention, plan.RetentionIsOverride)
	}
	requireKeys(t, rawKeys(t, out), "plan_id", "backup_set_id", "inventory_revision", "config_revision", "expires_at", "keep_count", "delete_count", "reclaim_bytes", "verdicts", "retention", "retention_is_override")

	// Each preview mints its own plan: an id is not a function of the set.
	again, _ := cliJSON(t, "retention", "--json", "--config", configPath, cliSet)
	var second apicontract.RetentionPlan
	strictDecode(t, again, &second)
	if second.PlanID == plan.PlanID {
		t.Errorf("two previews returned the same plan_id %q", plan.PlanID)
	}
}

func TestRetentionJSON_ASetWithNoBackupsIsAnEmptyVerdictArray_1152(t *testing.T) {
	configPath := writeTestConfig(t)
	out, code := cliJSON(t, "retention", "--json", "--config", configPath, cliSet)
	if code != exitOK {
		t.Fatalf("code = %d:\n%s", code, out)
	}
	if !strings.Contains(strings.Join(strings.Fields(out), ""), `"verdicts":[]`) {
		t.Errorf("verdicts is not an empty array on the wire:\n%s", out)
	}
}

func TestRetentionJSON_RefusesWhatItCannotSay_1152(t *testing.T) {
	configPath := aDeploymentWithOneBackup(t)
	for name, args := range map[string][]string{
		"no backup set named":     {"retention", "--json", "--config", configPath},
		"a what-if override flag": {"retention", "--json", "--daily-days", "3", "--config", configPath, cliSet},
	} {
		t.Run(name, func(t *testing.T) {
			var code int
			var out string
			stderr := captureStderr(t, func() { out, code = cliJSON(t, args...) })
			if code != exitUsage {
				t.Errorf("exit = %d, want %d", code, exitUsage)
			}
			if strings.TrimSpace(out) != "" {
				t.Errorf("a refusal wrote to stdout:\n%s", out)
			}
			if !strings.Contains(stderr, "--json") {
				t.Errorf("the refusal does not name the flag:\n%s", stderr)
			}
		})
	}
}

func TestRetentionJSON_AnUnknownSetPrintsNothingAndFails_1152(t *testing.T) {
	configPath := writeTestConfig(t)
	out, code := cliJSON(t, "retention", "--json", "--config", configPath, "production/nope")
	if code != exitFailure {
		t.Errorf("exit = %d, want 1", code)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a refused read wrote to stdout:\n%s", out)
	}
}

func TestRetentionText_StillPrintsNoPlanID_1152(t *testing.T) {
	// retnd-tests pins this command's stdout line for line, so the text form
	// does not grow a plan id: --json is where the plan is.
	configPath := aDeploymentWithOneBackup(t)
	out, code := cliJSON(t, "retention", "--config", configPath, cliSet)
	if code != exitOK || strings.Contains(out, "retplan_") {
		t.Errorf("code=%d, text carries a plan id:\n%s", code, out)
	}
}

// --- catalog rebuild -----------------------------------------------------

func TestCatalogRebuildJSON_ReportsALostJournalComingBack_1152(t *testing.T) {
	configPath := aDeploymentWithOneBackup(t)
	dir := filepath.Dir(configPath)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(dir, "state.db"+suffix)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	out, code := cliJSON(t, "catalog", "rebuild", "--dry-run", "--json", "--config", configPath)
	if code != exitOK {
		t.Fatalf("dry run = %d:\n%s", code, out)
	}
	var dry apicontract.CatalogReportResponse
	strictDecode(t, out, &dry)
	if !dry.DryRun || dry.Scanned != 1 || dry.Reconstructed != 1 || dry.AlreadyPresent != 0 || len(dry.Failures) != 0 {
		t.Errorf("dry run = %+v", dry)
	}
	if !strings.Contains(strings.Join(strings.Fields(out), ""), `"failures":[]`) {
		t.Errorf("failures is not an empty array on the wire:\n%s", out)
	}

	out, code = cliJSON(t, "catalog", "rebuild", "--json", "--config", configPath)
	if code != exitOK {
		t.Fatalf("rebuild = %d:\n%s", code, out)
	}
	var real apicontract.CatalogReportResponse
	strictDecode(t, out, &real)
	if real.DryRun || real.Scanned != 1 || real.Reconstructed != 1 {
		t.Errorf("rebuild = %+v", real)
	}

	out, code = cliJSON(t, "catalog", "rebuild", "--json", "--config", configPath)
	var again apicontract.CatalogReportResponse
	strictDecode(t, out, &again)
	if code != exitOK || again.Reconstructed != 0 || again.AlreadyPresent != 1 {
		t.Errorf("a second rebuild: code=%d %+v (running it against a healthy journal is a no-op)", code, again)
	}
}

func TestCatalogRebuildJSON_AnUnreadableManifestIsAFailureAndExitsOne_1152(t *testing.T) {
	configPath := aDeploymentWithOneBackup(t)
	dir := filepath.Dir(configPath)
	var sidecars []string
	_ = filepath.Walk(filepath.Join(dir, "local"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && p != filepath.Join(dir, "local", "backup.dump") {
			sidecars = append(sidecars, p)
		}
		return nil
	})
	if len(sidecars) == 0 {
		t.Fatalf("a cycle left no recovery sidecar under %s, so there is nothing to corrupt", dir)
	}
	for _, p := range sidecars {
		if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, code := cliJSON(t, "catalog", "rebuild", "--dry-run", "--json", "--config", configPath)
	var got apicontract.CatalogReportResponse
	strictDecode(t, out, &got)
	if len(got.Failures) == 0 || got.Failures[0].BackupSetID != cliSet || got.Failures[0].Reason == "" {
		t.Errorf("failures = %+v, want the unreadable manifest named", got.Failures)
	}
	if code != exitFailure {
		t.Errorf("exit = %d, want 1: a manifest nobody could read is something an operator has to look at", code)
	}
}
