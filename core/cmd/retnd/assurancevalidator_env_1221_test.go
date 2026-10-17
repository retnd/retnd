package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1221: `retnd assurance validators add|update --env minimal|workflow`.

func addValidatorArgs(configPath, script string, extra ...string) []string {
	return append([]string{"assurance", "validators", "add", "v", "--pattern", "^a", "--script", script, "--config", configPath}, extra...)
}

func TestIssue1221_AddTakesEnvAndTheListShowsIt(t *testing.T) {
	configPath, _ := writeValidatorTestConfig(t)
	script := writeScriptFixture(t, goodCLIValidator)

	var code int
	out := captureStdout(t, func() { code = run(addValidatorArgs(configPath, script, "--env", "workflow", "--json")) })
	if code != exitOK {
		t.Fatalf("add --env workflow exit %d:\n%s", code, out)
	}
	var created apicontract.CustomValidator
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.Env != "workflow" {
		t.Fatalf("add --json = %+v, %v", created, err)
	}
	if raw, _ := os.ReadFile(configPath); !strings.Contains(string(raw), "env: workflow") {
		t.Errorf("config.yaml does not carry the opt-in:\n%s", raw)
	}
	out = captureStdout(t, func() { code = run([]string{"assurance", "validators", "list", "--config", configPath}) })
	if code != exitOK || !strings.Contains(out, "env workflow") {
		t.Errorf("list (exit %d) does not say the validator gets the workflow environment:\n%s", code, out)
	}
	out = captureStdout(t, func() { code = run([]string{"assurance", "validators", "list", "--json", "--config", configPath}) })
	var l apicontract.ListCustomValidatorsResponse
	if err := json.Unmarshal([]byte(out), &l); err != nil || len(l.Validators) != 1 || l.Validators[0].Env != "workflow" {
		t.Errorf("list --json = %+v, %v", l.Validators, err)
	}
}

func TestIssue1221_AddWithoutEnvIsMinimal(t *testing.T) {
	configPath, _ := writeValidatorTestConfig(t)
	script := writeScriptFixture(t, goodCLIValidator)
	var code int
	out := captureStdout(t, func() { code = run(addValidatorArgs(configPath, script, "--json")) })
	var created apicontract.CustomValidator
	if err := json.Unmarshal([]byte(out), &created); code != exitOK || err != nil || created.Env != "minimal" {
		t.Errorf("add without --env: exit %d, env %q, %v\n%s", code, created.Env, err, out)
	}
	out = captureStdout(t, func() { code = run([]string{"assurance", "validators", "list", "--config", configPath}) })
	if !strings.Contains(out, "env minimal") {
		t.Errorf("list does not say what environment the validator gets:\n%s", out)
	}
}

func TestIssue1221_UpdateIsAReplacementSoWithoutEnvItIsMinimalAgain(t *testing.T) {
	configPath, _ := writeValidatorTestConfig(t)
	script := writeScriptFixture(t, goodCLIValidator)
	if code := run(addValidatorArgs(configPath, script, "--env", "workflow")); code != exitOK {
		t.Fatalf("add exit %d", code)
	}
	if code := run([]string{"assurance", "validators", "update", "v", "--pattern", "^a", "--global", "--config", configPath}); code != exitOK {
		t.Fatalf("update exit %d", code)
	}
	raw, _ := os.ReadFile(configPath)
	if strings.Contains(string(raw), "env: workflow") || !strings.Contains(string(raw), "env: minimal") {
		t.Errorf("an update without --env must not keep the opt-in:\n%s", raw)
	}
	if code := run([]string{"assurance", "validators", "update", "v", "--pattern", "^a", "--global", "--env", "workflow", "--config", configPath}); code != exitOK {
		t.Fatalf("update --env workflow exit %d", code)
	}
	if raw, _ = os.ReadFile(configPath); !strings.Contains(string(raw), "env: workflow") {
		t.Errorf("update --env workflow did not write it:\n%s", raw)
	}
}

func TestIssue1221_AnEnvThatIsNotExactlyOneOfTheTwoIsAUsageErrorAndWritesNothing(t *testing.T) {
	configPath, _ := writeValidatorTestConfig(t)
	script := writeScriptFixture(t, goodCLIValidator)
	before, _ := os.ReadFile(configPath)
	for _, bad := range []string{"Workflow", "WORKFLOW", "all", "secrets", "true", " workflow"} {
		var code int
		stderr := captureStderr(t, func() { code = run(addValidatorArgs(configPath, script, "--env", bad)) })
		if code != exitUsage || !strings.Contains(stderr, "--env") {
			t.Errorf("add --env %q: exit %d, stderr %q, want a usage error naming --env", bad, code, stderr)
		}
	}
	if after, _ := os.ReadFile(configPath); string(after) != string(before) {
		t.Errorf("a refused --env still changed config.yaml:\n%s", after)
	}
	if code := run(addValidatorArgs(configPath, script)); code != exitOK {
		t.Fatalf("plain add exit %d", code)
	}
	var code int
	stderr := captureStderr(t, func() {
		code = run([]string{"assurance", "validators", "update", "v", "--pattern", "^a", "--global", "--env", "Workflow", "--config", configPath})
	})
	if code != exitUsage || !strings.Contains(stderr, "--env") {
		t.Errorf("update --env Workflow: exit %d, stderr %q", code, stderr)
	}
}

func TestIssue1221_RoutedWritesCarryTheEnvToTheEngine(t *testing.T) {
	cliConfig, engine := routedValidatorFixture(t)
	script := writeScriptFixture(t, goodCLIValidator)
	run1 := func(argv ...string) int {
		return captureStdoutCode(t, func() int { return run(withConfig(argv, cliConfig)) })
	}
	var listCode int
	if code := run1("assurance", "validators", "add", "routed", "--pattern", "^R-", "--script", script, "--env", "workflow"); code != exitOK {
		t.Fatalf("routed add exit %d", code)
	}
	got, err := engine.svc.ListCustomValidators(t.Context(), service.CustomValidatorsFilter{})
	if err != nil || len(got.Validators) != 1 || got.Validators[0].Env != "workflow" {
		t.Fatalf("the engine holds %+v, %v", got.Validators, err)
	}
	if code := run1("assurance", "validators", "update", "routed", "--pattern", "^R-", "--global"); code != exitOK {
		t.Fatalf("routed update exit %d", code)
	}
	got, _ = engine.svc.ListCustomValidators(t.Context(), service.CustomValidatorsFilter{})
	if len(got.Validators) != 1 || got.Validators[0].Env != "minimal" {
		t.Errorf("a routed update without --env left %+v, want minimal", got.Validators)
	}
	out := captureStdout(t, func() { listCode = run(withConfig([]string{"assurance", "validators", "list", "--json"}, cliConfig)) })
	var l apicontract.ListCustomValidatorsResponse
	if err := json.Unmarshal([]byte(out), &l); listCode != exitOK || err != nil || len(l.Validators) != 1 || l.Validators[0].Env != "minimal" {
		t.Errorf("the routed list does not carry env: %+v, %v\n%s", l.Validators, err, out)
	}
}
