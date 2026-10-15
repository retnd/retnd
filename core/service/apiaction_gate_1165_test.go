package service

import (
	"context"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/config"
)

// Issue #1165: a refusal by the destructive gate is an audit entry, and it
// reaches both places the product keeps its record of what was done through
// the API: the live activity feed and the log, which is one obs line.
//
// The entry says who, which act, which set or snapshot, the state the gate
// was in, and when (the feed stamps every line, so "when" is the line's own
// time and not a field to forget). These tests drive RecordAPIAction the way
// the web host does and read the feed back, because a field that is set on
// the struct and never printed is not in anybody's audit trail.

func feedFieldsOf(t *testing.T, svc *BackupService, set string) (level, message string, fields map[string]string) {
	t.Helper()
	live, err := svc.LiveActivity(context.Background(), LiveActivityRequest{})
	if err != nil {
		t.Fatalf("LiveActivity: %v", err)
	}
	events := []LiveActivityEvent(nil)
	if set != "" {
		events = setNamed(t, live, set).Events
	} else if live.Deployment != nil {
		events = live.Deployment.Events
	}
	if len(events) != 1 {
		t.Fatalf("the feed holds %d lines, want exactly 1 for one refusal: %v", len(events), eventNamesOf(events))
	}
	fields = map[string]string{}
	for _, f := range events[0].Fields {
		fields[f.Key] = f.Value
	}
	return events[0].Level, events[0].Message, fields
}

func TestRecordAPIAction_AGateRefusalCarriesTheGateStateAndWhatWasRefused(t *testing.T) {
	svc := newRecordingService(t, config.Source{
		Name:       "alpha",
		BackupSets: []config.BackupSet{{Name: "nightly", ID: mustBackupSetID(t, "alpha", "nightly")}},
	})

	svc.RecordAPIAction(context.Background(), cliecho.APIAction{
		Actor: "alice", Method: "POST", Route: "/operations", Status: 403,
		ErrorCode: "DESTRUCTIVE_OPERATIONS_DISABLED", Message: "refused",
		BackupSetID: "alpha/nightly",
		Gate:        "shut", GateReason: "gate shut until #92",
		Operation: "hold_snapshot", RunID: "run-1", SnapshotID: "k1234", HoldID: "hold_9",
		Command: "retnd snapshot hold alpha/nightly --reason x --run run-1",
	})

	level, _, f := feedFieldsOf(t, svc, "alpha/nightly")
	if level != "warn" {
		t.Errorf("level = %q, want warn", level)
	}
	want := map[string]string{
		"actor": "alice", "gate": "shut", "gate_reason": "gate shut until #92",
		"operation": "hold_snapshot", "run_id": "run-1", "snapshot_id": "k1234", "hold_id": "hold_9",
		"error_code": "DESTRUCTIVE_OPERATIONS_DISABLED",
	}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("field %q = %q, want %q (fields: %v)", k, f[k], v, f)
		}
	}
}

// The create whose run was refused is a 201, and a 201 is recorded as a
// success. The entry still has to read as the refusal it is.
func TestRecordAPIAction_ACreateWhoseRunTheGateRefusedIsAWarningNotASuccess(t *testing.T) {
	svc := newRecordingService(t)

	svc.RecordAPIAction(context.Background(), cliecho.APIAction{
		Actor: "alice", Method: "POST", Route: "/backup-sets", Status: 201,
		ErrorCode: "DESTRUCTIVE_OPERATIONS_DISABLED", Message: "run refused",
		Gate: "shut", GateReason: "gate shut until #92", Operation: "run_immediately",
		Command: "retnd backup-set create x",
	})

	level, message, f := feedFieldsOf(t, svc, "")
	if level != "warn" {
		t.Errorf("level = %q, want warn: a refused run that reads as a success is the gap this closes", level)
	}
	if !strings.Contains(message, "refused") {
		t.Errorf("message = %q, want it to say the run was refused", message)
	}
	if f["operation"] != "run_immediately" || f["gate"] != "shut" {
		t.Errorf("fields = %v", f)
	}
}

// An action nothing refused carries no gate fields at all, absent rather than
// empty, so a reader can tell "not refused" from "refused with no reason".
func TestRecordAPIAction_AnOrdinaryActionCarriesNoGateFields(t *testing.T) {
	svc := newRecordingService(t)
	svc.RecordAPIAction(context.Background(), cliecho.APIAction{
		Actor: "alice", Method: "PATCH", Route: "/settings", Status: 200, Command: "retnd settings patch",
	})
	_, _, f := feedFieldsOf(t, svc, "")
	for _, k := range []string{"gate", "gate_reason", "operation", "run_id", "snapshot_id", "hold_id"} {
		if v, present := f[k]; present {
			t.Errorf("an unrefused action carries %q=%q", k, v)
		}
	}
}
