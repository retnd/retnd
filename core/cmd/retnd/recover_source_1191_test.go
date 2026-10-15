package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/recovery/budget"
	"github.com/retnd/retnd/core/internal/transport"
	"github.com/retnd/retnd/core/service"
)

// Issues #1191 and #1193: `retnd recover` with no --to sends the backup back
// where it came from, the set's own SFTP source, but only where that can
// work, only once the operator says so, and with a spelling (source: and
// source:FOLDER) that leaves a staging folder called "source" alone.

// writeSFTPTestConfig is one deployment whose one set pulls over SFTP from a
// host nothing here ever dials: every case below is refused before a
// connection would be made. readOnly declares the set read_only.
func writeSFTPTestConfig(t *testing.T, readOnly ...bool) string {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	known := filepath.Join(dir, "known_hosts")
	for _, f := range []string{key, known} {
		if err := os.WriteFile(f, []byte("unused by these tests\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ro := ""
	if len(readOnly) > 0 && readOnly[0] {
		ro = "        read_only: true\n"
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
		ro +
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

func TestRecover1193_NoTargetWithoutTheFlagIsRefusedBeforeAnyNetworkCall(t *testing.T) {
	configPath := writeSFTPTestConfig(t)
	var code int
	out := captureStderr(t, func() {
		code = run([]string{"recover", "--config", configPath, recoverID})
	})
	if want := recoverExitCodes[service.RecoverySourceHostNotAcknowledged]; want != 40 || code != want {
		t.Errorf("recover with no --to on an SFTP set = %d, want 40 (the source host, refused until acknowledged)", code)
	}
	for _, says := range []string{"sftp://retnd-pull@db1.example.internal/var/backups/postgres/" + transport.RecoveredFolderPrefix, "--to source:", "--acknowledge-source-write"} {
		if !strings.Contains(out, says) {
			t.Errorf("stderr %q does not say %q", strings.TrimSpace(out), says)
		}
	}
	// db1.example.internal does not resolve: a refusal that had dialled
	// would be the unreachable code, or a DNS error, not this one.
	if strings.Contains(out, "could not reach") {
		t.Errorf("the refusal came after a network call: %s", out)
	}
}

func TestRecover1193_ASetDeclaredReadOnlyHasNoDefault(t *testing.T) {
	configPath := writeSFTPTestConfig(t, true)
	var code int
	out := captureStderr(t, func() {
		code = run([]string{"recover", "--config", configPath, recoverID, "--acknowledge-source-write"})
	})
	if code != exitUsage || !strings.Contains(out, "read_only") || !strings.Contains(out, "--to") {
		t.Errorf("recover with no --to on a read_only set = %d %q; want a usage error naming why and the alternatives", code, strings.TrimSpace(out))
	}
}

func TestRecover1193_ANonSFTPSetHasNoDefaultAndSaysWhy(t *testing.T) {
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

func TestRecover1193_TheSourceSpellingNeedsTheColonAndBareSourceIsAStagingName(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want service.RecoveryTarget
	}{
		{"source", service.RecoveryTarget{Kind: "staging", Path: "source"}},
		{"./source", service.RecoveryTarget{Kind: "staging", Path: "./source"}},
		{"./source:x", service.RecoveryTarget{Kind: "staging", Path: "./source:x"}},
		{"source:", service.RecoveryTarget{Kind: "source"}},
		{"source:" + transport.RecoveredFolderPrefix + "incident-4711", service.RecoveryTarget{Kind: "source", Path: transport.RecoveredFolderPrefix + "incident-4711"}},
	} {
		got, err := parseRecoverTarget(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%q = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
}

func TestRecover1193_TheSourceFlagWithAnotherTargetIsAUsageError(t *testing.T) {
	configPath := writeSFTPTestConfig(t)
	for _, to := range []string{"-", "restore", "source", "medium:offsite"} {
		var code int
		out := captureStderr(t, func() {
			code = run([]string{"recover", "--config", configPath, recoverID, "--to", to, "--acknowledge-source-write"})
		})
		if code != exitUsage || !strings.Contains(out, "--acknowledge-source-write") {
			t.Errorf("--to %s --acknowledge-source-write = %d %q; want a usage error", to, code, strings.TrimSpace(out))
		}
	}
}

func TestRecover1193_AWholeSetOntoTheSourceIsSpelledOut(t *testing.T) {
	configPath := writeSFTPTestConfig(t)
	var code int
	out := captureStderr(t, func() {
		code = run([]string{"recover", "--config", configPath, "--set", "production/postgres-primary", "--to", "source:", "--acknowledge-source-write"})
	})
	if code != exitUsage || !strings.Contains(out, "--all") || !strings.Contains(out, "--newest-only") {
		t.Errorf("a whole set onto the source with no narrowing = %d %q; want a usage error naming --newest-only, --since and --all", code, strings.TrimSpace(out))
	}
	for _, narrow := range [][]string{{"--all"}, {"--newest-only"}, {"--since", "2026-10-01"}} {
		args := append([]string{"recover", "--config", configPath, "--set", "production/postgres-primary", "--to", "source:", "--acknowledge-source-write"}, narrow...)
		out := captureStderr(t, func() { code = run(args) })
		if code == exitUsage {
			t.Errorf("%v was refused as usage: %s", narrow, strings.TrimSpace(out))
		}
	}
}

func TestRecover1193_TheTerminalPrintsTheDestinationItAcknowledgesFirst(t *testing.T) {
	configPath := writeSFTPTestConfig(t)
	out := captureStderr(t, func() {
		_ = run([]string{"recover", "--config", configPath, recoverID, "--to", "source:" + transport.RecoveredFolderPrefix + "incident-1", "--acknowledge-source-write"})
	})
	want := "sftp://retnd-pull@db1.example.internal/var/backups/postgres/" + transport.RecoveredFolderPrefix + "incident-1/"
	i := strings.Index(out, want)
	if i < 0 || !strings.Contains(out[:i+len(want)+80], "sh1-") {
		t.Errorf("stderr does not print the destination and its id before anything happens:\n%s", out)
	}
}

func TestRecover1193_PreflightDescribesAndChecks(t *testing.T) {
	// A read_only set is answered without a probe, so this never dials.
	configPath := writeSFTPTestConfig(t, true)
	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"recover", "--config", configPath, recoverID, "--to", "source:", "--preflight", "--json"})
	})
	if code != exitOK {
		t.Fatalf("recover --to source: --preflight = %d: %s", code, stdout)
	}
	var got struct {
		Available   bool   `json:"available"`
		URL         string `json:"url"`
		WriteAccess string `json:"write_access"`
		Destination string `json:"destination_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if !got.Available || got.WriteAccess != "read_only" || !strings.HasPrefix(got.URL, "sftp://") || !strings.HasPrefix(got.Destination, "sh1-") {
		t.Errorf("preflight = %+v", got)
	}
}

func TestRecover1193_UsageSaysTheSourceSpellingAndTheFlag(t *testing.T) {
	out := captureStderr(t, usage)
	for _, says := range []string{"--acknowledge-source-write", "source:[FOLDER]", "--all"} {
		if !strings.Contains(out, says) {
			t.Errorf("usage() does not mention %s", says)
		}
	}
	if strings.Contains(out, "--to source[") {
		t.Error("usage() still spells the source target without its colon")
	}
}

func TestRecover1193_TheSourceHostCodesHaveTheirOwnExitCodes(t *testing.T) {
	for code, want := range map[string]int{
		service.RecoverySourceHostUnavailable:     38,
		service.RecoverySourceHostReadOnly:        39,
		service.RecoverySourceHostNotAcknowledged: 40,
		service.RecoverySourceHostUnreachable:     41,
		service.RecoverySourceHostNoSpace:         42,
		service.RecoverySourceHostProbeLeftFile:   43,
	} {
		if got := recoverExitCodes[code]; got != want {
			t.Errorf("%s exits %d, want %d", code, got, want)
		}
	}
}

// Every exit code a recovery can end with is its own, so a script can
// branch on it. The one collision that shipped before this test existed
// (32: a partial set recover and the concurrent-read budget) is recorded
// here rather than renumbered, which would break scripts; nothing new may
// join it.
func TestRecover1193_EveryRecoveryExitCodeIsUnique(t *testing.T) {
	owners := map[int][]string{}
	for code, n := range recoverExitCodes {
		owners[n] = append(owners[n], code)
	}
	for _, name := range budget.All {
		owners[budget.ExitCode(name)] = append(owners[budget.ExitCode(name)], "budget:"+string(name))
	}
	owners[exitRecoverThawStarted] = append(owners[exitRecoverThawStarted], "exitRecoverThawStarted")
	owners[exitRecoverPartial] = append(owners[exitRecoverPartial], "exitRecoverPartial")
	known := map[int]string{
		// The budget table and the recovery table share these by design:
		// each is the same refusal under both names.
		18: "RECOVERY_BELOW_RESERVE,budget:staging_reserve",
		33: "RECOVERY_DESTINATION_BUSY,budget:destination_reads",
		36: "RECOVERY_DOWNLOADS_BUSY,budget:download_streams",
		// The one real collision, recorded: a partial set recover and the
		// concurrent-read refusal both exit 32 (D8 in the #1193 review).
		32: "RECOVERY_READS_BUSY,budget:concurrent_reads,exitRecoverPartial",
	}
	for n, who := range owners {
		sort.Strings(who)
		if len(who) < 2 {
			continue
		}
		want := strings.Split(known[n], ",")
		sort.Strings(want)
		if strings.Join(who, ",") != strings.Join(want, ",") {
			t.Errorf("exit %d is shared by %v; only the recorded pairs may share a number", n, who)
		}
	}
	for _, n := range budget.Unused {
		if len(owners[n]) > 0 {
			t.Errorf("exit %d is retired and must not be reused, but %v use it", n, owners[n])
		}
	}
}
