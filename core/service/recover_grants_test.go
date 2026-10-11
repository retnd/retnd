package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/app"
)

// EPIC Q panel finding C6 (#976): what spends a download grant, what ends
// it, and who sweeps up when nobody comes back.

func i64(n int64) string { return strconv.FormatInt(n, 10) }

// redeemDownload mints and redeems one download grant on a local copy.
func redeemDownload(t *testing.T, f *recoveryFixture) (token string, dl RecoveryStream) {
	t.Helper()
	ctx := context.Background()
	g, err := f.b.MintRecovery(ctx, f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	dl, _, err = f.b.RedeemRecovery(ctx, f.session(), g.Token)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	return g.Token, dl
}

func serveRange(dl RecoveryStream, start, end int64) {
	dl.Close()
	dl.Served(start, end, end-start+1, nil)
}

func requireGrantStillGood(t *testing.T, f *recoveryFixture, token, why string) {
	t.Helper()
	again, _, err := f.b.RedeemRecovery(context.Background(), f.session(), token)
	if err != nil {
		t.Fatalf("%s: the grant is spent (%v), staged files left: %d", why, err, len(f.stagingEntries(t)))
	}
	again.Close()
}

func requireGrantSpent(t *testing.T, f *recoveryFixture, token, why string) {
	t.Helper()
	again, _, err := f.b.RedeemRecovery(context.Background(), f.session(), token)
	if err == nil {
		again.Close()
		t.Fatalf("%s: the grant is still redeemable", why)
	}
	if re, ok := AsRecoveryError(err); !ok || re.Code == "" {
		t.Fatalf("%s: refusal is not a recovery error: %v", why, err)
	}
	if left := f.stagingEntries(t); len(left) != 0 {
		t.Fatalf("%s: the grant is refused but the staged copy is still on disk: %v", why, left)
	}
}

// A zip reader, or a parallel downloader whose last segment finishes first,
// asks for the tail before the rest. That must not cost it the rest.
func TestATailRangeDoesNotSpendTheGrant(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	token, dl := redeemDownload(t, f)
	size := dl.Length()
	serveRange(dl, size-1000, size-1)
	requireGrantStillGood(t, f, token, "after one tail range of 1000 bytes out of "+i64(size))
}

// The grant is spent once the ranges served add up to the whole artifact,
// in whatever order they arrived and however they overlap, and not before.
func TestRangesThatCoverTheArtifactSpendTheGrant(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	token, dl := redeemDownload(t, f)
	size := dl.Length()
	third := size / 3

	serveRange(dl, size-1000, size-1) // the tail first
	for _, step := range []struct {
		start, end int64
		spent      bool
	}{
		{0, third, false},                 // the head
		{third - 50, 2*third + 50, false}, // the middle, overlapping the head
		{2*third + 40, size - 1000, true}, // closes the last gap, overlapping both sides
	} {
		d, _, err := f.b.RedeemRecovery(context.Background(), f.session(), token)
		if err != nil {
			t.Fatalf("redeem before range %d-%d: %v", step.start, step.end, err)
		}
		serveRange(d, step.start, step.end)
		if step.spent {
			requireGrantSpent(t, f, token, "after the ranges covered every byte")
		} else {
			requireGrantStillGood(t, f, token, "after range "+i64(step.start)+"-"+i64(step.end)+", with a gap still open")
		}
	}
}

// One request for the whole file (no Range header) still finishes the
// grant, which is what a browser's plain download does.
func TestAWholeFileRequestSpendsTheGrant(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	token, dl := redeemDownload(t, f)
	serveRange(dl, 0, dl.Length()-1)
	requireGrantSpent(t, f, token, "after the whole file was served")
}

// A failed request still spends the grant, whatever it covered.
func TestAFailedTailRangeStillSpendsTheGrant(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	token, dl := redeemDownload(t, f)
	size := dl.Length()
	dl.Close()
	dl.Served(size-1000, size-1, 10, errors.New("client went away"))
	requireGrantSpent(t, f, token, "after a failed request")
}

// Nobody comes back: the reaper, not a later request, frees the staged copy.
func TestAbandonedDownloadIsReapedWithoutANewRequest(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	old := recoveryReapEvery
	recoveryReapEvery = 5 * time.Millisecond
	t.Cleanup(func() { recoveryReapEvery = old })

	token, dl := redeemDownload(t, f)
	serveRange(dl, 0, 99) // started, then abandoned
	if len(f.stagingEntries(t)) == 0 {
		t.Fatal("setup: nothing is staged")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.b.runRecoveryReaper(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	f.clock.Advance(10 * time.Minute) // past the grant's idle lifetime
	deadline := time.Now().Add(5 * time.Second)
	for len(f.stagingEntries(t)) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("an abandoned download's staged copy is still on disk with no request to reap it: %v", f.stagingEntries(t))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, err := f.b.RedeemRecovery(context.Background(), f.session(), token); err == nil {
		t.Fatal("the reaped grant is still redeemable")
	}
}

// The reaper is started by the loop that runs under `retnd serve`; a
// reaper nobody starts is the finding.
func TestRunOnScheduleStartsTheRecoveryReaper(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true, pollEvery: time.Hour})
	old := recoveryReapEvery
	recoveryReapEvery = 5 * time.Millisecond
	t.Cleanup(func() { recoveryReapEvery = old })
	withStubbedRunCycle(t, func(*app.Service, context.Context) app.CycleReport { return app.CycleReport{} })
	timers := make(chan *stubTimer, 8)
	withStubbedSchedulerTimer(t, timers)

	_, dl := redeemDownload(t, f)
	serveRange(dl, 0, 99)
	f.clock.Advance(10 * time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- f.b.RunOnSchedule(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case timer := <-timers:
			timer.fire()
		case <-time.After(time.Second):
		}
		<-stopped
	})

	deadline := time.Now().Add(5 * time.Second)
	for len(f.stagingEntries(t)) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("RunOnSchedule never reaped the abandoned download: %v", f.stagingEntries(t))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A logout ends the session's grants and removes what they staged. The
// session handed over is the one the auth adapter reports (actor plus
// expiry), because that pair is the grant's binding.
func TestEndingTheSessionRevokesItsGrantsAndStagedCopy(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	token, dl := redeemDownload(t, f)
	serveRange(dl, 0, 99)
	f.b.EndRecoverySession(f.session())
	requireGrantSpent(t, f, token, "after the session ended")
}
