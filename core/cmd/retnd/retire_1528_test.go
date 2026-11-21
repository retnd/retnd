package main

import (
	"strings"
	"testing"
)

// Issue #1528's verb. FAILED had one way out (retry), and for a backup whose
// source is gone that way only leads back into FAILED. `retnd retire` is the
// second way out, and like retry it has to be findable from the command line
// by the operator who is already looking at something stuck.

func TestRetireIsDiscoverableFromTheCommandLine_1528(t *testing.T) {
	run, ok := commands["retire"]
	if !ok {
		t.Fatal("there is no retire command, so a FAILED backup whose source is gone has no way out from the terminal")
	}
	out := captureStderr(t, usage)
	if !strings.Contains(out, "retire <source/backup-set/artifact>") {
		t.Error("usage() does not list the retire verb, so an operator cannot discover it")
	}

	// Asked for wrongly, it refuses before opening anything (exit 2 is the
	// usage code; a refusal that leaked past argument handling would fail
	// opening the state database instead).
	var code int
	stderr := captureStderr(t, func() { code = run([]string{"--config", "/nonexistent/no-such-config.yaml"}) })
	if code != 2 || !strings.Contains(stderr, "expected <source/backup-set/artifact>") {
		t.Errorf("retire with no artifact: exit %d, stderr %q; want exit 2 naming the operand", code, stderr)
	}
}
