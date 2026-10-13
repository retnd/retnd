package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/recovery/budget"
	"github.com/retnd/retnd/core/service"
)

// `retnd recover` (EPIC Q, #976): hand the retained bytes back from a
// terminal, through the same service code the two HTTP routes use.

const recoverID = "production/postgres-primary/backup.dump"

// committedArtifact runs one real cycle so the journal holds a committed
// artifact with a local copy, a recorded SHA-256 and a placement.
func committedArtifact(t *testing.T) (configPath, localFile string) {
	t.Helper()
	configPath = writeTestConfig(t)
	if got := run([]string{"run", "--config", configPath}); got != 0 {
		t.Fatalf("run = %d, want 0 (this test needs one committed artifact)", got)
	}
	return configPath, filepath.Join(filepath.Dir(configPath), "local", "backup.dump")
}

func TestRecoverIsDiscoverableFromTheCommandLine(t *testing.T) {
	if _, ok := commands["recover"]; !ok {
		t.Fatal("there is no recover command")
	}
	out := captureStderr(t, usage)
	if !strings.Contains(out, "recover <source/backup-set/artifact>") {
		t.Error("usage() does not list the recover verb")
	}
	for _, flag := range []string{"--to", "--copy", "--acknowledge-cost", "--json"} {
		if !strings.Contains(out, flag) {
			t.Errorf("usage() does not mention %s", flag)
		}
	}
	if !strings.Contains(out, "snapshot restore") {
		t.Error("usage() should say which verb a kopia set uses instead")
	}
}

func TestRecoverIsRefusedBeforeAnythingIsOpened(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{"no artifact", []string{"--to", "-"}, "expected <source/backup-set/artifact>"},
		{"no target", []string{recoverID}, "--to is required"},
		{"two artifacts to standard output", []string{recoverID, recoverID, "--to", "-"}, "cannot go to standard output"},
		{"empty storage target", []string{recoverID, "--to", "medium:"}, "names no destination"},
		{"not an artifact id", []string{"nonsense", "--to", "-"}, "source/backup-set/name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStderr(t, func() {
				if code := cmdRecover(tc.args); code != exitUsage {
					t.Errorf("cmdRecover = %d, want %d: a malformed request must not reach a service", code, exitUsage)
				}
			})
			if !strings.Contains(out, tc.says) {
				t.Errorf("stderr %q does not say %q", strings.TrimSpace(out), tc.says)
			}
		})
	}
}

