package service

import (
	"context"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// EPIC Q, #978, panel findings C1 and C10: the default deployment, stood up
// the way production stands it up (service.Open over a YAML file with no
// `revalidation:` block anywhere), runs a real cycle and then a scheduled
// scrub tick, and the tick reads the copy.

func journalOf(t *testing.T, svc *BackupService) *state.Journal {
	t.Helper()
	j, ok := svc.state.Load().inner.Journal.(*state.Journal)
	if !ok {
		t.Fatalf("the service's journal is %T, want *state.Journal", svc.state.Load().inner.Journal)
	}
	return j
}

func openWithACompleteArtifact(t *testing.T) (*BackupService, model.ArtifactID) {
	t.Helper()
	svc, cleanup, err := Open(context.Background(), writeTestConfigFile(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })
	op, err := svc.SubmitRunCycle(context.Background(), RunCycleRequest{IdempotencyKey: "scrub-wiring", Actor: "alice", ConfigRevision: svc.ConfigRevision()})
	if err != nil {
		t.Fatalf("SubmitRunCycle: %v", err)
	}
	if done := waitForTerminalStatus(t, svc, op.ID); done.Status != "completed" {
		t.Fatalf("cycle ended %q: %s", done.Status, done.Error)
	}
	arts, err := svc.ListArtifacts(context.Background(), ArtifactFilter{})
	if err != nil || len(arts) != 1 {
		t.Fatalf("artifacts = %d, %v; want the one the cycle produced", len(arts), err)
	}
	set, err := model.NewBackupSetID(arts[0].SourceName, arts[0].SetName)
	if err != nil {
		t.Fatalf("NewBackupSetID: %v", err)
	}
	id, err := model.NewArtifactID(set, arts[0].Name)
	if err != nil {
		t.Fatalf("NewArtifactID: %v", err)
	}
	return svc, id
}

func TestDefaultConfigSchedulesAScrub_C1(t *testing.T) {
	ctx := context.Background()
	svc, id := openWithACompleteArtifact(t)
	j := journalOf(t, svc)

	rec, err := j.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := rec.LocalPlacement(); !ok || p.AssuranceState != state.AssuranceUnknown {
		t.Fatalf("precondition: a freshly ingested copy must be unscrubbed, got %+v ok=%v", p.Assurance, ok)
	}

	svc.tickScrub(ctx)

	rec, err = j.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := rec.LocalPlacement()
	if p.AssuranceState != state.AssurancePassed || p.LastCheckedClass != state.VerificationContent || p.ContentVerifiedAt == nil {
		t.Fatalf("a scrub tick on a default install did not read the copy: state=%s class=%q contentVerifiedAt=%v", p.AssuranceState, p.LastCheckedClass, p.ContentVerifiedAt)
	}
	if p.NextDueAt == nil || p.NextDueAt.Before(time.Now().Add(6*24*time.Hour)) {
		t.Errorf("NextDueAt = %v, want about the default weekly cadence out", p.NextDueAt)
	}
}

// What /assurance/due tells the operator and what the driver does must be one
// clock: the copy's own next_due_at.
func TestAssuranceDue_AgreesWithTheCopiesNextDueAt_C1(t *testing.T) {
	ctx := context.Background()
	svc, id := openWithACompleteArtifact(t)
	j := journalOf(t, svc)

	before, err := svc.AssuranceDue(ctx, AssuranceDueFilter{})
	if err != nil || len(before) != 1 {
		t.Fatalf("AssuranceDue = %d items, %v", len(before), err)
	}
	if !before[0].Due || before[0].Basis != DueBasisNever {
		t.Errorf("an unscrubbed copy must be due with basis never, got %+v", before[0])
	}

	svc.tickScrub(ctx)
	rec, _ := j.Get(ctx, id)
	p, _ := rec.LocalPlacement()
	if p.NextDueAt == nil {
		t.Fatal("the scrub left no next_due_at")
	}

	after, err := svc.AssuranceDue(ctx, AssuranceDueFilter{})
	if err != nil || len(after) != 1 {
		t.Fatalf("AssuranceDue = %d items, %v", len(after), err)
	}
	got := after[0]
	if !got.NextDueAt.Equal(*p.NextDueAt) {
		t.Errorf("AssuranceDue says next due %s, the copy says %s: two clocks", got.NextDueAt, *p.NextDueAt)
	}
	if got.Due || got.LastAssuredAt.IsZero() {
		t.Errorf("a copy scrubbed a moment ago is not due and has a last-assured time: %+v", got)
	}
}
