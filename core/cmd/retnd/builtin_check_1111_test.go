package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1111 through the terminal: the flag, the wire shapes, and the two new
// lines of `retnd recovery-test`.

func TestIssue1111_TheBuiltinCheckFlagPatchesBothScopesAndEchoesBack(t *testing.T) {
	configPath := writeTestConfig(t)
	if code := run([]string{"settings", "assurance", "patch", "--recovery-test-builtin-check", "off", "--config", configPath}); code != 0 {
		t.Fatalf("global patch exit %d", code)
	}
	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "builtin_check") {
		t.Fatalf("not persisted:\n%s", raw)
	}
	var out string
	out = captureStdout(t, func() { _ = run([]string{"settings", "assurance", "--json", "--config", configPath}) })
	var doc struct {
		RecoveryTest struct {
			BuiltinCheck   string  `json:"builtin_check"`
			EffectiveCheck *string `json:"effective_check"`
		} `json:"recovery_test"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.RecoveryTest.BuiltinCheck != "off" {
		t.Fatalf("read back %q: %v %+v", out, err, doc)
	}
	if doc.RecoveryTest.EffectiveCheck != nil {
		t.Errorf("the deployment-wide policy has no set to look in, but answered effective_check %q", *doc.RecoveryTest.EffectiveCheck)
	}

	if code := run([]string{"backup-set", "assurance", cliSet, "--recovery-test-builtin-check", "structure", "--config", configPath}); code != 0 {
		t.Fatalf("set patch exit %d", code)
	}
	out = captureStdout(t, func() { _ = run([]string{"backup-set", "assurance", cliSet, "--json", "--config", configPath}) })
	var set struct {
		RecoveryTest struct {
			BuiltinCheck         string `json:"builtin_check"`
			EffectiveCheck       string `json:"effective_check"`
			EffectiveCheckReason string `json:"effective_check_reason"`
		} `json:"recovery_test"`
	}
	if err := json.Unmarshal([]byte(out), &set); err != nil || set.RecoveryTest.BuiltinCheck != "structure" || set.RecoveryTest.EffectiveCheck == "" {
		t.Fatalf("set read back %q: %v %+v", out, err, set)
	}
	if set.RecoveryTest.EffectiveCheck == "none" && set.RecoveryTest.EffectiveCheckReason == "" {
		t.Error("effective_check none with no reason")
	}
}

func TestIssue1111_AnInvalidBuiltinCheckExitsWithTheOrdinaryFailureCode(t *testing.T) {
	configPath := writeTestConfig(t)
	if code := run([]string{"settings", "assurance", "patch", "--recovery-test-builtin-check", "deep", "--config", configPath}); code == 0 {
		t.Error("deep was accepted")
	}
}

func TestIssue1111_TheWireCarriesBuiltinCheckAndTheEffectiveCheck(t *testing.T) {
	b, err := json.Marshal(backupSetAssuranceWire(service.BackupSetAssurance{
		BackupSetID:    "a/b",
		Policy:         service.AssurancePolicy{RecoveryTest: service.AssuranceRecoveryTestPolicy{Mode: "off", BuiltinCheck: "structure"}},
		EffectiveCheck: "none", EffectiveCheckReason: "the built-in check is off for this backup set and no validator is declared",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"builtin_check":"structure"`, `"effective_check":"none"`, `"effective_check_reason":"the built-in check is off`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s lacks %s", b, want)
		}
	}
	var back apicontract.BackupSetAssurance
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	got := backupSetAssuranceFromWire(back)
	if got.EffectiveCheck != "none" || got.Policy.RecoveryTest.BuiltinCheck != "structure" || got.EffectiveCheckReason == "" {
		t.Errorf("the engine route lost a field on the way back: %+v", got)
	}
	g, _ := json.Marshal(assuranceSettingsWire(service.AssuranceSettings{Policy: service.AssurancePolicy{RecoveryTest: service.AssuranceRecoveryTestPolicy{BuiltinCheck: "off"}}}))
	if !strings.Contains(string(g), `"builtin_check":"off"`) || strings.Contains(string(g), "effective_check") {
		t.Errorf("deployment-wide answer: %s", g)
	}
}

func TestIssue1111_RecoveryTestPrintsTheCheckAndFormatLinesAndJSONNamesTheCheck(t *testing.T) {
	declared := true
	out := service.RecoveryTestOutcome{
		BackupSet: "a/b", Artifact: "a/b/db.tar.zst", Medium: "local", State: "passed", DigestMatched: &declared,
		ValidatorID: "retnd-builtin-structure", CleanupState: "removed",
		Check: "builtin", CheckFormat: "zstd, 1 frame, content checksum verified, 48.2 GiB decoded and discarded",
	}
	text := captureStdout(t, func() { printRecoveryTest(out, false) })
	for _, want := range []string{
		"check:               built-in structure check (no validator is declared for this backup set)",
		"format:              zstd, 1 frame, content checksum verified, 48.2 GiB decoded and discarded",
		"validator:           retnd-builtin-structure",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	js := captureStdout(t, func() { printRecoveryTest(out, true) })
	var doc map[string]any
	if err := json.Unmarshal([]byte(js), &doc); err != nil || doc["check"] != "builtin" {
		t.Errorf("--json = %s (%v), want \"check\": \"builtin\"", js, err)
	}

	// A validator run says neither.
	out.Check, out.CheckFormat, out.ValidatorID = "", "", "10-check.local.sh"
	text = captureStdout(t, func() { printRecoveryTest(out, false) })
	if strings.Contains(text, "check:") || strings.Contains(text, "format:") {
		t.Errorf("a validator run printed a built-in line:\n%s", text)
	}
	js = captureStdout(t, func() { printRecoveryTest(out, true) })
	if strings.Contains(js, `"check":"builtin"`) {
		t.Errorf("a validator run says it was built in: %s", js)
	}
}
