package service

import (
	"context"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/recovery/budget"
)

// EPIC Q panel finding C6 (#976), seen from the budgets: a download the
// client walked away from holds a slot against the download ceiling. The
// timer, not a later request, has to give it back, or on a quiet deployment
// the ceiling stays taken for days.
func TestTheReaperGivesBackAnAbandonedDownloadsStreamSlot(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	// A one byte reserve: the default is 1 GiB of free space, which a small
	// scratch disk does not have, and this test is about the slot.
	f.b.recovery.limits = budget.Limits{MaxDownloadStreams: 1, StagingReserveBytes: 1}
	sess := f.session()
	g, err := f.b.MintRecovery(ctx, sess, RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}})
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := f.b.RedeemRecovery(ctx, sess, g.Token)
	if err != nil {
		t.Fatal(err)
	}
	d.Close() // the client went away and never came back
	h := f.b.recoveryHost()
	if h.streams.InFlight() != 1 {
		t.Fatalf("setup: %d stream slots held, want 1", h.streams.InFlight())
	}

	f.clock.Advance(10 * time.Minute)
	f.b.reapRecoveryOnce(ctx) // what the timer runs; no request involved
	if n := h.streams.InFlight(); n != 0 {
		t.Fatalf("an abandoned download kept its slot after the reaper ran (%d held)", n)
	}
	stagingIsEmpty(t, f)
}
