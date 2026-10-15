package service

import (
	"testing"
	"time"
)

// #1174: the Dashboard tile lists the due, overdue and degraded backups. This
// is the same classification for a terminal (`retnd assurance list`), ported
// from the web client's claim composition (realAssurance.ts) and its
// bucketsOf, so the two cannot name a different set of backups.

var listNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func localCopy(over func(p *Placement)) Placement {
	p := Placement{Medium: "local", Status: "ACTIVE", Access: "immediate"}
	if over != nil {
		over(&p)
	}
	return p
}

func art(id, set string, ps ...Placement) Artifact {
	return Artifact{ID: set + "/" + id, BackupSetID: set, Name: id, Placements: ps}
}

func bucketsFor(t *testing.T, got []AssuranceListing, id string) []string {
	t.Helper()
	for _, g := range got {
		if g.ArtifactID == id {
			return g.Buckets
		}
	}
	t.Fatalf("artifact %s is not in the listing %+v", id, got)
	return nil
}

func has(bs []string, want string) bool {
	for _, b := range bs {
		if b == want {
			return true
		}
	}
	return false
}

func TestListAssuranceBuckets(t *testing.T) {
	ago := func(d time.Duration) time.Time { return listNow.Add(-d) }
	verified := func(p *Placement) { p.VerificationClass = "content"; p.VerifiedAt = ago(3 * time.Hour) }
	artifacts := []Artifact{
		// content verified, recovery passed: in no bucket
		art("fine.zst", "s/a", localCopy(func(p *Placement) { verified(p); p.RecoveryState = "passed"; p.RecoveryTestedAt = ago(time.Hour) })),
		// never scrubbed: due, and (nothing tested) untested
		art("never.zst", "s/a", localCopy(nil)),
		// a failed scrub: degraded
		art("failed-scrub.zst", "s/a", localCopy(func(p *Placement) { p.ScrubState = "failed"; p.ScrubLastFailureAt = ago(time.Hour) })),
		// archive copy that needs a thaw: not readable, but not degraded
		art("archived.zst", "s/b", localCopy(func(p *Placement) { p.Access = "requires_restore"; p.RecoveryState = "passed"; p.RecoveryTestedAt = ago(time.Hour) })),
		// nothing readable at all: degraded
		art("unreachable.zst", "s/b", localCopy(func(p *Placement) { p.Access = "unreachable" })),
		// recovery test failed on a content-verified backup: degraded only
		art("rt-failed.zst", "s/b", localCopy(func(p *Placement) { verified(p); p.RecoveryState = "failed"; p.RecoveryTestedAt = ago(2 * time.Hour) })),
		// the scrub schedule says overdue though a pass is on record
		art("overdue.zst", "s/a", localCopy(func(p *Placement) { verified(p); p.RecoveryState = "readable"; p.RecoveryTestedAt = ago(time.Hour) })),
		// quarantined: failed content
		{ID: "s/a/quarantined.zst", BackupSetID: "s/a", Name: "quarantined.zst", Quarantined: true, Placements: []Placement{localCopy(verified)}},
		// a copy waiting to be deleted does not count
		art("pending.zst", "s/a", Placement{Medium: "local", Status: "DELETE_PENDING"}),
	}
	due := []AssuranceDueItem{
		{ArtifactID: "s/a/overdue.zst", Overdue: true, Due: true, LastAssuredAt: ago(60 * 24 * time.Hour)},
		{ArtifactID: "s/a/never.zst", Overdue: true, Due: true},
	}
	got := ListAssurance(artifacts, due, nil, listNow)

	cases := []struct {
		id   string
		want []string
		not  []string
	}{
		{"s/a/fine.zst", nil, []string{"due", "overdue", "degraded", "untested"}},
		{"s/a/never.zst", []string{"due", "untested"}, []string{"overdue", "degraded"}},
		{"s/a/failed-scrub.zst", []string{"degraded"}, []string{"overdue"}},
		{"s/b/archived.zst", nil, []string{"degraded", "overdue"}},
		{"s/b/unreachable.zst", []string{"degraded"}, nil},
		{"s/b/rt-failed.zst", []string{"degraded"}, []string{"due", "overdue"}},
		{"s/a/overdue.zst", []string{"due", "overdue"}, []string{"degraded", "untested"}},
		{"s/a/quarantined.zst", []string{"degraded"}, nil},
	}
	for _, c := range cases {
		bs := bucketsFor(t, got, c.id)
		for _, w := range c.want {
			if !has(bs, w) {
				t.Errorf("%s: want bucket %q in %v", c.id, w, bs)
			}
		}
		for _, n := range c.not {
			if has(bs, n) {
				t.Errorf("%s: must not be in %q, got %v", c.id, n, bs)
			}
		}
	}
	// A backup whose only copy is waiting to be deleted has no live copy, so
	// its content claim is never_run: due, the same as the web client says.
	if bs := bucketsFor(t, got, "s/a/pending.zst"); !has(bs, "due") {
		t.Errorf("a backup with only a DELETE_PENDING copy is due, got %v", bs)
	}
}

func TestListAssuranceReasonAndAge(t *testing.T) {
	a := art("rt-failed.zst", "s/b", localCopy(func(p *Placement) {
		p.VerificationClass = "content"
		p.VerifiedAt = listNow.Add(-3 * time.Hour)
		p.RecoveryState = "failed"
		p.RecoveryTestedAt = listNow.Add(-26 * time.Hour)
	}))
	got := ListAssurance([]Artifact{a}, nil, nil, listNow)
	if len(got) != 1 {
		t.Fatalf("want one row, got %+v", got)
	}
	if want := "Content: verified 3h ago. Recovery: test failed 26h ago."; got[0].Reason != want {
		t.Errorf("reason = %q, want %q", got[0].Reason, want)
	}
	if got[0].Recovery.State != "failed" || got[0].Content.State != "pass" {
		t.Errorf("claims = %+v / %+v", got[0].Content, got[0].Recovery)
	}
}

func TestListAssuranceRecoveryOffIsNotDue(t *testing.T) {
	a := art("x.zst", "s/off", localCopy(func(p *Placement) { p.VerificationClass = "content"; p.VerifiedAt = listNow.Add(-time.Hour) }))
	got := ListAssurance([]Artifact{a}, nil, map[string]bool{"s/off": true}, listNow)
	bs := bucketsFor(t, got, "s/off/x.zst")
	if has(bs, "due") || !has(bs, "untested") {
		t.Errorf("a set whose recovery tests are off is untested, not due: %v", bs)
	}
}
