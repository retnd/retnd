package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// Panel finding C7 on retnd#1050: `status` is the image's baked-in
// HEALTHCHECK, and `check` is what an operator runs after a restore. A
// configuration that went missing next to a journal full of backups printed
// "first run, fresh install" and exited 0 from both.

func seedHistory(t *testing.T, dbPath string) {
	t.Helper()
	ctx := context.Background()
	j, err := state.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer j.Close() //nolint:errcheck // test teardown, nothing reads the journal after this
	set, err := model.NewBackupSetID("production", "postgres-primary")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := model.NewArtifactID(set, "old.dump")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Discover(ctx, artifact, "seed-1", "/backups/old.dump", state.RemoteIdentity{}, time.Now()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
}

func runWithLostConfig(t *testing.T, verb string) (code int, stdout, stderr string) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "state.db")
	seedHistory(t, db)
	t.Setenv("STATE_DATABASE", db)
	configDir := t.TempDir()
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() { code = run([]string{verb, "--config", configDir}) })
	})
	return code, stdout, stderr
}

func TestRun_StatusOnAMissingConfigWithAPopulatedJournalExitsNonZero(t *testing.T) {
	code, stdout, stderr := runWithLostConfig(t, "status")
	if code == 0 {
		t.Errorf("status exited 0 although the configuration is missing and the journal holds backups\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if saysFirstRun.MatchString(stdout) {
		t.Errorf("status called a deployment with history a fresh install:\n%s", stdout)
	}
	all := stdout + stderr
	if !strings.Contains(all, "configuration missing") || !strings.Contains(all, "journal") || !strings.Contains(all, "holds 1 backup") {
		t.Errorf("status did not say the configuration is missing while the journal holds backups:\n%s", all)
	}
}

func TestRun_CheckOnAMissingConfigWithAPopulatedJournalExitsNonZero(t *testing.T) {
	code, stdout, stderr := runWithLostConfig(t, "check")
	if code == 0 {
		t.Errorf("check exited 0 although the configuration is missing and the journal holds backups\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	all := stdout + stderr
	if !strings.Contains(all, "configuration missing") || !strings.Contains(all, "holds 1 backup") {
		t.Errorf("check did not say the configuration is missing while the journal holds backups:\n%s", all)
	}
}

// The botched-edit shape: the file exists and declares no sources, and the
// journal it names holds backups.
func TestRun_StatusOnAnEmptySourcesConfigOverAPopulatedJournalExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	seedHistory(t, db)
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("poll_interval: 1h\nstate:\n  database: "+db+"\nsources: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var code int
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() { code = run([]string{"status", "--config", cfg}) })
	})
	if code == 0 {
		t.Errorf("status exited 0 on a configuration that lost every source while the journal holds backups\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if saysFirstRun.MatchString(stdout) {
		t.Errorf("status called a deployment with history a first run:\n%s", stdout)
	}
}
