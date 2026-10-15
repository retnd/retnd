package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/transport"
	"github.com/retnd/retnd/core/service"
)

// Issue #1191: `retnd recover` with no --to sends the backup back where it
// came from, the set's own SFTP source, and only once the operator says so.

// writeSFTPTestConfig is one deployment whose one set pulls over SFTP from a
// host nothing here ever dials: every case below is refused before a
// connection would be made.
func writeSFTPTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	known := filepath.Join(dir, "known_hosts")
	for _, f := range []string{key, known} {
		if err := os.WriteFile(f, []byte("unused by these tests\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, "config.yaml")
	content := "poll_interval: 15m\n" +
		"state:\n" +
		"  database: " + filepath.Join(dir, "state.db") + "\n" +
		"sources:\n" +
		"  - id: production\n" +
		"    backup_sets:\n" +
		"      - id: postgres-primary\n" +
		"        remote:\n" +
		"          type: sftp\n" +
		"          host: db1.example.internal\n" +
		"          port: 22\n" +
		"          user: retnd-pull\n" +
		"          key:\n" +
		"            file: " + key + "\n" +
		"          known_hosts: " + known + "\n" +
		"        remote_path: /var/backups/postgres\n" +
		"        local_path: " + filepath.Join(dir, "local") + "\n" +
		"        completion:\n" +
		"          strategy: rename\n" +
		"        stale_after: 24h\n" +
		"retention:\n" +
		"  timezone: UTC\n" +
		"  week_starts_on: monday\n"
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestRecover1191_WithNoTargetItDefaultsToTheSourceHostAndAsksFirst(t *testing.T) {
	configPath := writeSFTPTestConfig(t)
	var code int
	out := captureStderr(t, func() {
		code = run([]string{"recover", "--config", configPath, recoverID})
	})
	want := recoverExitCodes[service.RecoverySourceNotAcknowledged]
	if want == 0 || code != want {
		t.Errorf("recover with no --to on an SFTP set = %d, want %d (the source host, refused until acknowledged)", code, want)
	}
	for _, says := range []string{"sftp://retnd-pull@db1.example.internal", "/var/backups/postgres/" + transport.RecoveredFolderPrefix, "--acknowledge-source-write"} {
		if !strings.Contains(out, says) {
			t.Errorf("stderr %q does not say %q", strings.TrimSpace(out), says)
		}
	}
}

func TestRecover1191_ANonSFTPSetHasNoDefaultAndSaysWhy(t *testing.T) {
	configPath, _ := committedArtifact(t)
	var code int
	out := captureStderr(t, func() {
		code = run([]string{"recover", "--config", configPath, recoverID})
	})
	if code != exitUsage {
		t.Errorf("recover with no --to on a local-mount set = %d, want %d", code, exitUsage)
	}
	for _, says := range []string{"not SFTP", "--to"} {
		if !strings.Contains(out, says) {
			t.Errorf("stderr %q does not say %q", strings.TrimSpace(out), says)
		}
	}
}

func TestRecover1191_TheSourceTargetSpellings(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want service.RecoveryTarget
	}{
		{"source", service.RecoveryTarget{Kind: "source"}},
		{"source:" + transport.RecoveredFolderPrefix + "incident-4711", service.RecoveryTarget{Kind: "source", Path: transport.RecoveredFolderPrefix + "incident-4711"}},
	} {
		got, err := parseRecoverTarget(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%q = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	if _, err := parseRecoverTarget("source:"); err == nil {
		t.Error(`"source:" names no folder and should be refused`)
	}
}

func TestRecover1191_UsageSaysTheSourceIsTheDefault(t *testing.T) {
	out := captureStderr(t, usage)
	for _, says := range []string{"--acknowledge-source-write", "--to source"} {
		if !strings.Contains(out, says) {
			t.Errorf("usage() does not mention %s", says)
		}
	}
}
