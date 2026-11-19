package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// #1152, settings and backup-set retention.

func TestSettingsJSON_IsTheSettingsResponse_1152(t *testing.T) {
	configPath := writeTestConfigWithDeploymentPolicy(t)

	out, code := cliJSON(t, "settings", "--json", "--config", configPath)
	if code != exitOK {
		t.Fatalf("code = %d:\n%s", code, out)
	}
	var got apicontract.SettingsResponse
	strictDecode(t, out, &got)

	if got.Retention.Timezone != "America/Vancouver" || got.Retention.WeekStartsOn != "sunday" || !got.Retention.ProtectLastKnownGood {
		t.Errorf("retention = %+v", got.Retention)
	}
	byName := map[string]apicontract.RetentionTier{}
	for _, tier := range got.Retention.Tiers {
		byName[tier.Name] = tier
	}
	if byName["daily"].Keep != 90 || byName["monthly"].Keep != 60 || byName["weekly"].Keep != 24 {
		t.Errorf("the resolved chain is not the configured one: %+v", got.Retention.Tiers)
	}
	if got.Service.PollIntervalSeconds != 900 {
		t.Errorf("poll_interval_seconds = %d, want 900 (15m)", got.Service.PollIntervalSeconds)
	}
	if got.Capacity.BackupRoot == "" || !strings.HasSuffix(got.Capacity.BackupRoot, "local") {
		t.Errorf("capacity.backup_root = %q, want the directory derived from the set's local_path", got.Capacity.BackupRoot)
	}
	// The schema travels with the settings, as it does on the route: a
	// client validates against the engine's rules, not a copy.
	if len(got.Schema.Retention.DefaultTiers) == 0 || len(got.Schema.Retention.Granularities) == 0 ||
		got.Schema.Service.MinPollIntervalSeconds == 0 || len(got.Schema.Storage.VerificationClasses) == 0 {
		t.Errorf("schema is missing pieces: %+v", got.Schema)
	}
	if !got.Persisted {
		t.Errorf("persisted = false for a configuration read off a file")
	}
	if got.FirstRun {
		t.Errorf("first_run = true for a deployment with a backup set")
	}
	m := rawKeys(t, out)
	requireKeys(t, m, "retention", "capacity", "service", "mediums", "schema", "first_run", "persisted")
	if got.Mediums == nil {
		t.Errorf("mediums decoded to nil; it is always an array")
	}
}

func TestSettingsJSON_AFreshDeploymentIsFirstRun_1152(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	body := "poll_interval: 1h\nstate:\n  database: " + filepath.Join(dir, "state.db") + "\nsources: []\n"
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := cliJSON(t, "settings", "--json", "--config", configPath)
	if code != exitOK {
		t.Fatalf("settings --json failed on a fresh deployment (code %d):\n%s", code, out)
	}
	var got apicontract.SettingsResponse
	strictDecode(t, out, &got)
	if !got.FirstRun {
		t.Errorf("first_run = false for a configuration with no backup set, default settings and an empty journal:\n%s", out)
	}
}

func TestSettingsJSON_IsReadOnly_1152(t *testing.T) {
	configPath := writeTestConfig(t)
	var code int
	stderr := captureStderr(t, func() {
		code = run([]string{"settings", "patch", "--json", "--timezone", "UTC", "--config", configPath})
	})
	if code != exitUsage {
		t.Errorf("settings patch --json exited %d, want %d: --json is the read's object, and a patch must not accept a flag it then ignores", code, exitUsage)
	}
	if !strings.Contains(stderr, "--json") {
		t.Errorf("the refusal does not name the flag:\n%s", stderr)
	}
}

func TestBackupSetRetentionJSON_IsTheBackupSetRetentionObject_1152(t *testing.T) {
	configPath := writeTestConfigWithDeploymentPolicy(t)

	// Inheriting: override is present and null, not absent.
	out, code := cliJSON(t, retentionArgs(configPath, "--json")...)
	if code != exitOK {
		t.Fatalf("code = %d:\n%s", code, out)
	}
	var got apicontract.BackupSetRetention
	strictDecode(t, out, &got)
	if got.BackupSetID != cliSet || got.IsOverride || got.Override != nil {
		t.Errorf("an inheriting set: %+v", got)
	}
	if got.Effective.Timezone != "America/Vancouver" || len(got.Effective.Tiers) == 0 {
		t.Errorf("effective = %+v", got.Effective)
	}
	if string(rawKeys(t, out)["override"]) != "null" {
		t.Errorf("override = %s, want the key present and null", rawKeys(t, out)["override"])
	}
	requireKeys(t, rawKeys(t, out), "backup_set_id", "is_override", "effective", "deployment", "override")
}

func TestBackupSetRetentionJSON_ReadsBackWhatASetAndAClearLeft_1152(t *testing.T) {
	configPath := writeTestConfigWithDeploymentPolicy(t)

	if got := run(retentionArgs(configPath, "--daily-days", "3", "--weekly-months", "1", "--monthly-months", "2")); got != 0 {
		t.Fatalf("set = %d", got)
	}
	out, code := cliJSON(t, retentionArgs(configPath, "--json")...)
	if code != exitOK {
		t.Fatalf("--json after a set = %d:\n%s", code, out)
	}
	var set apicontract.BackupSetRetention
	strictDecode(t, out, &set)
	if !set.IsOverride || set.Override == nil || set.Override.DailyDays != 3 || set.Override.MonthlyMonths != 2 {
		t.Errorf("after a set: %+v / %+v", set, set.Override)
	}
	if set.Deployment.Tiers[0].Keep != 90 {
		t.Errorf("the deployment half moved: %+v", set.Deployment)
	}

	if got := run(retentionArgs(configPath, "--inherit")); got != 0 {
		t.Fatalf("--inherit = %d", got)
	}
	out, code = cliJSON(t, retentionArgs(configPath, "--json")...)
	if code != exitOK {
		t.Fatalf("--json after a clear = %d:\n%s", code, out)
	}
	var cleared apicontract.BackupSetRetention
	strictDecode(t, out, &cleared)
	if cleared.IsOverride || cleared.Override != nil {
		t.Errorf("after a clear: %+v", cleared)
	}
}

// A write logs its startup lines on stdout, so a JSON document printed after
// them would not be one document. --json is therefore the read's, and a write
// that is handed it is refused before anything is opened or changed.
func TestBackupSetRetentionJSON_IsReadOnly_1152(t *testing.T) {
	for _, flags := range [][]string{
		{"--daily-days", "3", "--weekly-months", "1", "--monthly-months", "2", "--json"},
		{"--inherit", "--json"},
	} {
		configPath := writeTestConfigWithDeploymentPolicy(t)
		before := readFile(t, configPath)
		var code int
		stderr := captureStderr(t, func() { code = run(retentionArgs(configPath, flags...)) })
		if code != exitUsage {
			t.Errorf("%v exited %d, want %d", flags, code, exitUsage)
		}
		if !strings.Contains(stderr, "--json") {
			t.Errorf("%v: the refusal does not name the flag:\n%s", flags, stderr)
		}
		if readFile(t, configPath) != before {
			t.Errorf("%v: a refused command changed the configuration", flags)
		}
	}
}

func TestBackupSetRetentionJSON_AnUnknownSetPrintsNothingAndFails_1152(t *testing.T) {
	configPath := writeTestConfig(t)
	out, code := cliJSON(t, "backup-set", "retention", "--config", configPath, "--json", "production/nope")
	if code != exitFailure {
		t.Errorf("exit = %d, want 1", code)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a refused read wrote to stdout:\n%s", out)
	}
}
