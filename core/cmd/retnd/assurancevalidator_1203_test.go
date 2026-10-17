package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1203: `retnd assurance validator`, against a real configuration file and a
// real workflow root, with nothing serving (so the writes go to the file).

// validatorWorkflowRoot is a workflow root discovery's custody rules accept:
// beside the test, not under a world-writable /tmp.
func validatorWorkflowRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".cli-validators-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(abs, 0o750); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func writeValidatorTestConfig(t *testing.T) (configPath, root string) {
	t.Helper()
	configPath = writeTestConfig(t)
	root = validatorWorkflowRoot(t)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "\nworkflows:") {
		t.Fatal("the fixture already has a workflows block; this helper would write a second one")
	}
	if err := os.WriteFile(configPath, append(raw, []byte("workflows:\n  root: "+root+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath, root
}

func writeScriptFixture(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "check.local.sh")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAssuranceValidator_AddListScriptRemoveRoundTrip(t *testing.T) {
	configPath, root := writeValidatorTestConfig(t)
	script := writeScriptFixture(t, goodCLIValidator)

	var code int
	out := captureStdout(t, func() {
		code = run([]string{"assurance", "validators", "add", "manifest", "--pattern", `^MANIFEST-\d{8}T\d{6}Z\.txt$`,
			"--backup-set", cliSet, "--script", script, "--json", "--config", configPath})
	})
	if code != exitOK {
		t.Fatalf("add exit %d:\n%s", code, out)
	}
	var created apicontract.CustomValidator
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("add --json is not the API's body: %v\n%s", err, out)
	}
	if created.Name != "manifest" || created.BackupSetID != cliSet || !created.ScriptPresent || created.Scope != "backup_set" {
		t.Errorf("created = %+v", created)
	}
	if _, err := os.Stat(filepath.Join(root, "recovery-test-validators", "manifest.local.sh")); err != nil {
		t.Errorf("the script is not in the workflow root: %v", err)
	}

	out = captureStdout(t, func() { code = run([]string{"assurance", "validators", "list", "--config", configPath}) })
	if code != exitOK || !strings.Contains(out, "manifest  "+cliSet+`  ^MANIFEST-\d{8}T\d{6}Z\.txt$`) || !strings.Contains(out, "script manifest.local.sh") {
		t.Errorf("list exit %d:\n%s", code, out)
	}
	out = captureStdout(t, func() { code = run([]string{"assurance", "validators", "list", "--json", "--config", configPath}) })
	var l apicontract.ListCustomValidatorsResponse
	if err := json.Unmarshal([]byte(out), &l); err != nil || len(l.Validators) != 1 || l.Directory != "recovery-test-validators" {
		t.Errorf("list --json = %+v, %v\n%s", l, err, out)
	}

	out = captureStdout(t, func() { code = run([]string{"assurance", "validators", "script", "manifest", "--config", configPath}) })
	if code != exitOK || out != goodCLIValidator {
		t.Errorf("script exit %d printed %q", code, out)
	}

	if code := run([]string{"assurance", "validators", "remove", "manifest", "--config", configPath}); code != exitOK {
		t.Fatalf("remove exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "recovery-test-validators", "manifest.local.sh")); !os.IsNotExist(err) {
		t.Errorf("the script survived its removal: %v", err)
	}
	raw, _ := os.ReadFile(configPath)
	if strings.Contains(string(raw), "name: manifest") {
		t.Errorf("config.yaml still names the validator:\n%s", raw)
	}
}

const goodCLIValidator = "#!/usr/bin/env bash\nset -euo pipefail\ntest -s \"$RECOVERY_TEST_FILE\"\n"

func TestAssuranceValidator_UpdateKeepsTheScriptWithoutScript(t *testing.T) {
	configPath, root := writeValidatorTestConfig(t)
	script := writeScriptFixture(t, goodCLIValidator)
	if code := run([]string{"assurance", "validators", "add", "v", "--pattern", "^a", "--script", script, "--config", configPath}); code != exitOK {
		t.Fatalf("add exit %d", code)
	}
	if code := run([]string{"assurance", "validators", "update", "v", "--pattern", "^b", "--backup-set", cliSet, "--config", configPath}); code != exitOK {
		t.Fatalf("update exit %d", code)
	}
	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "pattern: ^b") || !strings.Contains(string(raw), "backup_set: "+cliSet) {
		t.Errorf("update did not write:\n%s", raw)
	}
	body, _ := os.ReadFile(filepath.Join(root, "recovery-test-validators", "v.local.sh"))
	if string(body) != goodCLIValidator {
		t.Error("an update without --script changed the script")
	}
}

func TestAssuranceValidator_DryRunJudgesWithRE2AndSavesNothing(t *testing.T) {
	configPath, _ := writeValidatorTestConfig(t)
	before, _ := os.ReadFile(configPath)
	var code int
	out := captureStdout(t, func() {
		code = run([]string{"assurance", "validators", "add", "--pattern", `^MANIFEST-\d+`, "--dry-run", "--json", "--config", configPath})
	})
	var p apicontract.CustomValidatorPreview
	if code != exitOK || json.Unmarshal([]byte(out), &p) != nil || !p.Valid {
		t.Errorf("dry run exit %d: %s", code, out)
	}
	out = captureStdout(t, func() {
		code = run([]string{"assurance", "validators", "add", "--pattern", `(?=x)`, "--dry-run", "--config", configPath})
	})
	if code != exitFailure || !strings.Contains(out, "RE2") {
		t.Errorf("a lookahead dry run exit %d:\n%s", code, out)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Error("a dry run wrote the configuration")
	}
}

func TestAssuranceValidator_DefaultScriptPrintsAndWritesANewFileOnly(t *testing.T) {
	var code int
	out := captureStdout(t, func() { code = run([]string{"assurance", "validators", "default-script"}) })
	if code != exitOK || out != service.DefaultCustomValidatorScript().Content {
		t.Fatalf("default-script exit %d", code)
	}
	target := filepath.Join(t.TempDir(), "mine.local.sh")
	if code := run([]string{"assurance", "validators", "default-script", "--output", target}); code != exitOK {
		t.Fatalf("--output exit %d", code)
	}
	got, _ := os.ReadFile(target)
	if string(got) != service.DefaultCustomValidatorScript().Content {
		t.Error("--output wrote something else")
	}
	if code := run([]string{"assurance", "validators", "default-script", "--output", target}); code == exitOK {
		t.Error("--output replaced a file that already existed")
	}
}

func TestAssuranceValidator_PatternsSuggestFromTheJournal(t *testing.T) {
	configPath, _ := writeValidatorTestConfig(t)
	var code int
	out := captureStdout(t, func() { code = run([]string{"assurance", "validators", "patterns", "--json", "--config", configPath}) })
	var p apicontract.CustomValidatorPatternsResponse
	if code != exitOK || json.Unmarshal([]byte(out), &p) != nil || p.Patterns == nil {
		t.Errorf("patterns exit %d: %s", code, out)
	}
}

func TestAssuranceValidator_UsageRefusals(t *testing.T) {
	configPath, _ := writeValidatorTestConfig(t)
	script := writeScriptFixture(t, goodCLIValidator)
	for _, argv := range [][]string{
		{"assurance", "validators"},
		{"assurance", "validators", "frobnicate"},
		{"assurance", "validators", "add", "v", "--script", script},
		{"assurance", "validators", "add", "v", "--pattern", "x"},
		{"assurance", "validators", "add", "--pattern", "x", "--script", script},
		{"assurance", "validators", "add", "--pattern", "x", "--dry-run", "--script", script},
		{"assurance", "validators", "update", "v"},
		{"assurance", "validators", "remove"},
		{"assurance", "validators", "list", "extra"},
		{"assurance", "validators", "default-script", "extra"},
	} {
		if code := run(append(argv, "--config", configPath)); code != exitUsage {
			t.Errorf("%v exit %d, want usage %d", argv, code, exitUsage)
		}
	}
}

func TestAssuranceValidator_ARefusedUploadExitsWithTheOrdinaryFailureAndWritesNothing(t *testing.T) {
	configPath, root := writeValidatorTestConfig(t)
	bad := writeScriptFixture(t, "#!/usr/bin/env bash\nrm -rf /$UNSET_DIR\n")
	before, _ := os.ReadFile(configPath)
	if code := run([]string{"assurance", "validators", "add", "v", "--pattern", "x", "--script", bad, "--config", configPath}); code != exitFailure {
		t.Errorf("a lint refusal exit %d, want %d", code, exitFailure)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Error("a refused upload changed config.yaml")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "recovery-test-validators")); len(entries) != 0 {
		t.Errorf("a refused upload left %d files", len(entries))
	}
}
