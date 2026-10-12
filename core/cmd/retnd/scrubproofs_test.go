package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EPIC Q panel 2 (W-07) on #978: `retnd scrub --json` speaks the contract's
// scrub result, and a CLI scrub beside a serving engine honours the edit hold
// that engine is keeping (D7).

const scrubSet = "production/postgres-primary"

// oneCommittedArtifact runs a real cycle so the journal holds one durable
// local copy, and returns the config path and the file that copy lives in.
func oneCommittedArtifact(t *testing.T) (configPath, localFinal string) {
	t.Helper()
	configPath = writeTestConfig(t)
	if got := run([]string{"run", "--config", configPath}); got != 0 {
		t.Fatalf("run = %d, want 0 (this test needs one committed artifact)", got)
	}
	return configPath, filepath.Join(filepath.Dir(configPath), "local", "backup.dump")
}

func TestScrubJSON_IsOneContractObjectWithTheCountsAndTheCopyDetail(t *testing.T) {
	configPath, localFinal := oneCommittedArtifact(t)
	if err := os.WriteFile(localFinal, []byte("tampered bytes that do not match the recorded hash"), 0o644); err != nil {
		t.Fatal(err)
	}

	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"scrub", "--json", "--config", configPath, scrubSet})
	})
	if code != 1 {
		t.Fatalf("scrub --json of a rotted copy = %d, want 1 (a copy failed); stdout %q", code, stdout)
	}

	dec := json.NewDecoder(bytes.NewReader([]byte(stdout)))
	var got map[string]any
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not one JSON value: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Errorf("stdout carries more than one JSON value:\n%s", stdout)
	}
	for _, k := range []string{"trigger", "scope", "selected", "checked", "passed", "failed", "could_not_complete", "requires_restore", "skipped_held", "skipped_leased", "refused", "bytes_read", "sets"} {
		if _, ok := got[k]; !ok {
			t.Errorf("the JSON has no %q: %v", k, got)
		}
	}
	if got["failed"] != float64(1) {
		t.Errorf("failed = %v, want 1", got["failed"])
	}
	if strings.Contains(stdout, "scrub (") {
		t.Errorf("the human summary line leaked into --json output:\n%s", stdout)
	}
}

func TestScrub_UnderAServingEnginesEditHoldIsSkippedVisiblyAndTouchesNothing(t *testing.T) {
	configPath, localFinal := oneCommittedArtifact(t)
	if err := os.WriteFile(localFinal, []byte("tampered bytes that do not match the recorded hash"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := startFakeEngine(t, configPath)
	e.attach(t)
	if _, err := e.svc.BeginBackupSetEdit(t.Context(), scrubSet); err != nil {
		t.Fatalf("taking the hold on the engine: %v", err)
	}

	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"scrub", "--config", configPath, scrubSet})
	})
	if code != 1 {
		t.Errorf("scrub of a held set = %d, want 1: a skip is work that did not happen; stdout %q", code, stdout)
	}
	if !strings.Contains(stdout, "skipped, the backup set is held for editing") {
		t.Errorf("the skip is not visible, in the engine's own wording:\n%s", stdout)
	}
	if !strings.Contains(stdout, "1 skipped") {
		t.Errorf("the summary does not count the skip:\n%s", stdout)
	}
	if strings.Contains(stdout, "FAILED") {
		t.Errorf("the scrub read a copy in a set the engine holds for editing:\n%s", stdout)
	}
}

func TestScrub_WithNoHoldTheSameCommandChecksTheCopy(t *testing.T) {
	// The control for the case above: attached to the same engine with no
	// hold in force, the scrub runs, so the skip is the hold's doing and not
	// the attachment's.
	configPath, localFinal := oneCommittedArtifact(t)
	if err := os.WriteFile(localFinal, []byte("tampered bytes that do not match the recorded hash"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := startFakeEngine(t, configPath)
	e.attach(t)

	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"scrub", "--config", configPath, scrubSet})
	})
	if code != 1 || !strings.Contains(stdout, "FAILED") {
		t.Errorf("an unheld set was not scrubbed: code %d\n%s", code, stdout)
	}
}
