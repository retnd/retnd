package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// Issue #1146. A set that stopped for a reason a person must act on has to
// look stopped from the terminal. halt_reason was on the report and the API
// and never on `status`, so a refused set read like a slow one until it
// went STALE.

var haltFixtureAt = time.Date(2026, 10, 12, 4, 12, 8, 0, time.UTC)

// stageHalt records a standing connection refusal against the fixture's
// set, through the real journal, the way a cycle that was refused leaves it.
func stageHalt(t *testing.T, configPath, reason string) {
	t.Helper()
	j, err := state.Open(context.Background(), filepath.Join(filepath.Dir(configPath), "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer func() { _ = j.Close() }()
	set, err := model.NewBackupSetID("production", "postgres-primary")
	if err != nil {
		t.Fatalf("NewBackupSetID: %v", err)
	}
	if err := j.RecordBackupSetHalt(context.Background(), set, reason, haltFixtureAt); err != nil {
		t.Fatalf("RecordBackupSetHalt: %v", err)
	}
}

func TestRun_Status_PrintsWhyASetIsHalted_1146(t *testing.T) {
	cases := []struct {
		reason string
		hint   string
	}{
		{state.HaltHostKeyChanged, "--trust-host-key --acknowledge-host-key-change"},
		{state.HaltAuthenticationFailed, "credential"},
		{state.HaltKeyPermissions, "key_file"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			configPath := writeTestConfig(t)
			stageHalt(t, configPath, tc.reason)

			out := captureStdout(t, func() {
				run([]string{"status", "--config", configPath})
			})
			want := "  halted: " + tc.reason + " (last refused 2026-10-12T04:12:08Z)"
			if !strings.Contains(out, want) {
				t.Errorf("status has no %q line:\n%s", want, out)
			}
			if !strings.Contains(out, tc.hint) {
				t.Errorf("status names no fix (%q) for %s:\n%s", tc.hint, tc.reason, out)
			}
			// The line sits under the set it is about, not at the foot.
			set := strings.Index(out, "production/postgres-primary:")
			halted := strings.Index(out, "  halted:")
			if set < 0 || halted < set {
				t.Errorf("the halted line is not under its set:\n%s", out)
			}
		})
	}
}

func TestRun_Status_SaysNothingOfAHaltWhenNoneIsOnRecord_1146(t *testing.T) {
	configPath := writeTestConfig(t)
	out := captureStdout(t, func() { run([]string{"status", "--config", configPath}) })
	if strings.Contains(out, "halted") {
		t.Errorf("status claims a halt that nothing recorded:\n%s", out)
	}
}

// statusJSON is what `status --json` emits: HealthResponse plus process.
type statusJSON struct {
	apicontract.HealthResponse
	Process struct {
		BinaryVersion string `json:"binary_version"`
		RcloneVersion string `json:"rclone_version"`
	} `json:"process"`
}

func TestRun_Status_JSONIsTheHealthWireObject_1146(t *testing.T) {
	configPath := writeTestConfig(t)
	stageHalt(t, configPath, state.HaltHostKeyChanged)

	var code int
	out := captureStdout(t, func() { code = run([]string{"status", "--json", "--config", configPath}) })
	if code != 1 {
		t.Errorf("status --json exit = %d, want 1 (a set that is not HEALTHY keeps the exit convention)", code)
	}

	// Strict: a key the contract does not have is a key an API client
	// cannot read.
	var got statusJSON
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not one HealthResponse object plus process: %v\n%s", err, out)
	}
	if dec.More() {
		t.Errorf("stdout holds more than one JSON value:\n%s", out)
	}
	if got.GeneratedAt == "" {
		t.Errorf("generated_at is empty:\n%s", out)
	}
	if got.Process.BinaryVersion == "" {
		t.Errorf("process.binary_version is empty:\n%s", out)
	}
	if len(got.BackupSets) != 1 {
		t.Fatalf("backup_sets = %d, want 1:\n%s", len(got.BackupSets), out)
	}
	bs := got.BackupSets[0]
	if bs.BackupSetID != "production/postgres-primary" || bs.HaltReason != state.HaltHostKeyChanged {
		t.Errorf("backup set = %q halt_reason = %q, want production/postgres-primary / HOST_KEY_CHANGED", bs.BackupSetID, bs.HaltReason)
	}
	// A value, not just a key: a field copied as its zero value is the
	// quiet way for the two surfaces to drift.
	if bs.StaleAfterSeconds != 24*60*60 || bs.State != "DEGRADED" || bs.Reason == "" || bs.SetName != "postgres-primary" {
		t.Errorf("stale_after_seconds = %d, state = %q, reason = %q, set_name = %q; want 86400, DEGRADED, a sentence, postgres-primary", bs.StaleAfterSeconds, bs.State, bs.Reason, bs.SetName)
	}
}

// Every field the API always sends is a key the CLI always sends. This is
// the field-for-field half of the done criterion: a field dropped from the
// CLI's encoding is a key a script reading either surface would miss.
func TestRun_Status_JSONCarriesEveryAlwaysPresentWireField_1146(t *testing.T) {
	configPath := writeTestConfig(t)
	var out string
	out = captureStdout(t, func() { run([]string{"status", "--json", "--config", configPath}) })

	var raw struct {
		BackupSets []map[string]json.RawMessage `json:"backup_sets"`
	}
	if err := json.NewDecoder(bytes.NewReader([]byte(out))).Decode(&raw); err != nil || len(raw.BackupSets) != 1 {
		t.Fatalf("no backup_sets in:\n%s (%v)", out, err)
	}
	typ := reflect.TypeOf(apicontract.BackupSetHealth{})
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" || strings.Contains(opts, "omitempty") {
			continue
		}
		if _, ok := raw.BackupSets[0][name]; !ok {
			t.Errorf("status --json omits %q, which the API always sends", name)
		}
	}
}

func TestRun_Status_JSONWithNoHaltOmitsHaltReason_1146(t *testing.T) {
	configPath := writeTestConfig(t)
	out := captureStdout(t, func() { run([]string{"status", "--json", "--config", configPath}) })
	if strings.Contains(out, "halt_reason") {
		t.Errorf("halt_reason is present with nothing on record (absent means unobserved, never empty):\n%s", out)
	}
}
