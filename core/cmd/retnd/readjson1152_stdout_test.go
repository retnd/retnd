package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #1152. A --json verb's stdout is data. When the deployment will not open,
// the structured error line the service logs belongs on stderr, because a
// script that pipes stdout into a JSON parser must see nothing there rather
// than one log record it cannot tell from a result.

func TestReadJSONVerbs_ABrokenConfigurationWritesNothingToStdout_1152(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	// A poll interval of zero is refused by validation, after the service has
	// started logging, which is the path that used to write to stdout.
	body := "state:\n  database: " + filepath.Join(dir, "state.db") + "\nsources: []\n"
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, argv := range [][]string{
		{"snapshot", "list", incSet},
		{"snapshot", "show", incSet, "run_a"},
		{"snapshot", "holds", incSet},
		{"snapshot", "retention", incSet},
		{"repository", "health"},
		{"repository", "maintenance", "production"},
		{"settings"},
		{"backup-set", "retention", cliSet},
		{"retention", cliSet},
		{"catalog", "rebuild"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			args := append(append([]string{}, argv...), "--json", "--config", configPath)
			out, code := cliJSON(t, args...)
			if code != exitFailure {
				t.Fatalf("a broken configuration exited %d, want %d", code, exitFailure)
			}
			if strings.TrimSpace(out) != "" {
				t.Errorf("stdout is not empty:\n%s", out)
			}
		})
	}
}
