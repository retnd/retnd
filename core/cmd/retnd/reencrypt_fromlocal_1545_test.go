package main

import (
	"strings"
	"testing"
)

// #1545: `retnd encryption reencrypt ... --from-local`. The byte path is the
// engine's and the service's; these are the argument rules and the help text.

// The flag exists on the dry run and the run, and a run still needs --to.
// Red while the flag set does not know --from-local (exit 2 from the parser).
func TestReencryptKnowsFromLocal_1545(t *testing.T) {
	configPath := writeTestConfig(t)
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"a dry run", []string{"encryption", "reencrypt", "--key", "k1", "--from-local", "--dry-run", "--config", configPath}, exitFailure},
		{"a run with no destination", []string{"encryption", "reencrypt", "--key", "k1", "--from-local", "--config", configPath}, exitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			stderr := captureStderr(t, func() { code = captureStdoutCode(t, func() int { return run(tc.args) }) })
			if code != tc.want {
				t.Errorf("exit %d, want %d\n%s", code, tc.want, stderr)
			}
			if strings.Contains(stderr, "flag provided but not defined") {
				t.Errorf("the flag is not defined: %s", stderr)
			}
		})
	}
}

func TestReencryptUsageNamesFromLocal_1545(t *testing.T) {
	out := captureStderr(t, func() { _ = run([]string{"encryption"}) })
	if !strings.Contains(out, "--from-local") {
		t.Errorf("the encryption usage text does not mention --from-local:\n%s", out)
	}
}
