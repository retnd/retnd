package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// The review of #1256, M4. #1193 wants every retained backup of a set onto its
// production host said as --all. A range counts as saying which backups you
// meant, but --until 2099-01-01 (or a habitual --until $(date +%F), or --since
// 1970-01-01) selects the whole set under another name. So when the range
// keeps every retained restore point, the terminal refuses it and asks for
// --all, naming the count, before any network call.

// seedSourceSet records two retained backups of the SFTP fixture's set, one
// that arrived on 2026-03-01 and one on 2026-04-02, without a cycle: nothing
// here dials the host.
func seedSourceSet(t *testing.T, configPath string) {
	t.Helper()
	ctx := context.Background()
	j, err := state.Open(ctx, journalNamedByTestConfig(t, configPath))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	set, _ := model.NewBackupSetID("production", "postgres-primary")
	for name, at := range map[string]time.Time{
		"march.dump": time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		"april.dump": time.Date(2026, 4, 2, 8, 0, 0, 0, time.UTC),
	} {
		id, err := model.NewArtifactID(set, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j.RecordTransition(ctx, state.Transition{Artifact: id, Key: "d-" + name, To: string(lifecycle.Discovered), OccurredAt: at, RemotePath: "/var/backups/postgres/" + name}); err != nil {
			t.Fatal(err)
		}
		if _, err := j.RecordTransition(ctx, state.Transition{Artifact: id, Key: "c-" + name, From: string(lifecycle.Discovered), To: string(lifecycle.Complete), OccurredAt: at.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIssue1251_ARangeThatKeepsTheWholeSetOntoTheSourceNeedsAll(t *testing.T) {
	configPath := writeSFTPTestConfig(t)
	seedSourceSet(t, configPath)
	base := []string{"recover", "--config", configPath, "--set", "production/postgres-primary", "--to", "source:", "--acknowledge-source-write"}
	for _, rng := range [][]string{
		{"--until", "2099-01-01"},
		{"--since", "1970-01-01"},
		{"--since", "2026-01-01", "--until", "2026-12-31"},
	} {
		var code int
		out := captureStderr(t, func() { code = run(append(append([]string{}, base...), rng...)) })
		if code != exitUsage {
			t.Errorf("%v onto the source host = %d, want %d: it keeps every restore point\n%s", rng, code, exitUsage, out)
			continue
		}
		for _, says := range []string{"--all", "all 2"} {
			if !strings.Contains(out, says) {
				t.Errorf("%v: the refusal %q does not say %q", rng, strings.TrimSpace(out), says)
			}
		}
		if strings.Contains(out, "could not reach") {
			t.Errorf("%v: the refusal came after a network call: %s", rng, out)
		}
	}
	// A range that leaves one out is a narrowing, so it is not refused as usage.
	var code int
	out := captureStderr(t, func() { code = run(append(append([]string{}, base...), "--until", "2026-03-31")) })
	if code == exitUsage {
		t.Errorf("--until 2026-03-31 keeps one of two and was refused as usage: %s", strings.TrimSpace(out))
	}
	// Somewhere other than the source host, a range may keep everything.
	out = captureStderr(t, func() {
		code = run([]string{"recover", "--config", configPath, "--set", "production/postgres-primary", "--to", "restored", "--until", "2099-01-01", "--preflight"})
	})
	if code == exitUsage {
		t.Errorf("--until 2099-01-01 into staging was refused as usage: %s", strings.TrimSpace(out))
	}
}

func TestIssue1251_UsageSaysUntilNarrowsAWholeSetOntoTheSource(t *testing.T) {
	text := strings.Join(strings.Fields(captureStderr(t, usage)), " ")
	if !strings.Contains(text, "A whole set onto the source host also needs --newest-only, --since, --until or --all") {
		t.Errorf("usage() still lists the source-host narrowings without --until")
	}
	if !strings.Contains(text, "a range that keeps every retained restore point still needs --all") {
		t.Errorf("usage() does not say a range that keeps the whole set still needs --all")
	}
}

// --all reaches the service as all, so the service's own rule (the same one)
// does not refuse what the terminal let through.
func TestIssue1251_AllReachesTheService(t *testing.T) {
	configPath := writeSFTPTestConfig(t)
	seedSourceSet(t, configPath)
	var code int
	out := captureStderr(t, func() {
		code = run([]string{"recover", "--config", configPath, "--set", "production/postgres-primary", "--to", "source:", "--acknowledge-source-write", "--all"})
	})
	if code == exitUsage || strings.Contains(out, "whole set onto its source host") {
		t.Errorf("--all onto the source host was refused for not saying all = %d\n%s", code, out)
	}
}
