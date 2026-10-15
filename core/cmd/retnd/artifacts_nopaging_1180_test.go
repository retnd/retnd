package main

import (
	"strings"
	"testing"
)

// TestArtifacts_PrintsTheWholeList_1180 pins the review's call on #1180: the
// Backups page pages in the browser, and the terminal prints every artifact.
// GET /backups has no paging, so a CLI --limit/--offset would only slice a
// full read and pin an offset style the API does not have; when real paging
// arrives it will be cursor-based, like `activity --before`.
func TestArtifacts_PrintsTheWholeList_1180(t *testing.T) {
	configPath := scrubbedRottedCopy(t)

	for _, flag := range []string{"--limit", "--offset"} {
		var code int
		stderr := captureStderr(t, func() { code = run([]string{"artifacts", "--config", configPath, flag, "20"}) })
		if code != exitUsage {
			t.Errorf("artifacts %s 20 = %d, want %d (not a flag of artifacts)", flag, code, exitUsage)
		}
		if !strings.Contains(stderr, "flag provided but not defined") {
			t.Errorf("artifacts %s 20 should be refused as an unknown flag, stderr:\n%s", flag, stderr)
		}
	}

	if out := artifactsOut(t, 0, "artifacts", "--config", configPath); strings.Contains(out, "showing ") {
		t.Errorf("the list grew a paging line:\n%s", out)
	}

	text := captureStderr(t, usage)
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "artifacts ") && (strings.Contains(line, "--limit") || strings.Contains(line, "--offset")) {
			t.Errorf("usage() still offers paging on artifacts: %q", line)
		}
	}
}
