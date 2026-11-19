package main

import (
	"strings"
	"testing"
)

// #1317: `retnd encryption reencrypt --suite ID | --key ID | --deprecated`.
// These are the argument rules and the refusals that need no deployment to
// hold an encrypted copy. The grouping by suite and key is the service's
// (TestReencryptDryRunGroupsBySuiteAndKey_1317) and its route twin's; the
// byte path is the engine's and the crash cells'. Written by reading the verb
// dispatch and the existing encryption verbs; not run.

func TestReencryptArgumentRules_1317(t *testing.T) {
	configPath := writeTestConfig(t)
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"an operand", []string{"encryption", "reencrypt", "a/b", "--suite", "age/v1", "--to", "offsite", "--config", configPath}, exitUsage},
		{"a run with no destination", []string{"encryption", "reencrypt", "--suite", "age/v1", "--config", configPath}, exitUsage},
		{"no selector on the dry run", []string{"encryption", "reencrypt", "--dry-run", "--config", configPath}, exitFailure},
		{"two selectors on the dry run", []string{"encryption", "reencrypt", "--suite", "age/v1", "--deprecated", "--dry-run", "--config", configPath}, exitFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			stderr := captureStderr(t, func() { code = captureStdoutCode(t, func() int { return run(tc.args) }) })
			if code != tc.want {
				t.Errorf("exit %d, want %d\n%s", code, tc.want, stderr)
			}
		})
	}
}

// A suite this build does not know is refused by name, on the dry run, with
// the failure status. Red if it is read as "no copies" and exits 0.
func TestReencryptRefusesASuiteThisBuildDoesNotKnow_1317(t *testing.T) {
	configPath := writeTestConfig(t)
	args := []string{"encryption", "reencrypt", "--suite", "nosuch/v9", "--dry-run", "--config", configPath}
	var code int
	stderr := captureStderr(t, func() { code = captureStdoutCode(t, func() int { return run(args) }) })
	if code != exitFailure {
		t.Errorf("exit %d, want %d\n%s", code, exitFailure, stderr)
	}
	if !strings.Contains(stderr, "nosuch/v9") {
		t.Errorf("the refusal does not name the suite: %s", stderr)
	}
}

// The verb is listed where the other encryption verbs are.
func TestReencryptIsInTheUsageText_1317(t *testing.T) {
	out := captureStderr(t, func() { _ = run(nil) })
	if !strings.Contains(out, "encryption reencrypt") {
		t.Errorf("the usage text does not mention `encryption reencrypt`")
	}
}
