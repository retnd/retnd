package budget

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/workflowrun"
)

func TestEveryBudgetHasADefaultAndAValidDefaultSet(t *testing.T) {
	d := Defaults()
	if err := d.Validate(); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
	for name, v := range map[string]int64{
		"concurrent reads":      int64(d.MaxConcurrentReads),
		"reads per destination": int64(d.MaxReadsPerDestination),
		"staging reserve":       d.StagingReserveBytes,
		"copies per tick":       int64(d.MaxCopiesPerTick),
		"validator output":      d.ValidatorOutputBytes,
		"concurrent downloads":  int64(d.MaxDownloadStreams),
	} {
		if v <= 0 {
			t.Errorf("%s has no default (%d), so it is a paragraph and not a budget", name, v)
		}
	}
	if d.MaxReadsPerDestination > d.MaxConcurrentReads {
		t.Error("the default per-destination limit could never bind")
	}
}

func TestEveryBudgetHasItsOwnExitCodeAboveTheOnesTheUsageTextPublishes(t *testing.T) {
	seen := map[int]Name{}
	for _, n := range All {
		code := ExitCode(n)
		if code <= 3 {
			t.Errorf("%s exits %d, which a published meaning already owns", n, code)
		}
		if other, dup := seen[code]; dup {
			t.Errorf("%s and %s both exit %d, so a script cannot tell them apart", n, other, code)
		}
		seen[code] = n
	}
	if len(All) != 7 {
		t.Errorf("the issue names seven budgets and All lists %d", len(All))
	}
}

func TestValidateRefusesLimitsThatCannotMeanAnything(t *testing.T) {
	for name, mutate := range map[string]func(*Limits){
		"zero reads":                   func(l *Limits) { l.MaxConcurrentReads = 0 },
		"negative reads":               func(l *Limits) { l.MaxConcurrentReads = -1 },
		"zero per destination":         func(l *Limits) { l.MaxReadsPerDestination = 0 },
		"per destination above global": func(l *Limits) { l.MaxReadsPerDestination = l.MaxConcurrentReads + 1 },
		"negative reserve":             func(l *Limits) { l.StagingReserveBytes = -1 },
		"zero copies per tick":         func(l *Limits) { l.MaxCopiesPerTick = 0 },
		"zero validator output":        func(l *Limits) { l.ValidatorOutputBytes = 0 },
		"zero downloads":               func(l *Limits) { l.MaxDownloadStreams = 0 },
	} {
		l := Defaults()
		mutate(&l)
		if err := l.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestWithDefaultsFillsOnlyWhatWasLeftZeroAndLeavesNegativesToBeRefused(t *testing.T) {
	got := Limits{MaxConcurrentReads: 9}.WithDefaults()
	if got.MaxConcurrentReads != 9 || got.MaxCopiesPerTick != DefaultMaxCopiesPerTick {
		t.Fatalf("got %+v", got)
	}
	if neg := (Limits{MaxCopiesPerTick: -5}).WithDefaults(); neg.MaxCopiesPerTick != -5 || neg.Validate() == nil {
		t.Fatalf("a negative limit was quietly replaced: %+v", neg)
	}
}

func TestAGateRefusesPastItsLineNamingTheBudgetAndFreesOnRelease(t *testing.T) {
	g := NewGate(DownloadStreams, 2)
	r1, err1 := g.Acquire()
	r2, err2 := g.Acquire()
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	_, err := g.Acquire()
	var ref *Refusal
	if !errors.As(err, &ref) || ref.Budget != DownloadStreams || ref.Limit != 2 || !errors.Is(err, ErrOverBudget) {
		t.Fatalf("over the line: %v", err)
	}
	r1()
	r1() // releasing twice must not give back a slot it does not hold
	if g.InFlight() != 1 {
		t.Fatalf("in flight = %d after one release (called twice)", g.InFlight())
	}
	if _, err := g.Acquire(); err != nil {
		t.Fatalf("a freed slot was not reusable: %v", err)
	}
	if _, err := g.Acquire(); err == nil {
		t.Fatal("the double release gave back a slot that was still held")
	}
	r2()
}

func TestAGateNeverAdmitsMoreThanItsLimitUnderContention(t *testing.T) {
	const limit, workers = 5, 400
	g := NewGate(ConcurrentReads, limit)
	var running, over atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := g.Acquire()
			if err != nil {
				return
			}
			if running.Add(1) > limit {
				over.Add(1)
			}
			time.Sleep(time.Millisecond)
			running.Add(-1)
			release()
		}()
	}
	wg.Wait()
	if over.Load() != 0 || g.Peak() > limit {
		t.Fatalf("the ceiling was exceeded: %d over, peak %d", over.Load(), g.Peak())
	}
	if g.Peak() == 0 {
		t.Fatal("nothing ever ran, so the check above proved nothing")
	}
}

