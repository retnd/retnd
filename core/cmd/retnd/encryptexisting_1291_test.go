package main

import (
	"strings"
	"testing"
)

// #1291: `retnd encryption encrypt-existing <source/set> --to <medium>`. These
// are the argument rules and the refusals that need no deployment to hold a
// plaintext copy. The end-to-end behaviour is the engine's and the crash
// cells'. Written by reading the verb dispatch and the existing encryption
// verbs; not run.

func TestEncryptExistingNeedsASetAndADestination_1291(t *testing.T) {
	configPath := writeTestConfig(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no operand", []string{"encryption", "encrypt-existing", "--to", "offsite", "--config", configPath}},
		{"an operand that is not source/set", []string{"encryption", "encrypt-existing", "justaname", "--to", "offsite", "--config", configPath}},
		{"two operands", []string{"encryption", "encrypt-existing", "a/b", "c/d", "--to", "offsite", "--config", configPath}},
		{"no destination", []string{"encryption", "encrypt-existing", "a/b", "--config", configPath}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			stderr := captureStderr(t, func() { code = run(tc.args) })
			if code != exitUsage {
				t.Errorf("exit %d, want %d (usage)\n%s", code, exitUsage, stderr)
			}
		})
	}
}

// A set the deployment does not have is a refusal with the failure status and
// a message naming the set, on the dry run and on the real run alike. Red if
// either one starts planning, or exits with the usage or success status.
func TestEncryptExistingRefusesASetThisDeploymentDoesNotHave_1291(t *testing.T) {
	configPath := writeTestConfig(t)
	for _, extra := range [][]string{nil, {"--dry-run"}} {
		args := append([]string{"encryption", "encrypt-existing", "nosuch/set", "--to", "offsite", "--config", configPath}, extra...)
		var code int
		stderr := captureStderr(t, func() { code = captureStdoutCode(t, func() int { return run(args) }) })
		if code != exitFailure {
			t.Errorf("%v: exit %d, want %d\n%s", extra, code, exitFailure, stderr)
		}
		if !strings.Contains(stderr, "nosuch/set") {
			t.Errorf("%v: the refusal does not name the set: %s", extra, stderr)
		}
	}
}

// The verb is listed where the other encryption verbs are, so an operator
// finds it from the usage text and the site reference lists exactly the
// commands the binary registers.
func TestEncryptExistingIsInTheUsageText_1291(t *testing.T) {
	out := captureStderr(t, func() { _ = run(nil) })
	if !strings.Contains(out, "encryption encrypt-existing") {
		t.Errorf("the usage text does not mention `encryption encrypt-existing`")
	}
}
