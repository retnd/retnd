package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1154 (EPIC S, audit row 3): `retnd storage`, GET /system/storage from a
// terminal. `status` prints per-set free bytes only; the manager block (cap,
// denominator, headroom, binding constraint, catalog bytes, level) was only
// reachable in the browser.

// storageConfig is the standard fixture with its backup set's directory made
// real, so the reading is a measurement and not an "unknown".
func storageConfig(t *testing.T, extra string) (string, string) {
	t.Helper()
	cfg := writeTestConfig(t)
	local := filepath.Join(filepath.Dir(cfg), "local")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if extra != "" {
		f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(extra); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	return cfg, local
}

func TestIssue1154_StoragePrintsTheManagerBlockAndEveryBackupSetsLevel(t *testing.T) {
	cfg, local := storageConfig(t, "")
	var code int
	out := captureStdout(t, func() { code = run([]string{"storage", "--config", cfg}) })
	if code != 0 {
		t.Fatalf("storage = %d, want 0:\n%s", code, out)
	}
	// Under the API's own names, the way `artifacts <id>` prints the
	// assurance record, so a script and a browser read one vocabulary.
	for _, want := range []string{
		"known:", "measured_path:", "total_bytes:", "available_bytes:", "catalog_bytes:", "other_bytes:",
		"cap_bytes:", "denominator:", "limit_bytes:", "used_bytes:", "headroom_bytes:", "binding_constraint:", "level:",
		"production/postgres-primary", local,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("storage does not print %q:\n%s", want, out)
		}
	}
}

func TestIssue1154_StorageSaysWhichDenominatorABindingCapMakesTheGauge(t *testing.T) {
	cfg, _ := storageConfig(t, "capacity:\n  cap_bytes: 1048576\n")
	var code int
	out := captureStdout(t, func() { code = run([]string{"storage", "--config", cfg}) })
	if code != 0 {
		t.Fatalf("storage = %d:\n%s", code, out)
	}
	if !strings.Contains(out, "cap_bytes: 1048576") {
		t.Errorf("the configured cap is not printed:\n%s", out)
	}
	if !strings.Contains(out, "denominator: cap") {
		t.Errorf("a gauge drawn against a cap does not say so; 80%% of a volume and 80%% of an allowance read the same without it:\n%s", out)
	}
}

func TestIssue1154_StorageJSONIsTheContractsListStorageStatusResponse(t *testing.T) {
	cfg, local := storageConfig(t, "")
	var code int
	out := captureStdout(t, func() { code = run([]string{"storage", "--json", "--config", cfg}) })
	if code != 0 {
		t.Fatalf("storage --json = %d:\n%s", code, out)
	}
	var got apicontract.ListStorageStatusResponse
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("--json is not the contract's ListStorageStatusResponse: %v\n%s", err, out)
	}
	if len(got.BackupSets) != 1 || got.BackupSets[0].BackupSetID != "production/postgres-primary" || got.BackupSets[0].LocalPath != local {
		t.Errorf("--json backup_sets = %+v, want the one configured set at %s", got.BackupSets, local)
	}
	if !got.Manager.Known || got.Manager.MeasuredPath == "" || got.Manager.TotalBytes == 0 {
		t.Errorf("--json manager = %+v, want a measured reading", got.Manager)
	}

	// The same reading the API serves, taken through the service the web
	// host calls. The byte counts move between two reads, so the fields
	// compared are the ones a reading cannot change under you.
	svc, closeFn, err := service.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn() //nolint:errcheck // test teardown
	manager, err := svc.ManagerStorage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := service.StorageWire(manager, nil).Manager
	if got.Manager.MeasuredPath != want.MeasuredPath || got.Manager.Denominator != want.Denominator || got.Manager.Known != want.Known {
		t.Errorf("--json manager (%q, %q, %v) disagrees with the service's (%q, %q, %v)",
			got.Manager.MeasuredPath, got.Manager.Denominator, got.Manager.Known, want.MeasuredPath, want.Denominator, want.Known)
	}
}

func TestIssue1154_StorageJSONCarriesTheConfiguredCapAndItsDenominator(t *testing.T) {
	cfg, _ := storageConfig(t, "capacity:\n  cap_bytes: 1048576\n")
	var code int
	out := captureStdout(t, func() { code = run([]string{"storage", "--json", "--config", cfg}) })
	if code != 0 {
		t.Fatalf("storage --json = %d:\n%s", code, out)
	}
	var got apicontract.ListStorageStatusResponse
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json is not the contract's ListStorageStatusResponse: %v\n%s", err, out)
	}
	if got.Manager.CapBytes != 1048576 || got.Manager.Denominator != "cap" || got.Manager.LimitBytes != 1048576 {
		t.Errorf("--json manager = cap %d, denominator %q, limit %d; want the 1 MiB cap the configuration sets, drawn against the cap",
			got.Manager.CapBytes, got.Manager.Denominator, got.Manager.LimitBytes)
	}
}

func TestIssue1154_StorageTakesNoArgumentAndRefusesFlagsOfOtherForms(t *testing.T) {
	cfg, _ := storageConfig(t, "")
	for name, args := range map[string][]string{
		"an operand":      {"storage", "production/postgres-primary", "--config", cfg},
		"an unknown flag": {"storage", "--backup-set", "production/postgres-primary", "--config", cfg},
	} {
		t.Run(name, func(t *testing.T) {
			var code int
			stderr := captureStderr(t, func() { captureStdout(t, func() { code = run(args) }) })
			if code != exitUsage {
				t.Errorf("%v = %d, want %d", args, code, exitUsage)
			}
			if !strings.Contains(stderr, "storage") || strings.Contains(stderr, "unknown command") {
				t.Errorf("%v was refused as an unknown command rather than as a misuse of `storage`:\n%s", args, stderr)
			}
		})
	}
}

func TestIssue1154_UsageListsTheNewVerbs(t *testing.T) {
	out := captureStderr(t, usage)
	for _, want := range []string{"\n  storage [--json]", "medium configure ", "medium backends "} {
		if !strings.Contains(out, want) {
			t.Errorf("usage() does not list %q, so an operator cannot discover it", strings.TrimSpace(want))
		}
	}
	if _, ok := commands["storage"]; !ok {
		t.Error("`storage` is not registered in the dispatch map")
	}
	if _, ok := mediumVerbs["configure"]; !ok {
		t.Error("`configure` is not a medium verb")
	}
	if _, ok := mediumVerbs["backends"]; !ok {
		t.Error("`backends` is not a medium verb")
	}
}
