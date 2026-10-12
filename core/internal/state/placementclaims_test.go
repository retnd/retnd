package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
)

// EPIC Q, #978, panel findings C5 and C12: a claim is about the bytes it was
// made about, and it dies with them.

// rewriteCopy writes the artifact's local placement again through the same
// door the pipeline uses, a same-state transition carrying a PlacementUpdate.
func rewriteCopy(t *testing.T, j *Journal, a model.ArtifactID, key string, u PlacementUpdate, at time.Time) {
	t.Helper()
	if _, err := j.RecordTransition(context.Background(), Transition{
		Artifact: a, Key: key, From: "COMPLETE", To: "COMPLETE", OccurredAt: at, Placement: &u,
	}); err != nil {
		t.Fatalf("rewrite %s: %v", key, err)
	}
}

func recordPass(t *testing.T, j *Journal, a model.ArtifactID, at time.Time) {
	t.Helper()
	if err := j.RecordCopyScrub(context.Background(), CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationContent,
		At: at, NextDue: at.Add(time.Hour),
	}); err != nil {
		t.Fatalf("RecordCopyScrub: %v", err)
	}
}

// A copy with a content pass behind it is rewritten to different bytes and a
// weaker class. The old pass was made about bytes that are gone.
func TestAPlacementRewrittenToDifferentBytesLosesItsClaims_C5(t *testing.T) {
	j, a := scrubFixture(t)
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	recordPass(t, j, a, at)
	before := localCopy(t, j, a)
	if before.AssuranceState != AssurancePassed || before.ContentVerifiedAt == nil {
		t.Fatalf("precondition: %+v", before.Assurance)
	}

	size := int64(999)
	rewriteCopy(t, j, a, "rewrite-bytes", PlacementUpdate{
		Medium: MediumLocal, Location: before.Location, Size: &size, Hash: "ffff", HashAlg: "sha256",
		VerificationClass: VerificationExistence, Status: PlacementActive,
	}, at.Add(time.Hour))

	after := localCopy(t, j, a)
	if after.Hash != "ffff" {
		t.Fatalf("the rewrite did not land: hash %q", after.Hash)
	}
	if after.AssuranceState != AssuranceUnknown || after.ContentVerifiedAt != nil || after.LastCheckedAt != nil || after.NextDueAt != nil {
		t.Errorf("the copy holds different bytes yet still claims: state=%s contentVerifiedAt=%v lastChecked=%v nextDue=%v",
			after.AssuranceState, after.ContentVerifiedAt, after.LastCheckedAt, after.NextDueAt)
	}
}

func TestAPlacementMovedToAnotherLocationLosesItsClaims_C5(t *testing.T) {
	j, a := scrubFixture(t)
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	recordPass(t, j, a, at)
	before := localCopy(t, j, a)

	rewriteCopy(t, j, a, "move-location", PlacementUpdate{
		Medium: MediumLocal, Location: before.Location + ".moved", Size: before.Size, Hash: before.Hash, HashAlg: before.HashAlg,
		VerificationClass: before.VerificationClass, Status: PlacementActive,
	}, at.Add(time.Hour))

	after := localCopy(t, j, a)
	if after.AssuranceState != AssuranceUnknown || after.ContentVerifiedAt != nil {
		t.Errorf("a copy at a new location kept the claim made about the old one: state=%s contentVerifiedAt=%v", after.AssuranceState, after.ContentVerifiedAt)
	}
}

// The same bytes written again (a resumed upload) keep what was proven.
func TestTheSameBytesWrittenAgainKeepTheirClaims_C5(t *testing.T) {
	j, a := scrubFixture(t)
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	recordPass(t, j, a, at)
	before := localCopy(t, j, a)

	rewriteCopy(t, j, a, "same-bytes", PlacementUpdate{
		Medium: MediumLocal, Location: before.Location, Size: before.Size, Hash: before.Hash, HashAlg: before.HashAlg,
		VerificationClass: before.VerificationClass, VerifiedAt: before.VerifiedAt, Status: PlacementActive,
	}, at.Add(time.Hour))

	after := localCopy(t, j, a)
	if after.AssuranceState != AssurancePassed || after.ContentVerifiedAt == nil || !after.ContentVerifiedAt.Equal(*before.ContentVerifiedAt) {
		t.Errorf("rewriting identical bytes dropped a true claim: %+v", after.Assurance)
	}
}

// Mutation M8b: the second door to "Content verified". An existence write
// for the same bytes must not move the content age, whatever it is labelled.
func TestUpsertOfAnExistenceClassCannotMoveTheContentAge_C5(t *testing.T) {
	j, a := scrubFixture(t)
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(48 * time.Hour)
	p := localCopy(t, j, a)

	rewriteCopy(t, j, a, "content-write", PlacementUpdate{
		Medium: MediumLocal, Location: p.Location, Size: p.Size, Hash: p.Hash, HashAlg: p.HashAlg,
		VerificationClass: VerificationContent, VerifiedAt: &t0, Status: PlacementActive,
	}, t0)
	if got := localCopy(t, j, a).ContentVerifiedAt; got == nil || !got.Equal(t0) {
		t.Fatalf("precondition: a content write must set the content age, got %v", got)
	}

	rewriteCopy(t, j, a, "existence-write", PlacementUpdate{
		Medium: MediumLocal, Location: p.Location, Size: p.Size, Hash: p.Hash, HashAlg: p.HashAlg,
		VerificationClass: VerificationExistence, VerifiedAt: &t1, Status: PlacementActive,
	}, t1)
	got := localCopy(t, j, a).ContentVerifiedAt
	if got == nil || !got.Equal(t0) {
		t.Errorf("an existence write moved the content age: got %v, want it to stay %v", got, t0)
	}
}

// A verdict about bytes the copy no longer holds is refused.
func TestRecordCopyScrubRefusesAVerdictForDifferentBytes_C5(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	read := localCopy(t, j, a)

	// The bytes change after the check read them and before it records.
	size := int64(7)
	rewriteCopy(t, j, a, "changed-under-the-check", PlacementUpdate{
		Medium: MediumLocal, Location: read.Location, Size: &size, Hash: "eeee", HashAlg: "sha256",
		VerificationClass: VerificationExistence, Status: PlacementActive,
	}, at)

	id := read.Identity()
	for _, v := range []ScrubVerdict{ScrubPassed, ScrubFailed} {
		err := j.RecordCopyScrub(ctx, CopyScrub{
			Artifact: a, Medium: MediumLocal, Verdict: v, Class: VerificationContent,
			Expect: &id, At: at, NextDue: at.Add(time.Hour),
		})
		if err == nil || !errorsIs(err, ErrCopyChanged) {
			t.Errorf("%s verdict for old bytes: err = %v, want ErrCopyChanged", v, err)
		}
	}
	if got := localCopy(t, j, a); got.AssuranceState != AssuranceUnknown {
		t.Errorf("a refused verdict still landed: state=%s", got.AssuranceState)
	}

	// The same call with the identity it really holds is accepted.
	cur := localCopy(t, j, a).Identity()
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationContent,
		Expect: &cur, At: at, NextDue: at.Add(time.Hour),
	}); err != nil {
		t.Errorf("a verdict for the bytes the copy holds was refused: %v", err)
	}
}

func errorsIs(err, target error) bool { return errors.Is(err, target) }
