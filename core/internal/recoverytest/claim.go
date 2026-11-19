package recoverytest

import (
	"time"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// Claim is a copy's Recovery tested claim, the way a surface should render
// it: one of the state.Recovery* values and, when a test ever reached a
// verdict, how old that verdict is. The five reasons a copy is not a pass
// (never configured, never run, overdue, refused for cost, could not
// complete) are five values, because collapsing any two of them is how an
// unverified copy ends up looking like a verified one.
type Claim struct {
	State    string
	TestedAt *time.Time
	Age      *time.Duration

	// CleanupFailed says the last recovered copy could not be removed. It is
	// separate from State on purpose: one means the claim is unproven, the
	// other means a disk is filling.
	CleanupFailed bool
}

// ClaimOf computes the claim for one copy.
//
// configured says whether a validator is declared for the copy's set NOW. It
// is an argument, not a stored fact, because removing a validator makes the
// claim not configured at once whatever an earlier run recorded, and a
// stored pass must not go on reading as current under a validator that no
// longer exists.
//
// A stored pass past its next-due time reads overdue here as well as in the
// journal (state.MarkRecoveryOverdue), so a surface that renders this does
// not depend on a sweep having run.
func ClaimOf(p state.Placement, configured bool, now time.Time) Claim {
	c := Claim{State: p.RecoveryState, TestedAt: p.RecoveryTestedAt, CleanupFailed: p.RecoveryCleanupState == state.RecoveryCleanupFailed}
	switch {
	case !configured:
		c.State = state.RecoveryNotConfigured
	case c.State == "" || c.State == state.RecoveryUnknown || c.State == state.RecoveryNotConfigured:
		c.State = state.RecoveryNeverRun
	case c.State == state.RecoveryPassed && p.RecoveryNextDueAt != nil && !p.RecoveryNextDueAt.After(now):
		c.State = state.RecoveryOverdue
	}
	if c.TestedAt != nil {
		age := now.Sub(*c.TestedAt)
		c.Age = &age
	}
	return c
}

// KopiaClaim is the recovery-test claim for an engine: kopia set. The
// incremental engine already has a verification ladder and a per-set drill
// cadence (verification_restore_drill_every), and a snapshot run records the
// level it actually achieved. This READS that, and runs nothing: a second
// drill would fork the product's assurance truth into two producers
// disagreeing about one set.
//
// runs are the set's snapshot runs in any order. drillEvery is the
// configured cadence, zero when no drill is configured.
//
// The claim is the newest run that came to rest at SUCCESS having achieved
// restore_drill. No such run reads not configured when no drill cadence is
// set, and never run when one is; a run older than the cadence reads overdue.
// Levels below restore_drill (structural, a content sample, full content) are
// not recovery tests and never count as one.
func KopiaClaim(runs []state.SnapshotRun, drillEvery time.Duration, now time.Time) Claim {
	var best *state.SnapshotRun
	for i := range runs {
		r := &runs[i]
		if r.Phase != state.PhaseSuccess || r.VerificationLevelAchieved != string(model.LevelRestoreDrill) || r.CompletedAt == nil {
			continue
		}
		if best == nil || r.CompletedAt.After(*best.CompletedAt) {
			best = r
		}
	}
	if best == nil {
		if drillEvery <= 0 {
			return Claim{State: state.RecoveryNotConfigured}
		}
		return Claim{State: state.RecoveryNeverRun}
	}
	at := *best.CompletedAt
	age := now.Sub(at)
	c := Claim{State: state.RecoveryPassed, TestedAt: &at, Age: &age}
	if drillEvery > 0 && age >= drillEvery {
		c.State = state.RecoveryOverdue
	}
	return c
}
