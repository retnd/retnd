package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/state"
)

// Panel finding C10, rows Q1.6 and Q1.21: the matrix said the scrub driver
// "runs" and a default install "scrubs" on the strength of tests that either
// import the package or call one tick by hand. Calling tickScrub in a test
// proves the tick works; it cannot prove that anything in the shipped process
// calls it. These go one hop further out: they start RunOnSchedule, the very
// method `retnd serve` runs for the life of the process (serve.RunEngine hands
// it the BackupService), and wait for the product to do the work on its own
// timer. Nothing here calls a tick, AssuranceTick or Scrub by hand.
//
// The mutation that justifies them is in the matrix: delete the goroutine in
// RunOnSchedule that runs the scrub timer and both go red, where the older
// tests (which call tickScrub directly) stay green.

// runScheduleUntil runs RunOnSchedule on a short poll interval and returns once
// done() reports true or the deadline passes, stopping the loop either way.
func runScheduleUntil(t *testing.T, b *BackupService, done func() bool) bool {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- b.RunOnSchedule(ctx) }()

	ok := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			ok = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("RunOnSchedule returned %v after its context was cancelled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunOnSchedule did not stop when its context was cancelled")
	}
	return ok
}

func TestRunOnScheduleScrubsADefaultInstallWithoutAnyTickBeingCalledByHand(t *testing.T) {
	// The backup cycle is not what is under test, and a real one would race
	// the hand-seeded copy. Everything else is the shipped wiring.
	withStubbedRunCycle(t, func(*app.Service, context.Context) app.CycleReport { return app.CycleReport{} })

	f := newRecoveryFixture(t, fixtureOpts{pollEvery: 20 * time.Millisecond})
	if err := f.b.state.Load().inner.Config.ResolveAssurance(); err != nil {
		t.Fatal(err)
	}
	a := seedComplete(t, f, "default.dump", state.MediumLocal)
	if got := claimOf(t, f, a, state.MediumLocal).AssuranceState; got != state.AssuranceUnknown {
		t.Fatalf("precondition: the copy is %q before the scheduler runs, want unknown", got)
	}

	scrubbed := runScheduleUntil(t, f.b, func() bool {
		return claimOf(t, f, a, state.MediumLocal).AssuranceState == state.AssurancePassed
	})
	if !scrubbed {
		t.Errorf("RunOnSchedule ran for 10s on a default install and the copy is still %q: nothing the serving process runs scrubs it", claimOf(t, f, a, state.MediumLocal).AssuranceState)
	}
}

func TestRunOnScheduleRunsTheDueRecoveryTestOfASetThatRequiresOne(t *testing.T) {
	withStubbedRunCycle(t, func(*app.Service, context.Context) app.CycleReport { return app.CycleReport{} })

	f := newRecoveryFixture(t, fixtureOpts{pollEvery: 20 * time.Millisecond, cfg: requireRecoveryTest})
	if err := f.b.state.Load().inner.Config.ResolveAssurance(); err != nil {
		t.Fatal(err)
	}
	requireRecoveryTest(f.b.state.Load().inner.Config)
	stages := &stubStages{script: "pg-check.local.sh"}
	withStages(f, stages)
	a := seedComplete(t, f, "due.dump", state.MediumLocal)
	if got := claimOf(t, f, a, state.MediumLocal).RecoveryState; got == state.RecoveryPassed {
		t.Fatalf("precondition: the copy is already recovery-tested (%q)", got)
	}

	tested := runScheduleUntil(t, f.b, func() bool {
		return claimOf(t, f, a, state.MediumLocal).RecoveryState == state.RecoveryPassed
	})
	if !tested {
		t.Errorf("RunOnSchedule ran for 10s and the due recovery test is %q: nothing the serving process runs starts it", claimOf(t, f, a, state.MediumLocal).RecoveryState)
	}
	if stages.ran() == 0 {
		t.Error("the recovery test passed without the validator running")
	}
}

// With the mode left at its default (off) the same loop runs no test at all:
// a default install is scrubbed, not recovery-tested, and a hand-edited
// validator directory is a convention and not a decision.
func TestRunOnScheduleRunsNoRecoveryTestWhenTheModeIsOff(t *testing.T) {
	withStubbedRunCycle(t, func(*app.Service, context.Context) app.CycleReport { return app.CycleReport{} })

	f := newRecoveryFixture(t, fixtureOpts{pollEvery: 20 * time.Millisecond})
	if err := f.b.state.Load().inner.Config.ResolveAssurance(); err != nil {
		t.Fatal(err)
	}
	stages := &stubStages{script: "pg-check.local.sh"}
	withStages(f, stages)
	a := seedComplete(t, f, "off.dump", state.MediumLocal)

	// Wait for the scrub, which proves the loop ran passes, then check the test did not.
	if !runScheduleUntil(t, f.b, func() bool {
		return claimOf(t, f, a, state.MediumLocal).AssuranceState == state.AssurancePassed
	}) {
		t.Fatal("the loop never ran a pass, so the absence of a test below would prove nothing")
	}
	if got := claimOf(t, f, a, state.MediumLocal).RecoveryState; got == state.RecoveryPassed || stages.ran() != 0 {
		t.Errorf("a set whose recovery-test mode is off was recovery-tested (state %q, validator ran %d times)", got, stages.ran())
	}
}

// The defect the process-level test found (panel finding C10): in a real
// `retnd serve` the scrub timer and the cycle loop both start with the
// process and both repeat every poll interval, so they stay in step. A cycle
// takes the single-flight lock at the top of its interval, the scrub tick
// arrives a fraction of a millisecond later, finds the lock held and skips,
// and the next one does the same an interval later, because the cycle that
// kept winning is still starting at the same moment. A default install
// therefore never scrubbed and never ran a due recovery test for as long as
// the skew between the two timers stayed smaller than a cycle, which is for
// ever in a deployment whose cycles take any real time. The tests above stub
// the cycle to nothing, which is why they could not see it.
//
// This reproduces the shape directly: something that holds the lock for most
// of every interval, starting just before the scrub timer's tick, as a
// cycle does. A tick that finds the lock held has to run as soon as it is
// free and not an interval later.
func TestAScrubTickThatFindsTheLockHeldRunsAsSoonAsItIsFree(t *testing.T) {
	const interval = 100 * time.Millisecond
	f := newRecoveryFixture(t, fixtureOpts{pollEvery: interval})
	if err := f.b.state.Load().inner.Config.ResolveAssurance(); err != nil {
		t.Fatal(err)
	}
	a := seedComplete(t, f, "phase.dump", state.MediumLocal)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()

	// The cycle: it takes the lock at the top of every interval and keeps it
	// for 70% of it. It is started before the scrub timer, as the cycle loop
	// is, so its next start always lands just ahead of the scrub's next tick.
	wg.Add(1)
	go func() {
		defer wg.Done()
		next := time.Now()
		for ctx.Err() == nil {
			f.b.runOnce.Lock()
			time.Sleep(70 * time.Millisecond)
			f.b.runOnce.Unlock()
			next = next.Add(interval)
			time.Sleep(time.Until(next))
		}
	}()
	time.Sleep(2 * time.Millisecond)
	wg.Add(1)
	go func() {
		defer wg.Done()
		f.b.runScrubTicks(ctx, interval)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if claimOf(t, f, a, state.MediumLocal).AssuranceState == state.AssurancePassed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("the copy is still %q after 5s of scrub ticks that each arrive while a cycle holds the lock: a tick that finds an operation running skips until the next interval, and the next interval finds the next cycle running", claimOf(t, f, a, state.MediumLocal).AssuranceState)
}
