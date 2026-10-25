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
// single-flight lock for longer than a scrub tick is willing to wait, nor the
// short gaps between back-to-back cycles. The first fix polled the lock on a
// timer and could miss every gap; the tick now queues on the lock itself
// (waitForCycleLock). These tests drive tickScrubWhenFree against a fake cycle
// that holds the real lock, and watch the validator to see when the pass starts.

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
	f = newRecoveryFixture(t, fixtureOpts{pollEvery: time.Minute, cfg: requireRecoveryTest})
	if err := f.b.state.Load().inner.Config.ResolveAssurance(); err != nil {
		t.Fatal(err)
	}
	requireRecoveryTest(f.b.state.Load().inner.Config)
	probe = &lockProbeStages{stubStages: &stubStages{script: "pg-check.local.sh"}, b: f.b, held: new(atomic.Bool)}
	f.b.state.Load().inner.Stages = probe
	seedComplete(t, f, "slow.dump", state.MediumLocal)
	return f, probe
}

func runTick(f *recoveryFixture, ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.b.tickScrubWhenFree(ctx)
	}()
	return done
}

// A cycle that holds the lock far longer than the tick has been waiting: the
// tick runs nothing while the lock is held, gives up on nothing, and runs the
// pass exactly once, promptly, after the lock is released. The wait is not a
// timer, so it does not matter that the interval is a minute.
func TestAScrubTickBehindALongCycleRunsOnceWhenTheLockFrees(t *testing.T) {
	f, probe := newSlowCycleFixture(t)

	f.b.runOnce.Lock()
	probe.held.Store(true)
	done := runTick(f, context.Background())

	time.Sleep(300 * time.Millisecond) // the cycle is still running
	if n := len(probe.startsSeen()); n != 0 {
		t.Fatalf("the pass started %d time(s) while the cycle still held the lock", n)
	}
	select {
	case <-done:
		t.Fatal("the tick gave up with the cycle still running")
	default:
	}

	released := time.Now()
	probe.held.Store(false)
	f.b.runOnce.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the lock was released and the tick did not run its pass")
	}
	if d := time.Since(released); d > 5*time.Second {
		t.Errorf("the pass took %v to start after the release: the wait must be woken by the release", d)
	}
	starts := probe.startsSeen()
	if len(starts) != 1 {
		t.Fatalf("the due pass ran %d times, want exactly once", len(starts))
	}
	if starts[0] {
		t.Error("the pass started while the lock was held: it ran concurrently with the cycle")
	}
}

// Regression for the starvation in the timed retry: the cycle loop frees the
// lock for only a short gap between back-to-back cycles, and a poll can land in
// the held part of every one of them. The tick has to run in one of the gaps.
func TestAScrubTickRunsInTheBriefGapBetweenBackToBackCycles(t *testing.T) {
	f, probe := newSlowCycleFixture(t)

	stop := make(chan struct{})
	var cycles sync.WaitGroup
	cycles.Add(1)
	go func() {
		defer cycles.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			f.b.runOnce.Lock()
			probe.held.Store(true)
			time.Sleep(20 * time.Millisecond)
			probe.held.Store(false)
			f.b.runOnce.Unlock()
			time.Sleep(time.Millisecond) // the gap
		}
	}()
	defer func() { close(stop); cycles.Wait() }()

	done := runTick(f, context.Background())
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("back-to-back cycles with a 1ms gap and the scrub never ran: the tick missed every gap")
	}
	// Not asserted here: whether the lock was held when the validator began.
	// The next cycle takes the lock a millisecond after the pass is let
	// through, and a cycle starting beside a running pass is by design (see
	// the last test), so the probe would be measuring that and not the start.
	if n := len(probe.startsSeen()); n != 1 {
		t.Errorf("the pass ran %d times, want exactly once", n)
	}
}

// A cycle that never ends must not keep the tick, or the process, alive.
func TestAScrubTickBehindACycleThatNeverEndsStopsWithItsContext(t *testing.T) {
	f, probe := newSlowCycleFixture(t)

	f.b.runOnce.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := runTick(f, ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the tick kept waiting after its context was cancelled")
	}
	if n := len(probe.startsSeen()); n != 0 {
		t.Errorf("the pass ran %d times behind a held lock", n)
	}

	// The cycle finally ends. The goroutine the cancelled wait left queued on
	// the lock takes it and gives it back, so the lock is usable again.
	f.b.runOnce.Unlock()
	locked := make(chan struct{})
	go func() { f.b.runOnce.Lock(); close(locked); f.b.runOnce.Unlock() }()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("the lock stayed taken after the cancelled tick's waiter should have released it")
	}
	if n := len(probe.startsSeen()); n != 0 {
		t.Errorf("a cancelled tick ran its pass (%d) once the lock freed", n)
	}
}

// A cycle that starts while the pass is running is by design (tickScrub does
// not hold the lock while it scrubs). It must neither block on the pass nor
// deadlock with it, and the pass still finishes.
func TestACycleThatStartsDuringAScrubPassIsNotBlockedByIt(t *testing.T) {
	f, probe := newSlowCycleFixture(t)
	gate := make(chan struct{})
	probe.gate = gate

	done := runTick(f, context.Background())
	deadline := time.Now().Add(10 * time.Second)
	for len(probe.startsSeen()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the pass never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cycle := make(chan struct{})
	go func() { f.b.runOnce.Lock(); close(cycle); f.b.runOnce.Unlock() }()
	select {
	case <-cycle:
	case <-time.After(5 * time.Second):
		t.Fatal("a cycle could not take the lock while a scrub pass was running")
	}

	close(gate)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the pass did not finish after a cycle ran beside it")
	}
	if n := len(probe.startsSeen()); n != 1 {
		t.Errorf("the pass ran %d times, want once", n)
	}
}
