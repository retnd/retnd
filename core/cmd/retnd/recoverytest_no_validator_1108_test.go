package main

import (
	"strings"
	"testing"
)

// #1108 through the terminal's door. The browser and `retnd recovery-test`
// read the same engine lines, so the wording defect shows on stderr too, and
// the fix has to land in the engine and not in a UI-only string.
//
// The committed fixture declares no recovery-test validator.
func TestIssue1108_RecoveryTestFollowOnASetWithNoValidatorNeverPrintsCompleteAndStillRefuses(t *testing.T) {
	cfg, _ := committedArtifact(t)
	set := strings.TrimSuffix(recoverID, "/backup.dump")
	var code int
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() { code = run([]string{"recovery-test", "--config", cfg, set, "--follow"}) })
	})

	// The refusal stays: exit 1, state not_configured, and the reason in words.
	if code != 1 {
		t.Errorf("exit = %d, want 1 (the refusal stays); stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "not_configured") || !strings.Contains(stdout, "no recovery-test validator is declared") {
		t.Errorf("stdout %q does not carry the refusal", stdout)
	}

	for _, s := range stagesPrinted(stderr) {
		if s == "complete" {
			t.Errorf("--follow printed the complete stage for a run that tested nothing:\n%s", stderr)
		}
	}
	if strings.Contains(stderr, "recovery test complete") {
		t.Errorf("the closing line says the test is complete above state=not_configured:\n%s", stderr)
	}
	if !strings.Contains(stderr, "recovery test not run") {
		t.Errorf("the closing line does not say the test was not run:\n%s", stderr)
	}
}
