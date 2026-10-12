package service

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/state"
)

// The audit of #1069 found that nothing tested a cycle that holds the
// single-flight lock for LONGER than the scrub retry interval. The test above
// it (TestAScrubTickThatFindsTheLockHeldRunsAsSoonAsItIsFree) uses a cycle that
// holds the lock for 70ms of a 100ms interval, so the first retry already finds
// it free. A real backup holds the lock for many retries, and what matters
// then is that the tick keeps waiting, does not pile up, does not start while
// the lock is held, and runs the pass exactly once when the lock frees.
//
// These tests drive tickScrubWhenFree on a fake clock (scrubRetryTimer), so the
// "cycle" is many retries long in virtual time and the test takes milliseconds.

// retryClock hands the test control of every retry wait: each wait the tick
// asks for arrives on armed, and the test lets it elapse by sending on fire.
type retryClock struct {
	armed chan time.Duration
	fire  chan time.Time
}

func installRetryClock(t *testing.T) *retryClock {
	t.Helper()
	c := &retryClock{armed: make(chan time.Duration, 64), fire: make(chan time.Time, 1)}
	orig := scrubRetryTimer
	t.Cleanup(func() { scrubRetryTimer = orig })
	scrubRetryTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
		c.armed <- d
		return c.fire, func() bool { return true }
	}
	return c
}

// next waits for the tick to ask for its next retry wait.
func (c *retryClock) next(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-c.armed:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("the scrub tick never asked for another retry: it gave up, or ran its pass, while the cycle still held the lock")
		return 0
	}
}

func (c *retryClock) elapse() { c.fire <- time.Time{} }

// lockProbeStages is a validator that records, at the moment it starts, whether
// the cycle was holding the single-flight lock. A recovery test runs from the
// same tick as the scrub, so its start is the pass's start.
type lockProbeStages struct {
	*stubStages
	b    *BackupService
	held *atomic.Bool // the fake cycle's own flag

	mu     sync.Mutex
	starts []bool // for each validator start: was the lock held by someone
}

func (s *lockProbeStages) RunStage(ctx context.Context, req app.StageRequest) (app.StageResult, error) {
	if !req.DryRun && !strings.HasPrefix(req.SetDir, "assurance-after") {
		locked := !s.b.runOnce.TryLock()
		if !locked {
			s.b.runOnce.Unlock()
		}
		s.mu.Lock()
		s.starts = append(s.starts, locked || s.held.Load())
		s.mu.Unlock()
	}
	return s.stubStages.RunStage(ctx, req)
}

func (s *lockProbeStages) startsSeen() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.starts...)
}

func newSlowCycleFixture(t *testing.T) (f *recoveryFixture, probe *lockProbeStages) {
	t.Helper()
	f = newRecoveryFixture(t, fixtureOpts{pollEvery: 100 * time.Millisecond, cfg: requireRecoveryTest})
	if err := f.b.state.Load().inner.Config.ResolveAssurance(); err != nil {
		t.Fatal(err)
	}
	requireRecoveryTest(f.b.state.Load().inner.Config)
	probe = &lockProbeStages{stubStages: &stubStages{script: "pg-check.local.sh"}, b: f.b, held: new(atomic.Bool)}
	f.b.state.Load().inner.Stages = probe
	seedComplete(t, f, "slow.dump", state.MediumLocal)
	return f, probe
}

// A cycle that holds the lock for twenty retries: the tick waits it out one
// retry at a time, runs nothing while the lock is held, never has more than one
// wait pending, and runs the pass exactly once, after the lock is released.
func TestAScrubTickBehindACycleLongerThanTheRetryRunsOnceWhenTheLockFrees(t *testing.T) {
	f, probe := newSlowCycleFixture(t)
	clock := installRetryClock(t)
	const interval = 100 * time.Millisecond
	const cycleRetries = 20 // the cycle outlasts twenty retries (two intervals)

	// The cycle takes the lock before the tick arrives, as the cycle loop does.
	f.b.runOnce.Lock()
	probe.held.Store(true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		f.b.tickScrubWhenFree(context.Background(), interval)
	}()

	want := scrubBusyRetry(interval)
	for i := 0; i < cycleRetries; i++ {
		if got := clock.next(t); got != want {
			t.Fatalf("retry %d waited %v, want %v (the retry must not grow or shrink while the lock is held)", i, got, want)
		}
		if n := len(probe.startsSeen()); n != 0 {
			t.Fatalf("the pass started %d time(s) while the cycle still held the lock", n)
		}
		select {
		case <-done:
			t.Fatalf("the tick gave up after %d retries with the cycle still running", i)
		default:
		}
		// Exactly one wait is pending: the retry is sequential, so a long
		// cycle cannot stack ticks behind it.
		if n := len(clock.armed); n != 0 {
			t.Fatalf("%d extra waits were armed while one was pending", n)
		}
		if i < cycleRetries-1 {
			clock.elapse()
		}
	}

	// The cycle ends. The tick is waiting on its last retry, which now fires.
	probe.held.Store(false)
	f.b.runOnce.Unlock()
	clock.elapse()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the lock was released and the tick did not run its pass")
	}
	starts := probe.startsSeen()
	if len(starts) != 1 {
		t.Fatalf("the due pass ran %d times, want exactly once", len(starts))
	}
	if starts[0] {
		t.Error("the pass started while the lock was held: it ran concurrently with the cycle")
	}
	if len(clock.armed) != 0 {
		t.Error("the tick armed another wait after it had run: a finished tick must wait for the next interval, not retry")
	}
}

