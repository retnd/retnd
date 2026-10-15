package main

import (
	"os"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// #1152, backup-set test-connection and medium test-connection.
//
// Both are checks whose verdict is the exit code (1 when the connection does
// not work) and whose report is the result, so --json prints the report in
// both cases and keeps the exit code. Both open the deployment through the
// write route, which announces its mode and logs its startup on stdout, so a
// clean stdout is part of what is held here.

func TestBackupSetTestConnectionJSON_IsTheTestConnectionResponse_1152(t *testing.T) {
	configPath := writeTestConfig(t)

	out, code := cliJSON(t, "backup-set", "--config", configPath, "test-connection", "--json", cliSet)
	if code != exitOK {
		t.Fatalf("code = %d:\n%s", code, out)
	}
	var got apicontract.TestConnectionResponse
	strictDecode(t, out, &got)
	if !got.OK {
		t.Errorf("ok = false for a source this process can read: %+v", got)
	}
	steps := map[string]string{}
	for _, c := range got.Checks {
		steps[c.Step] = c.Outcome
	}
	for _, step := range []string{"credentials", "resolve", "connect", "host_key", "authenticate", "list"} {
		if _, ok := steps[step]; !ok {
			t.Errorf("the report has no %q step: %+v", step, got.Checks)
		}
	}
	requireKeys(t, rawKeys(t, out), "ok", "writable", "checks")
}

func TestBackupSetTestConnectionJSON_AFailedCheckStillPrintsTheReportAndExitsOne_1152(t *testing.T) {
	configPath := writeTestConfig(t)
	raw := readFile(t, configPath)
	// Point the source at a directory that is not there.
	broken := strings.Replace(raw, "        remote_path: ", "        remote_path: /nonexistent-1152", 1)
	if broken == raw {
		t.Fatal("the fixture changed shape")
	}
	if err := os.WriteFile(configPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := cliJSON(t, "backup-set", "--config", configPath, "test-connection", "--json", cliSet)
	var got apicontract.TestConnectionResponse
	strictDecode(t, out, &got)
	if got.OK || code != exitFailure {
		t.Errorf("ok=%v exit=%d, want a failed report and 1:\n%s", got.OK, code, out)
	}
	failed := false
	for _, c := range got.Checks {
		if c.Outcome == "failed" {
			failed = true
		}
	}
	if !failed {
		t.Errorf("no check is marked failed in a failed report: %+v", got.Checks)
	}
}

func TestBackupSetCreateAndPatchRefuseJSON_1152(t *testing.T) {
	configPath := writeTestConfig(t)
	before := readFile(t, configPath)
	for _, argv := range [][]string{
		{"backup-set", "--config", configPath, "patch", "--json", "--stale-after", "48h", cliSet},
		{"backup-set", "--config", configPath, "create", "--json", "production/other"},
		{"backup-set", "--config", configPath, "remove", "--json", cliSet},
	} {
		var code int
		stderr := captureStderr(t, func() { code = run(argv) })
		if code != exitUsage || !strings.Contains(stderr, "--json") {
			t.Errorf("%v: exit=%d, want a usage error naming --json; first line of stderr: %q", argv, code, strings.SplitN(stderr, "\n", 2)[0])
		}
	}
	if readFile(t, configPath) != before {
		t.Errorf("a refused command changed the configuration")
	}
}

func TestMediumTestConnectionJSON_IsTheMediumPreflightResponse_1152(t *testing.T) {
	configPath := writeOffsiteTestConfig(t)
	if err := os.MkdirAll(localBackupRootOf(t, configPath), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, verb := range []string{"test-connection", "preflight"} {
		out, code := cliJSON(t, "medium", verb, "--json", "--config", configPath, "local")
		if code != exitOK {
			t.Fatalf("medium %s --json local = %d:\n%s", verb, code, out)
		}
		var got apicontract.MediumPreflightResponse
		strictDecode(t, out, &got)
		if got.Medium != "local" || !got.OK || len(got.Checks) == 0 {
			t.Errorf("medium %s: %+v", verb, got)
		}
		requireKeys(t, rawKeys(t, out), "medium", "ok", "checks")
	}

	// A destination that cannot be reached: the report is the result, and
	// the exit code still says so.
	out, code := cliJSON(t, "medium", "test-connection", "--json", "--config", configPath, "cold_offsite")
	var failed apicontract.MediumPreflightResponse
	strictDecode(t, out, &failed)
	if failed.OK || code != exitFailure || failed.Medium != "cold_offsite" {
		t.Errorf("ok=%v exit=%d medium=%q, want a failed report and 1", failed.OK, code, failed.Medium)
	}
}

func TestMediumTestConnectionJSON_AnUndeclaredMediumPrintsNothingAndFails_1152(t *testing.T) {
	configPath := writeOffsiteTestConfig(t)
	out, code := cliJSON(t, "medium", "test-connection", "--json", "--config", configPath, "nope")
	if code != exitFailure {
		t.Errorf("exit = %d, want 1", code)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a refused check wrote to stdout:\n%s", out)
	}
}