func TestRecoverTargetSpellings(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want service.RecoveryTarget
	}{
		{"-", service.RecoveryTarget{Kind: "download"}},
		{"restore/dump.tar", service.RecoveryTarget{Kind: "staging", Path: "restore/dump.tar"}},
		{"medium:offsite", service.RecoveryTarget{Kind: "storage", Medium: "offsite"}},
		{"medium:offsite/recovered/dump.tar", service.RecoveryTarget{Kind: "storage", Medium: "offsite", Key: "recovered/dump.tar"}},
	} {
		got, err := parseRecoverTarget(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%q = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "medium:", "medium:/key"} {
		if _, err := parseRecoverTarget(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestEveryRecoveryCodeHasItsOwnExitCode(t *testing.T) {
	seen := map[int]string{}
	for _, code := range service.RecoveryCodes() {
		n, ok := recoverExitCodes[code]
		if !ok {
			t.Errorf("code %s has no exit code", code)
			continue
		}
		if n <= exitEngineHoldsDeployment {
			t.Errorf("code %s exits %d, which a published meaning already owns", code, n)
		}
		if other, dup := seen[n]; dup {
			t.Errorf("%s and %s both exit %d, so a script cannot tell them apart", code, other, n)
		}
		seen[n] = code
	}
	if len(recoverExitCodes) != len(service.RecoveryCodes()) {
		t.Errorf("%d exit codes for %d recovery codes", len(recoverExitCodes), len(service.RecoveryCodes()))
	}
}

func TestRecoverToStagingPlacesTheVerifiedBytes(t *testing.T) {
	configPath, local := committedArtifact(t)
	want, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	var code int
	out := captureStdout(t, func() {
		code = run([]string{"recover", "--config", configPath, recoverID, "--to", "restored/backup.dump", "--json"})
	})
	if code != 0 {
		t.Fatalf("recover = %d, output %q", code, out)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("--json output is not JSON: %q", out)
	}
	path, _ := res["path"].(string)
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("placed file wrong: %v", err)
	}
	if !strings.HasPrefix(path, filepath.Join(filepath.Dir(configPath), "recovery")) {
		t.Fatalf("placed outside the staging root: %s", path)
	}
	if res["sha256"] == "" || res["bytes"].(float64) != float64(len(want)) {
		t.Fatalf("result = %v", res)
	}
}

func TestRecoverToStandardOutputWritesOnlyTheBytes(t *testing.T) {
	configPath, local := committedArtifact(t)
	want, _ := os.ReadFile(local)
	var code int
	stderr := captureStderr(t, func() {
		out := captureStdout(t, func() {
			code = run([]string{"recover", "--config", configPath, recoverID, "--to", "-"})
		})
		if out != string(want) {
			t.Errorf("stdout is not exactly the artifact's bytes (%d vs %d)", len(out), len(want))
		}
	})
	if code != 0 {
		t.Fatalf("recover = %d, stderr %q", code, stderr)
	}
}

func TestRecoverOfACorruptedCopyExitsWithItsOwnCodeAndPrintsNoBytes(t *testing.T) {
	configPath, local := committedArtifact(t)
	data, _ := os.ReadFile(local)
	data[len(data)-1] ^= 1 // same size, different bytes
	if err := os.WriteFile(local, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	stderr := captureStderr(t, func() {
		out := captureStdout(t, func() {
			code = run([]string{"recover", "--config", configPath, recoverID, "--to", "-"})
		})
		if out != "" {
			t.Errorf("%d bytes were written before verification failed", len(out))
		}
	})
	if code != recoverExitCodes[service.RecoveryLocalCopyCorrupt] {
		t.Fatalf("exit = %d, want %d; stderr %q", code, recoverExitCodes[service.RecoveryLocalCopyCorrupt], stderr)
	}
	if !strings.Contains(stderr, "does not match") {
		t.Fatalf("stderr = %q", stderr)
	}
	entries, _ := filepath.Glob(filepath.Join(filepath.Dir(configPath), "recovery", "*"))
	if len(entries) != 0 {
		t.Fatalf("staging not empty: %v", entries)
	}
}

func TestRecoverJSONRefusalNamesItsCode(t *testing.T) {
	configPath, _ := committedArtifact(t)
	var code int
	out := captureStdout(t, func() {
		code = run([]string{"recover", "--config", configPath, recoverID, "--to", "medium:nowhere", "--json"})
	})
	if code != recoverExitCodes[service.RecoveryTargetInvalid] {
		t.Fatalf("exit = %d", code)
	}
	var res struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Error.Code != service.RecoveryTargetInvalid {
		t.Fatalf("--json refusal = %q, %v", out, err)
	}
}

func TestRecoverOfAnUnknownArtifactIsAnOrdinaryFailure(t *testing.T) {
	configPath, _ := committedArtifact(t)
	var code int
	captureStderr(t, func() {
		code = run([]string{"recover", "--config", configPath, "production/postgres-primary/nope", "--to", "-"})
	})
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d", code, exitFailure)
	}
}

func TestRecoverSpawnsNoProcessOfItsOwn(t *testing.T) {
	src, err := os.ReadFile("recover.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"os/exec", "exec.Command", "os.StartProcess"} {
		if bytes.Contains(src, []byte(banned)) {
			t.Errorf("recover.go mentions %s", banned)
		}
	}
}

func TestTheRecoveryBudgetsExitWithTheCodesTheBudgetTableGivesThem(t *testing.T) {
	for code, name := range map[string]budget.Name{
		service.RecoveryReadsBusy:       budget.ConcurrentReads,
		service.RecoveryDestinationBusy: budget.DestinationReads,
		service.RecoveryDownloadsBusy:   budget.DownloadStreams,
		service.RecoveryBelowReserve:    budget.StagingReserve,
	} {
		if recoverExitCodes[code] != budget.ExitCode(name) {
			t.Errorf("%s exits %d and budget %s says %d: one refusal, two numbers", code, recoverExitCodes[code], name, budget.ExitCode(name))
		}
	}
}
