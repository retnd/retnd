package webhost

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// Issue #1186, review of #1187. The sidebar's Backups badge reads
// `retained_count` off GET /system/health, which the shell already polls,
// instead of downloading every backup every 30 seconds to read a length.
//
// What it counts is decided once, here, against the real engine: every
// backup the journal holds outside quarantine, the backups of a set whose
// configuration was removed included. That is the Backups page's rows less
// the ones the Quarantine badge counts, so no backup is in both badges. Until
// this, every browser spec proved a rule only the mock had: the real
// GET /backups lists quarantined backups too.

// writeRetainedFixture runs one cycle over three sets, then quarantines
// alpha's backup the way production does (its committed local copy stops
// matching its hash and the next cycle's reconcile catches it), then removes
// gamma from the configuration, which leaves its backup on storage and on
// the list. It returns the config path.
func writeRetainedFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	set := func(name string) string {
		remote := filepath.Join(dir, "remote-"+name)
		if err := os.MkdirAll(remote, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remote, name+".dump"), []byte("payload for "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		return "      - id: " + name + "\n" +
			"        remote:\n          type: local\n" +
			"        remote_path: " + remote + "\n" +
			"        local_path: " + filepath.Join(dir, "local-"+name) + "\n" +
			"        include:\n          - \"*.dump\"\n" +
			"        completion:\n          strategy: rename\n" +
			"        stale_after: 24h\n"
	}
	head := "poll_interval: 15m\n" +
		"state:\n  database: " + filepath.Join(dir, "state.db") + "\n" +
		"sources:\n  - id: production\n    backup_sets:\n"
	tail := "retention:\n  timezone: UTC\n  week_starts_on: monday\n"
	path := filepath.Join(dir, "config.yaml")
	write := func(content string) {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(head + set("alpha") + set("beta") + set("gamma") + tail)
	runCLI(t, "run", "--config", path)

	// Alpha's committed copy no longer matches what was recorded. The run
	// that notices may well exit non-zero; what matters is the journal.
	if err := os.WriteFile(filepath.Join(dir, "local-alpha", "alpha.dump"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command(backupManagerCLI(t), "run", "--config", path).Run()

	write(head + set("alpha") + set("beta") + tail)
	return path
}

// cliStdout runs the CLI and returns its standard output alone, with the
// exit code, since the read-mode notice goes to standard error.
func cliStdout(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(backupManagerCLI(t), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("retnd %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), code
}

func TestIssue1186_RetainedCountOnTheRealHealthHandler(t *testing.T) {
	configPath := writeRetainedFixture(t)
	svc, cleanup, err := service.Open(context.Background(), configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	router := opsRouter(svc, NotYetImplementedGate{})

	// The control: the list really does hold a quarantined backup and a
	// removed set's backup, or the count below proves nothing.
	rec := send(router, http.MethodGet, "/api/v1/backups", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /backups = %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Artifacts []struct {
			BackupSetID string `json:"backup_set_id"`
			State       string `json:"state"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	quarantined, removed := 0, 0
	for _, a := range list.Artifacts {
		if strings.HasPrefix(a.State, "QUARANTINED") {
			quarantined++
		}
		if a.BackupSetID == "production/gamma" {
			removed++
		}
	}
	if len(list.Artifacts) != 3 || quarantined != 1 || removed != 1 {
		t.Fatalf("fixture: GET /backups holds %d rows, %d quarantined, %d of the removed set; want 3, 1, 1: %s",
			len(list.Artifacts), quarantined, removed, rec.Body.String())
	}

	rec = send(router, http.MethodGet, "/api/v1/system/health", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /system/health = %d %s", rec.Code, rec.Body.String())
	}
	var health struct {
		RetainedCount *int `json:"retained_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.RetainedCount == nil {
		t.Fatalf("GET /system/health has no retained_count: %s", rec.Body.String())
	}
	// beta, and gamma's backup that outlived its configuration. Not alpha's,
	// which the Quarantine badge counts.
	if *health.RetainedCount != 2 {
		t.Errorf("retained_count = %d, want 2 (three backups listed, one quarantined)", *health.RetainedCount)
	}

	// The terminal says the same number, as a number.
	if out, code := cliStdout(t, "artifacts", "--count", "--config", configPath); code != 0 || strings.TrimSpace(out) != "2" {
		t.Errorf("artifacts --count = %q (exit %d), want 2", out, code)
	}
	if out, code := cliStdout(t, "artifacts", "--count", "--json", "--config", configPath); code != 0 {
		t.Errorf("artifacts --count --json exited %d", code)
	} else {
		var got map[string]int
		if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 1 || got["count"] != 2 {
			t.Errorf("artifacts --count --json = %q (%v), want {\"count\": 2}", out, err)
		}
	}
	out, _ := cliStdout(t, "status", "--json", "--config", configPath)
	var status struct {
		RetainedCount *int `json:"retained_count"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil || status.RetainedCount == nil || *status.RetainedCount != 2 {
		t.Errorf("status --json retained_count = %v (%v), want 2\n%s", status.RetainedCount, err, out)
	}
}
