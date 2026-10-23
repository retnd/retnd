package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// Panel finding C7 on retnd#1050: a lost configuration volume next to a
// populated state volume looked exactly like a fresh install, so the engine
// wrote a default config.yaml over it and every health read said all was
// well. The journal is the evidence that a deployment has history, so it is
// asked first.

// populatedJournal writes a journal at path that holds one artifact, the
// way a deployment that has run a backup does, and closes it.
func populatedJournal(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	j, err := state.Open(ctx, path)
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

func TestEnsureDefaultConfigRefusesOverAPopulatedJournal(t *testing.T) {
	configPath, statePath := emptyInstall(t)
	populatedJournal(t, statePath)

	wrote, path, err := EnsureDefaultConfig(FirstRunDefaults{ConfigPath: configPath, StateDatabase: statePath})
	if wrote {
		t.Errorf("a default configuration was written over a journal that holds backups")
	}
	if !errors.Is(err, ErrConfigLostOverJournal) {
		t.Errorf("err = %v, want it to wrap ErrConfigLostOverJournal", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("%s exists afterwards (stat err = %v); nothing should have been written", path, statErr)
	}
}

func TestEnsureDefaultConfigStillWritesOverAnEmptyJournal(t *testing.T) {
	configPath, statePath := emptyInstall(t)
	// A journal that was opened once and has recorded nothing is a fresh
	// install that was started before, not a deployment with history.
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	j, err := state.Open(context.Background(), statePath)
	if err != nil {
		t.Fatal(err)
	}
	_ = j.Close()

	wrote, _, err := EnsureDefaultConfig(FirstRunDefaults{ConfigPath: configPath, StateDatabase: statePath})
	if err != nil || !wrote {
		t.Fatalf("= (%v, %v), want the default to be written over an empty journal", wrote, err)
	}
}

func TestEnsureDefaultConfigRefusesOverAJournalItCannotRead(t *testing.T) {
	configPath, statePath := emptyInstall(t)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	junk := "not a sqlite database, but long enough that nobody would call the file empty at a glance"
	if err := os.WriteFile(statePath, []byte(junk), 0o600); err != nil {
		t.Fatal(err)
	}
	wrote, path, err := EnsureDefaultConfig(FirstRunDefaults{ConfigPath: configPath, StateDatabase: statePath})
	if wrote || err == nil {
		t.Fatalf("= (%v, %v), want a refusal: a journal that cannot be read is not evidence of a fresh install", wrote, err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a configuration was written next to an unreadable journal")
	}
}

func TestCheckJournalIsFresh(t *testing.T) {
	ctx := context.Background()
	_, statePath := emptyInstall(t)
	if err := CheckJournalIsFresh(ctx, statePath); err != nil {
		t.Errorf("no journal file: %v, want nil", err)
	}
	populatedJournal(t, statePath)
	err := CheckJournalIsFresh(ctx, statePath)
	if !errors.Is(err, ErrConfigLostOverJournal) {
		t.Fatalf("populated journal: %v, want ErrConfigLostOverJournal", err)
	}
	for _, want := range []string{statePath, "1 "} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err.Error(), want)
		}
	}
}