// The same tick must stop when the process does, even mid-wait behind a cycle
// that never ends.
func TestAScrubTickBehindACycleThatNeverEndsStopsWithItsContext(t *testing.T) {
	f, probe := newSlowCycleFixture(t)
	clock := installRetryClock(t)

	f.b.runOnce.Lock()
	defer f.b.runOnce.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.b.tickScrubWhenFree(ctx, 100*time.Millisecond)
	}()
	clock.next(t)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the tick kept waiting after its context was cancelled")
	}
	if n := len(probe.startsSeen()); n != 0 {
		t.Errorf("the pass ran %d times behind a held lock", n)
	}
}

// The retry's bounds, so a change to either end is a decision and not an
// accident. The interval itself is never below config.MinPollInterval (one
// minute) in a real deployment, which makes the real range 6s to 30s.
func TestScrubBusyRetryBounds(t *testing.T) {
	for _, tc := range []struct {
		interval, want time.Duration
	}{
		{0, time.Millisecond},
		{time.Nanosecond, time.Millisecond},
		{5 * time.Millisecond, time.Millisecond},
		{100 * time.Millisecond, 10 * time.Millisecond},
		{time.Minute, 6 * time.Second},
		{5 * time.Minute, 30 * time.Second},
		{15 * time.Minute, 30 * time.Second},
		{24 * time.Hour, 30 * time.Second},
	} {
		if got := scrubBusyRetry(tc.interval); got != tc.want {
			t.Errorf("scrubBusyRetry(%v) = %v, want %v", tc.interval, got, tc.want)
		}
	}
}

// KNOWN GAP, pinned as it is today (not as it should be): the retry polls, so
// it only catches a lock that is free at the instant of a poll. The cycle loop
// frees the lock for as little as minPollWake (5s) between back-to-back cycles
// when a set is overdue, and the retry period is 6s at the minimum one-minute
// interval and 30s at the default fifteen. A cycle of 55s with its 5s gap
// against the 6s retry is a period of 60s that the 6s polls never land in the
// gap of, so the scrub waits for as long as the pattern lasts.
//
// The loop below is the deterministic version of that: the virtual clock
// advances one retry per fire, the lock is free for the last 5s of every 60s,
// and ten virtual days of polls never see it free. In a real process the
// phase drifts, so the miss is probabilistic (roughly gap/retry chance per
// cycle of catching it: 5 in 6 at a one-minute interval, 1 in 6 at the
// default), but a configuration whose cycles always overrun its interval
// waits several cycles for a pass that is due now.
//
// When the retry stops polling (waits for the lock to free, say), this test
// goes red on purpose: invert the assertion then.
func TestKnownGapAPollingScrubRetryCanMissEveryBriefGapBetweenBackToBackCycles(t *testing.T) {
	f, probe := newSlowCycleFixture(t)
	clock := installRetryClock(t)
	const interval = time.Minute
	retry := scrubBusyRetry(interval) // 6s
	const period, gap = 60 * time.Second, 5 * time.Second

	lockedAt := func(now time.Duration) bool { return now%period < period-gap }
	var now time.Duration
	f.b.runOnce.Lock()
	probe.held.Store(true)
	held := true
	setHeld := func(want bool) {
		if want == held {
			return
		}
		if want {
			f.b.runOnce.Lock()
		} else {
			f.b.runOnce.Unlock()
		}
		probe.held.Store(want)
		held = want
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		f.b.tickScrubWhenFree(context.Background(), interval)
	}()

	const polls = 10 * 24 * 60 * 10 // ten virtual days of 6s polls
	for i := 0; i < polls; i++ {
		if got := clock.next(t); got != retry {
			t.Fatalf("retry waited %v, want %v", got, retry)
		}
		now += retry
		setHeld(lockedAt(now))
		select {
		case <-done:
			t.Fatalf("the pass ran at virtual t=%v, so the gap was caught: the known gap is closed, invert this test", now)
		default:
		}
		clock.elapse()
	}
	// Free the lock for good and let the tick finish, so nothing leaks. The
	// tick has to be waiting again first, or it could still be mid-attempt.
	clock.next(t)
	setHeld(false)
	clock.elapse()
	<-done
}
