package service

import (
	"context"
	"testing"
	"time"

	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/config"
)

// Issue #1164: releasing a hold lifts a protection, so the feed has to say
// who released which hold, on which set, and when. A successful release is
// not a gate refusal, and the line is still the audit entry for it.
func TestRecordAPIAction_AReleasedHoldNamesWhoWhichHoldWhichSetAndWhen(t *testing.T) {
	svc := newRecordingService(t, config.Source{
		Name:       "alpha",
		BackupSets: []config.BackupSet{{Name: "nightly", ID: mustBackupSetID(t, "alpha", "nightly")}},
	})

	before := time.Now().Add(-time.Second)
	svc.RecordAPIAction(context.Background(), cliecho.APIAction{
		Actor: "alice", Method: "POST", Route: "/operations", Status: 202,
		BackupSetID: "alpha/nightly",
		Operation:   "release_snapshot_hold", HoldID: "hold_9",
		Command: "retnd snapshot unhold alpha/nightly hold_9",
	})
	after := time.Now().Add(time.Second)

	live, err := svc.LiveActivity(context.Background(), LiveActivityRequest{})
	if err != nil {
		t.Fatalf("LiveActivity: %v", err)
	}
	events := setNamed(t, live, "alpha/nightly").Events
	if len(events) != 1 {
		t.Fatalf("the feed holds %d lines for one release, want 1", len(events))
	}
	e := events[0]
	fields := map[string]string{}
	for _, f := range e.Fields {
		fields[f.Key] = f.Value
	}
	if fields["actor"] != "alice" || fields["operation"] != "release_snapshot_hold" || fields["hold_id"] != "hold_9" {
		t.Errorf("the release line does not name who and which: %v", fields)
	}
	if _, refused := fields["gate"]; refused {
		t.Errorf("a served release carries a gate state: %v", fields)
	}
	if e.At.Before(before) || e.At.After(after) {
		t.Errorf("the line is stamped %v, want a time inside the call (%v to %v): the feed is the record of when", e.At, before, after)
	}
}
