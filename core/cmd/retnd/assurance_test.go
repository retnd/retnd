package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// EPIC Q, Q1.4 (#982): the CLI half of the assurance configuration, against
// a real deployment.

func TestSettingsAssurance_ShowReportsDefaultsAndTheWrittenNo(t *testing.T) {
	configPath := writeTestConfig(t)
	var code int
	out := captureStdout(t, func() { code = run([]string{"settings", "assurance", "--config", configPath}) })
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"the defaults", "scrub: every 168h0m0s, strategy auto", "recovery test: off", "staging_ttl", "no per-set override"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestSettingsAssurance_PatchPersistsOnlyWhatWasNamedAndZeroClears(t *testing.T) {
	configPath := writeTestConfig(t)
	if code := run([]string{"settings", "assurance", "patch", "--scrub-cadence", "12h", "--json", "--config", configPath}); code != 0 {
		t.Fatalf("patch exit %d", code)
	}
	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "assurance:") || strings.Contains(string(raw), "staging_ttl") {
		t.Fatalf("the patch wrote the wrong thing:\n%s", raw)
	}
	var doc struct {
		Scrub struct {
			CadenceSeconds int64 `json:"cadence_seconds"`
		} `json:"scrub"`
	}
	out := captureStdout(t, func() { _ = run([]string{"settings", "assurance", "--json", "--config", configPath}) })
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Scrub.CadenceSeconds != 43200 {
		t.Fatalf("read back %q: %v %+v", out, err, doc)
	}
	// An explicit zero is a request: it clears the key, and the block with it.
	if code := run([]string{"settings", "assurance", "patch", "--scrub-cadence", "0", "--config", configPath}); code != 0 {
		t.Fatalf("clear exit %d", code)
	}
	raw, _ = os.ReadFile(configPath)
	if strings.Contains(string(raw), "assurance") {
		t.Errorf("clearing the last field must remove the block:\n%s", raw)
	}
}

func TestBackupSetAssurance_PatchResetAndTheGlobalOnlyRefusal(t *testing.T) {
	configPath := writeTestConfig(t)
	id := cliSet
	if code := run([]string{"backup-set", "assurance", id, "--scrub-cadence", "7200s", "--config", configPath}); code != 0 {
		t.Fatalf("patch exit %d", code)
	}
	out := captureStdout(t, func() { _ = run([]string{"backup-set", "assurance", id, "--config", configPath}) })
	if !strings.Contains(out, "has its own assurance block") || !strings.Contains(out, "every 2h0m0s") {
		t.Errorf("show after patch:\n%s", out)
	}
	if code := run([]string{"backup-set", "assurance", id, "--reset", "--config", configPath}); code != 0 {
		t.Fatalf("reset exit %d", code)
	}
	out = captureStdout(t, func() { _ = run([]string{"backup-set", "assurance", id, "--config", configPath}) })
	if !strings.Contains(out, "inherits the deployment's policy") {
		t.Errorf("show after reset:\n%s", out)
	}
	if code := run([]string{"backup-set", "assurance", id, "--staging-ttl", "1h", "--config", configPath}); code != exitUsage {
		t.Errorf("a global-only flag on a set exited %d, want the usage code %d", code, exitUsage)
	}
	if code := run([]string{"backup-set", "assurance", id, "--reset", "--scrub-cadence", "2h", "--config", configPath}); code != exitUsage {
		t.Errorf("--reset with a write exited %d, want usage", code)
	}
}

func TestSettingsAssurance_RefusalsExitWithTheOrdinaryFailureCode(t *testing.T) {
	configPath := writeTestConfig(t)
	for name, argv := range map[string][]string{
		"cadence under the floor":    {"settings", "assurance", "patch", "--scrub-cadence", "1m"},
		"required with no validator": {"settings", "assurance", "patch", "--recovery-test-mode", "required"},
	} {
		if code := run(append(argv, "--config", configPath)); code != exitFailure {
			t.Errorf("%s exited %d, want %d", name, code, exitFailure)
		}
	}
	if code := run([]string{"settings", "assurance", "patch", "--config", configPath}); code != exitUsage {
		t.Errorf("an empty patch exited %d, want usage", code)
	}
}

func TestSettingsAssuranceDue_ListsEveryScrubEligibleCopyAsJSON(t *testing.T) {
	configPath := writeTestConfig(t)
	out := captureStdout(t, func() {
		if code := run([]string{"settings", "assurance", "due", "--json", "--config", configPath}); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
	var doc struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Items == nil {
		t.Fatalf("not the API body shape: %q (%v)", out, err)
	}
}
