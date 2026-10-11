//go:build unix

package app

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
)

// EPIC Q, #978, panel findings C1, C4 and C11. Every case here runs through
// Service.Scrub with NO `revalidation:` block on any set, which is the only
// shape a default install has.

// C1: a copy 90 days old, a default configuration, and the policy resolved to
// weekly. The scheduled scrub must pick it up and read it.
func TestScrub_ADefaultInstallScrubsWithNoRevalidationBlock_C1(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, config.Revalidation{}, bs)
	a := seedMovableArtifact(t, ctx, journal, bs, "one.dump", retentionTestNow.Add(-90*24*time.Hour))

	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	p := localCopyOf(t, journal, a)
	if sum.Selected != 1 || sum.Passed != 1 {
		t.Fatalf("summary = %+v; a default install must select and pass the copy (cadence resolves to %s)", sum, svc.Config.Assurance.Scrub.Cadence)
	}
	if p.AssuranceState != state.AssurancePassed || p.LastCheckedClass != state.VerificationContent || p.ContentVerifiedAt == nil {
		t.Errorf("the copy was not read: state=%s class=%q contentVerifiedAt=%v", p.AssuranceState, p.LastCheckedClass, p.ContentVerifiedAt)
	}
	if p.NextDueAt == nil || p.NextDueAt.Before(retentionTestNow.Add(config.DefaultScrubCadence)) {
		t.Errorf("NextDueAt = %v, want the policy's weekly cadence (7d) from now", p.NextDueAt)
	}
}

// C1: the strategy is part of the resolved policy. `existence` does not
// re-hash a local copy, so it makes no content claim.
func TestScrub_TheExistenceStrategyDoesNotRehashALocalCopy_C1(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	bs.AssuranceConfig = &config.AssuranceOverride{Scrub: config.AssuranceScrub{Strategy: config.ScrubStrategyExistence}}
	svc, journal := scrubService(t, config.Revalidation{}, bs)
	a := seedMovableArtifact(t, ctx, journal, bs, "one.dump", retentionTestNow.Add(-90*24*time.Hour))

	if _, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule}); err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	p := localCopyOf(t, journal, a)
	if p.NextDueAt == nil {
		t.Errorf("strategy existence left the copy unscheduled, so the scrub never looked at it")
	}
	if p.ContentVerifiedAt != nil || p.AssuranceState == state.AssurancePassed {
		t.Errorf("strategy existence made a claim about the bytes: state=%s contentVerifiedAt=%v", p.AssuranceState, p.ContentVerifiedAt)
	}
}

// C1: changing assurance.scrub.cadence changes what is due, with no restart.
func TestScrub_ChangingAssuranceCadenceChangesWhatIsDue_C1(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, config.Revalidation{}, bs)
	a := seedMovableArtifact(t, ctx, journal, bs, "one.dump", retentionTestNow.Add(-48*time.Hour))

	if sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule}); err != nil || sum.Passed != 1 {
		t.Fatalf("first pass = %+v, %v", sum, err)
	}
	svc.Now = fixedNow(retentionTestNow.Add(2 * time.Hour))
	if sum, _ := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule}); sum.Selected != 0 {
		t.Fatalf("under a weekly cadence a copy checked 2h ago selected %d", sum.Selected)
	}

	// The operator patches the cadence from 7d to 1h and the config hot-reloads.
	set := &svc.Config.Sources[0].BackupSets[0]
	set.AssuranceConfig = &config.AssuranceOverride{Scrub: config.AssuranceScrub{Cadence: config.Duration(time.Hour)}}
	if err := svc.Config.ResolveAssurance(); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Selected != 1 {
		t.Errorf("after the cadence became 1h a copy last checked 2h ago selected %d, want 1 (next_due_at=%v)", sum.Selected, localCopyOf(t, journal, a).NextDueAt)
	}
}

// C4: the local copy rots, a content-verified copy sits on a medium. The
// artifact must not be quarantined, and the set must not go FAILING.
func TestScrub_ALocalFailureWithAGoodOffsiteCopyDegradesAndDoesNotQuarantine_C4(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	bs.StaleAfter = config.Duration(30 * 24 * time.Hour)
	svc, journal := scrubService(t, scrubReval, bs)
	a := seedMovableArtifact(t, ctx, journal, bs, "recent.dump", retentionTestNow.Add(-48*time.Hour))
	size := int64(1)
	verified := retentionTestNow.Add(-24 * time.Hour)
	if _, err := journal.RecordTransition(ctx, state.Transition{
		Artifact: a, Key: "second-placement", From: string(lifecycle.Complete), To: string(lifecycle.Complete), OccurredAt: retentionTestNow.Add(-24 * time.Hour),
		Placement: &state.PlacementUpdate{Medium: moveTestMedium, Location: "artifacts/recent.dump", Size: &size, VerificationClass: state.VerificationContent, VerifiedAt: &verified, Status: state.PlacementActive},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bs.LocalPath, "recent.dump"), []byte("rotted"), 0o600); err != nil {
		t.Fatal(err)
	}

	now := retentionTestNow
	healthState := func() health.State {
		rep, err := svc.BuildHealthReport(ctx, VersionInfo{})
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range rep.BackupSets {
			if h.Set == bs.ID {
				return h.State
			}
		}
		t.Fatal("set missing from the health report")
		return ""
	}
	for i := 1; i <= health.ScrubFailureThreshold; i++ {
		svc.Now = fixedNow(now)
		if _, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule}); err != nil {
			t.Fatal(err)
		}
		rec, _ := journal.Get(ctx, a)
		if st := lifecycle.State(rec.State); st != lifecycle.Complete {
			t.Fatalf("pass %d: the local copy failed, an ACTIVE content-verified copy exists on %s, and the artifact is now %s", i, moveTestMedium, st)
		}
		now = now.Add(48 * time.Hour)
	}
	if p := localCopyOf(t, journal, a); p.AssuranceState != state.AssuranceFailed || p.ConsecutiveFailures < health.ScrubFailureThreshold {
		t.Errorf("the rotted copy must still be recorded failed, repeatedly: state=%s failures=%d", p.AssuranceState, p.ConsecutiveFailures)
	}
	if got := healthState(); got != health.Degraded {
		t.Errorf("one failed copy beside a good one reads %s, want DEGRADED (never FAILING)", got)
	}
}

// C4, the other side of the rule: with no other ACTIVE verified copy the
// quarantine still happens.
func TestScrub_ALocalFailureWithNoOtherCopyStillQuarantines_C4(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, scrubReval, bs)
	a := seedMovableArtifact(t, ctx, journal, bs, "only.dump", retentionTestNow.Add(-48*time.Hour))
	if err := os.WriteFile(filepath.Join(bs.LocalPath, "only.dump"), []byte("rotted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule}); err != nil {
		t.Fatal(err)
	}
	rec, _ := journal.Get(ctx, a)
	if st := lifecycle.State(rec.State); st != lifecycle.QuarantinedLost {
		t.Errorf("a rotted only copy left the artifact %s, want QUARANTINED_LOST", st)
	}
}

// C11: a fifo at the copy's path must not hold the tick.
func TestScrub_AFifoAtTheCopyPathDoesNotBlockTheTick_C11(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, scrubReval, bs)
	seedMovableArtifact(t, ctx, journal, bs, "fifo.dump", retentionTestNow.Add(-48*time.Hour))
	path := filepath.Join(bs.LocalPath, "fifo.dump")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("no fifos here: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Scrub is still blocked 5s after it met a fifo at the copy's path")
	}
}