func TestReadsRefusesPerDestinationBeforeGlobalAndNamesTheDifference(t *testing.T) {
	r := NewReads(Limits{MaxConcurrentReads: 4, MaxReadsPerDestination: 2})
	var held []func()
	take := func(dest string) error {
		rel, err := r.Acquire(dest)
		if err == nil {
			held = append(held, rel)
		}
		return err
	}
	if take("a") != nil || take("a") != nil {
		t.Fatal("the first two reads of a destination should be admitted")
	}
	var ref *Refusal
	if err := take("a"); !errors.As(err, &ref) || ref.Budget != DestinationReads || ref.Destination != "a" {
		t.Fatalf("a third read of one destination: %v", err)
	}
	if take("b") != nil || take("b") != nil {
		t.Fatal("another destination must not be starved by the first")
	}
	if err := take("c"); !errors.As(err, &ref) || ref.Budget != ConcurrentReads {
		t.Fatalf("a fifth read overall: %v", err)
	}
	held[0]()
	if err := take("c"); err != nil {
		t.Fatalf("a global slot was freed and a different destination still refused: %v", err)
	}
	g, per := r.Peaks()
	if g != 4 || per["a"] != 2 || per["b"] != 2 {
		t.Fatalf("peaks = %d %v", g, per)
	}
}

func TestAGlobalRefusalDoesNotLeakTheDestinationSlotItBrieflyTook(t *testing.T) {
	r := NewReads(Limits{MaxConcurrentReads: 1, MaxReadsPerDestination: 1})
	rel, _ := r.Acquire("a")
	for i := 0; i < 20; i++ {
		if _, err := r.Acquire("b"); err == nil {
			t.Fatal("the global line did not hold")
		}
	}
	rel()
	if _, err := r.Acquire("b"); err != nil {
		t.Fatalf("refused attempts leaked destination b's slot: %v", err)
	}
}

func TestOneThousandDueCopiesSpreadOverPassesAndNothingStarves(t *testing.T) {
	l := Defaults()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var due []Candidate
	for i := 0; i < 1200; i++ {
		due = append(due, Candidate{ID: idOf(i), Destination: "d", Due: base.Add(time.Duration(1200-i) * time.Minute)})
	}
	done := map[string]int{}
	passes := 0
	for len(due) > 0 {
		passes++
		considered, deferred := SelectTick(due, l)
		if len(considered) > l.MaxCopiesPerTick {
			t.Fatalf("pass %d considered %d, over the bound of %d", passes, len(considered), l.MaxCopiesPerTick)
		}
		if len(considered) == 0 {
			t.Fatal("a pass considered nothing while copies were due: that is a stall")
		}
		if passes == 1 {
			// The oldest due is first: the copy that has waited longest
			// goes before any newer one.
			if considered[0].Due.After(considered[len(considered)-1].Due) {
				t.Fatal("not ordered oldest first")
			}
		}
		for _, c := range considered {
			done[c.ID]++
		}
		due = deferred
	}
	if len(done) != 1200 {
		t.Fatalf("%d of 1200 copies were ever considered", len(done))
	}
	for id, n := range done {
		if n != 1 {
			t.Fatalf("%s considered %d times", id, n)
		}
	}
	if want := (1200 + l.MaxCopiesPerTick - 1) / l.MaxCopiesPerTick; passes != want {
		t.Fatalf("took %d passes, want %d", passes, want)
	}
}

