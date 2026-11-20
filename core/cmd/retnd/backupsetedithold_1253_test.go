package main

import (
	"strings"
	"testing"
)

// Issue #1253: `backup-set edit-hold <id> --take` takes the
// hold the Web UI takes when its edit form opens, so a scripted
// multi-step edit can pause the set the same way, and give it back with
// --release when it is done.

func TestBackupSetEditHold1253_TakeHoldsTheSetOnTheEngine(t *testing.T) {
	cliConfig, e := oneServedDeployment(t)

	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"backup-set", "edit-hold", "production/postgres-primary", "--take", "--config", cliConfig})
	})
	if code != 0 {
		t.Fatalf("backup-set edit-hold --take = %d, want 0; it printed %q", code, stdout)
	}
	if !strings.Contains(stdout, "held: true") {
		t.Errorf("the report after a take does not say the set is held:\n%s", stdout)
	}
	if !strings.Contains(stdout, "expires_at:") {
		t.Errorf("the report does not say when the lease lapses, which is what a script renewing it needs:\n%s", stdout)
	}
	if !strings.Contains(stdout, "stopped: nothing") {
		t.Errorf("a take that interrupted nothing must say so rather than claim it stopped something:\n%s", stdout)
	}

	state, err := e.svc.BackupSetEditState(t.Context(), "production/postgres-primary")
	if err != nil {
		t.Fatalf("reading the hold back off the engine: %v", err)
	}
	if !state.Held {
		t.Error("the engine is not holding this backup set, so the take reached nothing")
	}
}

// A second --take with no token on a set somebody already holds is a
// second holder since #1606, not a renewal of the first: the set stays
// held either way. Renewing one holder is --take --token
// (backupsetedithold_1606_test.go).
func TestBackupSetEditHold1253_TakeOnAHeldSetHoldsItToo(t *testing.T) {
	cliConfig, e := oneServedDeployment(t)
	if _, err := e.svc.BeginBackupSetEdit(t.Context(), "production/postgres-primary"); err != nil {
		t.Fatalf("taking the hold on the engine: %v", err)
	}

	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"backup-set", "edit-hold", "production/postgres-primary", "--take", "--config", cliConfig})
	})
	if code != 0 {
		t.Fatalf("a take on a set already held = %d, want 0 (it is a second holder); it printed %q", code, stdout)
	}
	if !strings.Contains(stdout, "held: true") {
		t.Errorf("the set is not reported held after a second take:\n%s", stdout)
	}
}

func TestBackupSetEditHold1253_TakeAndReleaseTogetherIsAUsageError(t *testing.T) {
	cliConfig, _ := oneServedDeployment(t)
	if got := run([]string{"backup-set", "edit-hold", "production/postgres-primary", "--take", "--release", "--config", cliConfig}); got != 2 {
		t.Errorf("--take with --release = %d, want 2: they are opposites, and picking one silently would do the other half of what was asked", got)
	}
}

func TestBackupSetEditHold1253_TakeRefusesWithNoRoute(t *testing.T) {
	configPath := writeTestConfig(t)
	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"backup-set", "edit-hold", "production/postgres-primary", "--take", "--config", configPath})
	})
	if code == 0 {
		t.Fatalf("--take with no engine to reach exited 0, so it held something somewhere: %q", stdout)
	}
}

func TestBackupSetEditHold1253_UsageSaysTheVerbTakesAHold(t *testing.T) {
	text := captureStderr(t, usage)
	if !strings.Contains(text, "backup-set edit-hold <source/backup-set> [--take | --release]") {
		t.Errorf("usage() does not list --take on `backup-set edit-hold`")
	}
	if strings.Contains(text, "There is no verb that TAKES") {
		t.Errorf("usage() still says no verb takes a hold")
	}
}
