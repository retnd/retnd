package revalidate

import (
	"sort"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
)

// This file is the scheduling half of revalidation: which artifacts are due
// for another look, and in what order.
//
// It is separated from the checking half because the two have opposite
// natures. Checking touches the filesystem, runs an operator's hook and
// talks to a bucket; scheduling is a pure function of the records, the
// configuration and one instant. Keeping it pure is what lets the ordering
// and the bound be tested exhaustively without a fixture, and it is the
// same split internal/retention makes between GFSDecide and the code that
// acts on its answer.
//
// The two policy decisions live here and nowhere else: which states are
// worth re-checking at all, and what "due" means. Both are stated as data
// or as one comparison rather than being spread through the pass, so a
// future state added to the machine is a one-line question in this file
// rather than a behaviour nobody notices is missing.

// eligibleStates are the lifecycle states worth re-checking: a durable
// local final file has to actually exist for there to be anything to
// re-read. This is the same four-state set FR-19's last-known-good
// protection and internal/health's FR-24 computation already call known
// good. This package does not import either of those (health owns FR-24,
// retention owns FR-19, and this package's selection policy is allowed to
// agree with them by definition, both being grounded in the same "durable
// local copy exists" fact, rather than by a shared dependency neither of
// them exports for this purpose).
//
// RemoteRetained (issue #282) was missing here until issue #315: a
// read-only backup set's artifacts have exactly as durable a local final
// copy as a COMMITTED one, and Phase 4's whole reason to exist, bit rot
// does not announce itself, applies to them no less. Before this fix
// Phase 4 simply never looked at them again once retained, which is the
// gap issue #315 closes: a corrupted local copy for a read-only source is
// often undetectable any other way, since this manager never re-examines
// the remote either.
var eligibleStates = map[lifecycle.State]bool{
	lifecycle.Committed:           true,
	lifecycle.RemoteDeletePending: true,
	lifecycle.RemoteRetained:      true,
	lifecycle.Complete:            true,
}

// ScrubTargets returns the copies a scrub of rec looks at: its ACTIVE local
// copy when it has one, and otherwise every ACTIVE copy on a storage medium.
// A record with no placement rows at all returns nil, which the caller reads
// as a legacy row to adopt before anything is recorded against it.
//
// This is the same fork runChecks makes, and it is written once here so the
// answer to "what is due" and the answer to "what gets checked" cannot
// drift: a copy that selection counted and the check then skipped would be
// selected again every cycle without ever being looked at.
func ScrubTargets(rec state.Record) []state.Placement {
	if p, ok := rec.LocalPlacement(); ok {
		return []state.Placement{p}
	}
	return rec.ActiveMediumPlacements()
}

// dueKey is when rec's earliest-due target became due, and whether it is due
// at all at now. A target with no recorded due time is due, and sorts before
// every copy that has one, because "never scheduled" is more overdue than
// any date: it is the state a rebuilt catalog and an upgraded one leave
// every copy in.
//
// A record with no placement rows at all is due, for the same reason. It is
// a row the migration backfill or a rebuild never gave a copy, and what the
// driver does about it is adopt it, not skip it.
func dueKey(rec state.Record, now time.Time) (time.Time, bool) {
	targets := ScrubTargets(rec)
	if len(targets) == 0 {
		return time.Time{}, true
	}
	var (
		earliest time.Time
		any      bool
		due      bool
	)
	for _, p := range targets {
		if !p.DueAt(now) {
			continue
		}
		due = true
		var at time.Time
		if p.NextDueAt != nil {
			at = *p.NextDueAt
		}
		if !any || at.Before(earliest) {
			earliest, any = at, true
		}
	}
	return earliest, due
}

// SelectDue is the pure half of scheduling: given records, cfg and now, it
// returns which artifacts are due for a fresh check, in the order they
// should be checked in, bounded to at most cfg.MaxPerCycle. It does no I/O
// and reaches for no clock of its own, the same discipline
// retention.GFSDecide already follows for the same reason: the same inputs
// must always produce the same answer.
//
// # Due-ness is a property of the COPY
//
// An artifact is due when its current state is one of eligibleStates and at
// least one of the copies a scrub would check (ScrubTargets) has a
// next_due_at that has arrived, or none at all. It used to compare
// now.Sub(rec.UpdatedAt) against the ARTIFACT row, and that row is touched
// by FR-17 reconciliation at startup and rewritten by a catalog rebuild, so
// a copy nobody re-read could look freshly verified. Nothing on the
// artifact row is consulted any more, which is asserted by rewriting
// updated_at and checking that nothing about what is due moves.
//
// cfg.MaxPerCycle <= 0 always returns nil, regardless of how many records
// are otherwise due: see config.Validate's own rule that this is the only
// way MaxPerCycle can be non-positive in a validated config, which is this
// package's way of spelling "revalidation is disabled for this backup set".
//
// The returned slice is sorted most-overdue-first (never-scheduled copies
// before any dated one), so the artifacts actually spent within a bounded
// MaxPerCycle are the ones that have waited longest, tie-broken by the
// artifact's own string form so two calls over identical input always pick
// the same subset in the same order (see PR#60's own reasoning for why this
// project treats that kind of determinism as load-bearing, not cosmetic).
func SelectDue(records []state.Record, cfg config.Revalidation, now time.Time) []state.Record {
	if cfg.MaxPerCycle <= 0 {
		return nil
	}

	type candidate struct {
		rec state.Record
		key time.Time
	}
	var due []candidate
	for _, rec := range records {
		if !eligibleStates[lifecycle.State(rec.State)] {
			continue
		}
		if key, ok := dueKey(rec, now); ok {
			due = append(due, candidate{rec, key})
		}
	}

	sort.SliceStable(due, func(i, j int) bool {
		if !due[i].key.Equal(due[j].key) {
			return due[i].key.Before(due[j].key)
		}
		return due[i].rec.Artifact.String() < due[j].rec.Artifact.String()
	})

	if len(due) > cfg.MaxPerCycle {
		due = due[:cfg.MaxPerCycle]
	}
	out := make([]state.Record, len(due))
	for i, c := range due {
		out[i] = c.rec
	}
	return out
}

// SelectEligible returns every record a scrub may look at regardless of
// due-ness or the per-cycle bound, in the same deterministic order. It is
// for an operator who asked for specific copies; they have said now.
func SelectEligible(records []state.Record) []state.Record {
	var out []state.Record
	for _, rec := range records {
		if eligibleStates[lifecycle.State(rec.State)] {
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Artifact.String() < out[j].Artifact.String() })
	return out
}
