package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/service"
)

// W-04 / #982: `settings assurance patch` and `backup-set assurance` writes
// beside a serving engine. Before this they were refused (exit 3, nothing
// written) even with RETND_API_URL naming the engine; the browser's PATCH
// route hot-reloads the process that served it, so the CLI hands the change
// to that route and the engine owns the write.

// seedAssuredCopy runs one cycle so the journal holds a verified copy, which
// is what gives the engine a next-due time to move. It runs before any
// engine is announced, so the direct route is the only one it can take.
func seedAssuredCopy(t *testing.T, configPath string) {
	t.Helper()
	if code := run([]string{"run", "--config", configPath}); code != 0 {
		t.Fatalf("seeding a verified copy: run exited %d", code)
	}
}

func dueFor(t *testing.T, engine *fakeEngine) service.AssuranceDueItem {
	t.Helper()
	items, err := engine.svc.AssuranceDue(t.Context(), service.AssuranceDueFilter{BackupSetID: cliSet})
	if err != nil {
		t.Fatalf("reading the engine's schedule: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("the engine schedules %d copies, want 1: %+v", len(items), items)
	}
	return items[0]
}

func TestSettingsAssurancePatchReachesTheAttachedEngineAndMovesNextDue(t *testing.T) {
	cliConfig := writeTestConfig(t)
	seedAssuredCopy(t, cliConfig)
	engine := startFakeEngineFor(t, cliConfig)
	engine.attach(t)

	before := dueFor(t, engine)
	cliBefore := readFile(t, cliConfig)

	var code int
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code = run([]string{"settings", "assurance", "patch", "--scrub-cadence", "12h", "--json", "--config", cliConfig})
		})
	})
	if code != 0 {
		t.Fatalf("settings assurance patch beside an attached engine exited %d, want 0\nstderr: %s", code, stderr)
	}
	if !slices.Contains(engine.requests(), "PATCH /settings/assurance") {
		t.Errorf("the engine never received PATCH /settings/assurance; it saw %v", engine.requests())
	}
	if readFile(t, cliConfig) != cliBefore {
		t.Error("the CLI wrote its own config.yaml as well as handing the change to the engine")
	}

	var doc struct {
		Scrub struct {
			CadenceSeconds int64 `json:"cadence_seconds"`
		} `json:"scrub"`
	}
	// The mode line and the startup log lines share stdout with the body, as
	// they do on the direct route; the body is the indented object that
	// starts at the last line that is only an opening brace.
	body := stdout
	if i := strings.LastIndex(stdout, "\n{\n"); i >= 0 {
		body = stdout[i+1:]
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil || doc.Scrub.CadenceSeconds != 43200 {
		t.Errorf("--json did not print the engine's answer: %q (%v)", stdout, err)
	}

	got, err := engine.svc.AssuranceSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Policy.Scrub.Cadence != 12*time.Hour {
		t.Errorf("the engine's scrub cadence is %s, the CLI set 12h", got.Policy.Scrub.Cadence)
	}
	after := dueFor(t, engine)
	if after.Cadence != 12*time.Hour {
		t.Errorf("the engine schedules this copy on %s, want 12h", after.Cadence)
	}
	if want := after.LastAssuredAt.Add(12 * time.Hour); !after.NextDueAt.Equal(want) {
		t.Errorf("next due is %s, want last assured + 12h = %s", after.NextDueAt, want)
	}
	if after.NextDueAt.Equal(before.NextDueAt) {
		t.Errorf("the engine's next-due did not move (still %s)", before.NextDueAt)
	}
}