func idOf(i int) string { return "set/artifact-" + strings.Repeat("0", 5-len(itoa(i))) + itoa(i) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func TestATickWithFewerDueCopiesThanTheBoundTakesThemAll(t *testing.T) {
	c, d := SelectTick([]Candidate{{ID: "a"}, {ID: "b"}}, Defaults())
	if len(c) != 2 || len(d) != 0 {
		t.Fatalf("%v %v", c, d)
	}
	if c, d := SelectTick(nil, Defaults()); len(c) != 0 || len(d) != 0 {
		t.Fatalf("%v %v", c, d)
	}
}

func TestTheValidatorOutputBoundIsEpicLsAndNotACopyOfIt(t *testing.T) {
	if Defaults().ValidatorOutputBytes != workflowrun.DefaultStepOutputBytes {
		t.Fatalf("the validator output bound is %d and EPIC L's is %d: this budget reuses that bound rather than keeping its own",
			Defaults().ValidatorOutputBytes, workflowrun.DefaultStepOutputBytes)
	}
}

func TestAScheduledPassCostsNoEgressByConstruction(t *testing.T) {
	if ScheduledCeiling().CostsEgress() {
		t.Fatalf("the unattended ceiling %s costs egress, so a scheduled pass could download", ScheduledCeiling())
	}
	if ScheduledCeiling() != placement.Existence {
		t.Fatalf("the automatic ceiling moved to %s; FR-31 keeps it at existence", ScheduledCeiling())
	}
	if !placement.Content.CostsEgress() {
		t.Fatal("test premise: a content read costs egress")
	}
	if ExitCode(ScheduledEgress) == 0 {
		t.Fatal("the zero-egress budget has no exit code")
	}
}

func TestARefusalReadsInWordsAnOperatorCanActOn(t *testing.T) {
	for _, r := range []*Refusal{
		{Budget: ConcurrentReads, Limit: 4},
		{Budget: DestinationReads, Limit: 2, Destination: "offsite"},
		{Budget: DownloadStreams, Limit: 4},
		{Budget: StagingReserve, Limit: 1 << 30},
		{Budget: TickWork, Limit: 50},
	} {
		msg := r.Error()
		if !strings.Contains(msg, "budget:") || len(msg) < 40 {
			t.Errorf("%s: %q", r.Budget, msg)
		}
		if strings.ContainsAny(msg, "$") || strings.Contains(strings.ToLower(msg), "usd") {
			t.Errorf("%s renders a price: %q", r.Budget, msg)
		}
	}
	if (&Refusal{Budget: DestinationReads, Destination: "offsite"}).Error() == (&Refusal{Budget: ConcurrentReads}).Error() {
		t.Error("the global and per-destination refusals read the same")
	}
}

func TestAcquireWaitBlocksUntilASlotFreesAndHonoursCancellation(t *testing.T) {
	g := NewGate(ConcurrentReads, 1)
	first, err := g.Acquire()
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan error, 1)
	go func() {
		rel, err := g.AcquireWait(context.Background())
		if err == nil {
			rel()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("a waiter was admitted while the gate was full: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	first()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("the waiter was refused after the slot freed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter never got the freed slot")
	}

	// A cancelled waiter leaves nothing behind.
	hold, _ := g.Acquire()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := g.AcquireWait(ctx); done <- err }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	hold()
	if g.InFlight() != 0 {
		t.Fatalf("in flight = %d after everything was released: a cancelled waiter leaked a slot", g.InFlight())
	}
}

func TestReadsAcquireWaitHoldsBothCeilingsUnderContention(t *testing.T) {
	r := NewReads(Limits{MaxConcurrentReads: 4, MaxReadsPerDestination: 2})
	var wg sync.WaitGroup
	var running, runningA, over atomic.Int32
	for i := 0; i < 200; i++ {
		dest := "a"
		if i%3 == 0 {
			dest = "b"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := r.AcquireWait(context.Background(), dest)
			if err != nil {
				t.Error(err)
				return
			}
			if running.Add(1) > 4 {
				over.Add(1)
			}
			if dest == "a" && runningA.Add(1) > 2 {
				over.Add(1)
			}
			time.Sleep(time.Millisecond)
			if dest == "a" {
				runningA.Add(-1)
			}
			running.Add(-1)
			rel()
		}()
	}
	wg.Wait()
	if over.Load() != 0 {
		t.Fatalf("a ceiling was exceeded %d times", over.Load())
	}
	g, per := r.Peaks()
	if g > 4 || per["a"] > 2 || per["b"] > 2 {
		t.Fatalf("peaks = %d %v", g, per)
	}
	if g < 3 {
		t.Fatalf("peak global = %d: the test never exercised the ceiling", g)
	}
}