func TestBackupSetAssuranceWritesReachTheAttachedEngineAndMoveNextDue(t *testing.T) {
	cliConfig := writeTestConfig(t)
	seedAssuredCopy(t, cliConfig)
	engine := startFakeEngineFor(t, cliConfig)
	engine.attach(t)

	before := dueFor(t, engine)
	cliBefore := readFile(t, cliConfig)
	setPath := "/backup-sets/" + cliSet + "/assurance"

	var code int
	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			code = run([]string{"backup-set", "assurance", cliSet, "--scrub-cadence", "2h", "--config", cliConfig})
		})
	})
	if code != 0 {
		t.Fatalf("backup-set assurance patch beside an attached engine exited %d, want 0\nstderr: %s", code, stderr)
	}
	if !slices.Contains(engine.requests(), "PATCH "+setPath) {
		t.Errorf("the engine never received the set's PATCH; it saw %v", engine.requests())
	}
	pinned := dueFor(t, engine)
	if pinned.Cadence != 2*time.Hour || pinned.NextDueAt.Equal(before.NextDueAt) {
		t.Errorf("the engine's schedule did not move to the set's 2h: %+v (was %+v)", pinned, before)
	}

	code = 0
	stderr = captureStderr(t, func() {
		_ = captureStdout(t, func() {
			code = run([]string{"backup-set", "assurance", cliSet, "--reset", "--config", cliConfig})
		})
	})
	if code != 0 {
		t.Fatalf("backup-set assurance --reset beside an attached engine exited %d, want 0\nstderr: %s", code, stderr)
	}
	if !slices.Contains(engine.requests(), "DELETE "+setPath) {
		t.Errorf("the engine never received the set's DELETE; it saw %v", engine.requests())
	}
	if back := dueFor(t, engine); back.Cadence != before.Cadence || !back.NextDueAt.Equal(before.NextDueAt) {
		t.Errorf("a reset must return the set to the deployment's schedule: got %+v, want %+v", back, before)
	}
	if readFile(t, cliConfig) != cliBefore {
		t.Error("the CLI wrote its own config.yaml as well as handing the change to the engine")
	}
}

// The no-engine behaviour is unchanged (W-04 test c). Naming an address when
// nothing serves this deployment still writes the file directly: the address
// only matters beside a serving engine.
func TestAssuranceWritesWithNoEngineStillWriteTheFile(t *testing.T) {
	configPath := writeTestConfig(t)
	t.Setenv("RETND_API_URL", "http://127.0.0.1:1")
	if code := run([]string{"settings", "assurance", "patch", "--scrub-cadence", "12h", "--config", configPath}); code != 0 {
		t.Fatalf("settings assurance patch with nothing serving exited %d, want 0", code)
	}
	if code := run([]string{"backup-set", "assurance", cliSet, "--scrub-cadence", "2h", "--config", configPath}); code != 0 {
		t.Fatalf("backup-set assurance patch with nothing serving exited %d, want 0", code)
	}
	raw := readFile(t, configPath)
	for _, want := range []string{"assurance:", "cadence: 12h", "cadence: 2h"} {
		if !containsLine(raw, want) {
			t.Errorf("the direct write left no %q in config.yaml:\n%s", want, raw)
		}
	}
}

// And an engine this command has not been told how to reach is still the
// refusal it was: exit 3, nothing written.
func TestAssuranceWritesBesideAnEngineWithNoAddressAreStillRefused(t *testing.T) {
	cliConfig := writeTestConfig(t)
	attachEngineTo(t, cliConfig)
	before := readFile(t, cliConfig)
	for name, argv := range map[string][]string{
		"settings":   {"settings", "assurance", "patch", "--scrub-cadence", "12h", "--config", cliConfig},
		"backup-set": {"backup-set", "assurance", cliSet, "--scrub-cadence", "2h", "--config", cliConfig},
	} {
		var code int
		stderr := captureStderr(t, func() { code = captureStdoutCode(t, func() int { return run(argv) }) })
		if code != exitEngineHoldsDeployment {
			t.Errorf("%s: exit %d, want %d\n%s", name, code, exitEngineHoldsDeployment, stderr)
		}
	}
	if readFile(t, cliConfig) != before {
		t.Error("a refused assurance write changed config.yaml")
	}
}

func containsLine(haystack, needle string) bool {
	for _, l := range strings.Split(haystack, "\n") {
		if strings.Contains(strings.TrimSpace(l), needle) {
			return true
		}
	}
	return false
}
