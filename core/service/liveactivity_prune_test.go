package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// The hourly prune of the activity feed's table (#1247). On a NAS busy with a
// scrub it failed every hour with "context deadline exceeded" and put an
// error line in the operator's terminal each time. These cases hold what the
// prune promises instead: every statement has a deadline of its own and
// removes a bounded batch, one pass does a bounded amount of work and stops
// when the writer does, a prune that loses the connection to somebody else
// is tried again shortly with one warning in the process log, and only
// contention that lasts, or a fault that is not contention at all, is worth
// an operator's attention.

// heldJournal is a real journal whose one connection a test can hold, the
// way a slow reader holds it. While it is held a prune statement waits for
// it until its own deadline or holdFor, whichever is first, and then fails
// the way database/sql fails a caller that waited out its deadline for a
// pool of one. failFast makes a held statement fail at once, and failWith
// makes every statement fail with that error instead.
type heldJournal struct {
	*state.Journal

	mu       sync.Mutex
	held     chan struct{}
	holdFor  time.Duration
	failFast bool
	failWith error
	delay    map[string]time.Duration
	calls    []pruneCall
}

// pruneCall is one prune statement as the journal saw it: which kind, the
// batch it was asked for, when it arrived and finished, and the deadline
// its context carried.
type pruneCall struct {
	kind     string
	limit    int
	at, end  time.Time
	deadline time.Time
	n        int64
	err      error
}

func newHeldJournal(t *testing.T) *heldJournal {
	return &heldJournal{Journal: openTestJournal(t), holdFor: 50 * time.Millisecond, delay: map[string]time.Duration{}}
}

func (h *heldJournal) hold() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held == nil {
		h.held = make(chan struct{})
	}
}

func (h *heldJournal) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held != nil {
		close(h.held)
		h.held = nil
	}
}

func (h *heldJournal) statement(ctx context.Context, kind string, limit int, run func(context.Context) (int64, error)) (int64, error) {
	call := pruneCall{kind: kind, limit: limit, at: time.Now()}
	call.deadline, _ = ctx.Deadline()
	h.mu.Lock()
	held, holdFor, failFast, failWith, delay := h.held, h.holdFor, h.failFast, h.failWith, h.delay[kind]
	h.mu.Unlock()
	switch {
	case failWith != nil:
		call.err = failWith
	case held != nil && failFast:
		call.err = fmt.Errorf("state: prune activity events (%s): %w", kind, context.DeadlineExceeded)
	case held != nil:
		timer := time.NewTimer(holdFor)
		select {
		case <-held:
		case <-ctx.Done():
			call.err = fmt.Errorf("state: prune activity events (%s): %w", kind, ctx.Err())
		case <-timer.C:
			call.err = fmt.Errorf("state: prune activity events (%s): %w", kind, context.DeadlineExceeded)
		}
		timer.Stop()
	}
	if call.err == nil && delay > 0 {
		time.Sleep(delay)
	}
	if call.err == nil {
		call.n, call.err = run(ctx)
	}
	call.end = time.Now()
	h.mu.Lock()
	h.calls = append(h.calls, call)
	h.mu.Unlock()
	return call.n, call.err
}

func (h *heldJournal) PruneActivityEventsByAge(ctx context.Context, olderThan time.Time, limit int) (int64, error) {
	return h.statement(ctx, "age", limit, func(ctx context.Context) (int64, error) {
		return h.Journal.PruneActivityEventsByAge(ctx, olderThan, limit)
	})
}

func (h *heldJournal) PruneActivityEventsOverCount(ctx context.Context, maxRows, limit int) (int64, error) {
	return h.statement(ctx, "count", limit, func(ctx context.Context) (int64, error) {
		return h.Journal.PruneActivityEventsOverCount(ctx, maxRows, limit)
	})
}

func (h *heldJournal) pruneCalls() []pruneCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]pruneCall(nil), h.calls...)
}

func (h *heldJournal) forgetCalls() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = nil
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// pruneRig is a writer over a held journal, a fake clock, and a logger whose
// tap is a feed of its own: that feed is what the operator's terminal reads,
// and log is the process log at its default level. The writer keeps its
// real statement timeout: the real statements commit with an fsync each,
// and only the held path, which never reaches SQLite, is short.
type pruneRig struct {
	t       *testing.T
	journal *heldJournal
	clock   *fakeClock
	feed    *liveActivity
	log     *bytes.Buffer
	d       *activityDurable
}

func newPruneRig(t *testing.T) *pruneRig {
	t.Helper()
	r := &pruneRig{
		t: t, journal: newHeldJournal(t), clock: &fakeClock{t: time.Now().UTC()},
		feed: newLiveActivity(func(string) bool { return true }), log: &bytes.Buffer{},
	}
	r.d = newActivityDurable(r.journal, obs.New(r.log, obs.LevelInfo).WithSink(r.feed))
	r.d.now = r.clock.now
	return r
}

// seed stores n rows, the first old of them past the retention.
func (r *pruneRig) seed(n, old int) {
	r.t.Helper()
	rows := make([]state.ActivityEventRow, 0, n)
	for i := 1; i <= n; i++ {
		at := r.clock.now()
		if i <= old {
			at = at.Add(-activityRetention - time.Hour)
		}
		rows = append(rows, state.ActivityEventRow{Seq: int64(i), At: at, Level: "info", Event: "e", Scope: "set", SetID: "alpha/nightly", Fields: "[]"})
	}
	if err := r.journal.AppendActivityEvents(context.Background(), rows); err != nil {
		r.t.Fatal(err)
	}
}

func (r *pruneRig) stored() int {
	r.t.Helper()
	rows, err := r.journal.RecentActivityEvents(context.Background(), "alpha/nightly", 2*activityMaxRows)
	if err != nil {
		r.t.Fatal(err)
	}
	return len(rows)
}

// pruneLines is every line about the prune the feed holds, at any level.
func (r *pruneRig) pruneLines() []LiveActivityEvent {
	r.feed.mu.Lock()
	defer r.feed.mu.Unlock()
	var out []LiveActivityEvent
	rings := []*liveActivityRing{r.feed.deployment}
	for _, st := range r.feed.sets {
		rings = append(rings, st.ring)
	}
	for _, ring := range rings {
		for i := 0; i < ring.len(); i++ {
			e := ring.at(i)
			if e.Event == obs.EventActivityPruneDeferred {
				out = append(out, e)
				continue
			}
			for _, f := range e.Fields {
				if f.Key == "op" && f.Value == "activity_events_prune" {
					out = append(out, e)
				}
			}
		}
	}
	return out
}

func (r *pruneRig) pruneErrors() []LiveActivityEvent {
	var out []LiveActivityEvent
	for _, e := range r.pruneLines() {
		if e.Level == "error" {
			out = append(out, e)
		}
	}
	return out
}

// logLines is the process log's lines that name an event, as event@LEVEL.
func (r *pruneRig) logLines(event string) []string {
	var out []string
	for _, line := range strings.Split(r.log.String(), "\n") {
		if !strings.Contains(line, `"event":"`+event+`"`) {
			continue
		}
		level := "?"
		for _, l := range []string{"DEBUG", "INFO", "WARN", "ERROR"} {
			if strings.Contains(line, `"level":"`+l+`"`) {
				level = l
			}
		}
		out = append(out, event+"@"+level)
	}
	return out
}

// loggedPruneError reports whether the process log holds an error-level
// line about the prune.
func (r *pruneRig) loggedPruneError() bool {
	for _, line := range strings.Split(r.log.String(), "\n") {
		if strings.Contains(line, "activity_events_prune") && strings.Contains(line, `"level":"ERROR"`) {
			return true
		}
	}
	return false
}

func TestAPruneThatLosesTheConnectionToASlowReaderFinishesOnALaterTickWithoutAnError(t *testing.T) {
	r := newPruneRig(t)
	r.seed(10, 3)
	r.journal.hold()
	r.d.tick()
	if got := r.stored(); got != 10 {
		t.Fatalf("setup: the prune removed rows through a held connection (%d left)", got)
	}
	if lines := r.pruneErrors(); len(lines) != 0 || r.loggedPruneError() {
		t.Errorf("one prune that waited behind a slow reader put %d error lines in the terminal (process log: %q)", len(lines), r.log.String())
	}

	r.journal.release()
	r.clock.advance(activityPruneRetry)
	r.d.tick()
	if got := r.stored(); got != 7 {
		t.Errorf("%d rows left a retry interval after the reader let go; the prune is owed again by then and want 7", got)
	}
	if lines := r.pruneErrors(); len(lines) != 0 || r.loggedPruneError() {
		t.Errorf("a prune that lost one race and then finished left %d error lines (process log: %q)", len(lines), r.log.String())
	}
}

func TestEveryPruneStatementHasADeadlineOfItsOwn(t *testing.T) {
	r := newPruneRig(t)
	r.seed(10, 3)
	// The first statement takes a while. A deadline shared with the second
	// would end where the first one's ends, which is what turned a slow
	// first statement into an error naming the second one.
	r.journal.delay["age"] = 30 * time.Millisecond
	r.d.tick()
	calls := r.journal.pruneCalls()
	if len(calls) < 2 || calls[0].kind != "age" || calls[1].kind != "count" {
		t.Fatalf("the prune sent %+v; the age rule and the row bound are a statement each", calls)
	}
	for i, c := range calls {
		if c.deadline.IsZero() {
			t.Fatalf("the %s statement ran with no deadline at all", c.kind)
		}
		if c.deadline.Sub(c.at) > r.d.statementTimeout {
			t.Errorf("the %s statement's deadline is %v after it arrived, past the %v statement budget", c.kind, c.deadline.Sub(c.at), r.d.statementTimeout)
		}
		if i == 0 {
			continue
		}
		prev := calls[i-1]
		if !c.deadline.After(prev.deadline) || c.deadline.Sub(prev.end) < r.d.statementTimeout/2 {
			t.Errorf("the %s statement's deadline is %v after the %s statement finished; each statement owes a fresh %v",
				c.kind, c.deadline.Sub(prev.end), prev.kind, r.d.statementTimeout)
		}
	}
}

func TestAPruneOfAnOverCapTableRemovesRowsInBoundedBatches(t *testing.T) {
	r := newPruneRig(t)
	over := 2*activityPruneBatch + activityPruneBatch/2
	r.seed(activityMaxRows+over, 0)
	r.d.tick()

	var removed int64
	var batches int
	for _, c := range r.journal.pruneCalls() {
		if c.err != nil {
			t.Fatalf("a prune statement failed: %v", c.err)
		}
		if c.kind != "count" {
			continue
		}
		batches++
		removed += c.n
		if c.limit != activityPruneBatch || c.n > int64(activityPruneBatch) {
			t.Errorf("a row-bound statement was asked for %d rows and removed %d; every one is a batch of at most %d", c.limit, c.n, activityPruneBatch)
		}
	}
	if batches != 3 || removed != int64(over) {
		t.Errorf("the row bound took %d statements removing %d rows; want 3 batches removing %d", batches, removed, over)
	}
	if got := r.stored(); got != activityMaxRows {
		t.Errorf("%d rows left; want the bound, %d", got, activityMaxRows)
	}
}

func TestAPrunePassStopsAtItsStatementBudgetAndCarriesOnShortly(t *testing.T) {
	r := newPruneRig(t)
	over := (activityPruneStatementsPerPass + 3) * activityPruneBatch
	r.seed(activityMaxRows+over, 0)
	r.d.tick()
	if got := len(r.journal.pruneCalls()); got != activityPruneStatementsPerPass {
		t.Fatalf("one pass sent %d statements; want it to stop at its budget of %d", got, activityPruneStatementsPerPass)
	}
	left := r.stored()
	if left <= activityMaxRows {
		t.Fatalf("one pass removed everything (%d left); the setup needs more than a pass of work", left)
	}

	r.clock.advance(activityPruneContinue)
	r.d.tick()
	if got := r.stored(); got != activityMaxRows {
		t.Errorf("%d rows left %v after a pass ran out of budget; the next pass is owed by then and want %d", got, activityPruneContinue, activityMaxRows)
	}
	if lines := r.pruneLines(); len(lines) != 0 || len(r.logLines(obs.EventActivityPruneDeferred)) != 0 || r.loggedPruneError() {
		t.Errorf("a pass that ran out of budget is not a failure, but it said so: %d feed lines, log %q", len(lines), r.log.String())
	}
}

func TestACancelledWriterStopsAPrunePassAndSaysNothing(t *testing.T) {
	r := newPruneRig(t)
	r.seed(activityMaxRows+3*activityPruneBatch, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.d.parent = ctx
	r.d.tick()
	if got := len(r.journal.pruneCalls()); got > 1 {
		t.Errorf("a pass whose writer had stopped sent %d statements; it must stop at the first", got)
	}
	if got := r.stored(); got != activityMaxRows+3*activityPruneBatch {
		t.Errorf("a pass whose writer had stopped removed %d rows", activityMaxRows+3*activityPruneBatch-got)
	}
	if lines := r.pruneLines(); len(lines) != 0 || strings.Contains(r.log.String(), "prune") {
		t.Errorf("stopping is not a prune failure, but it was reported: %d feed lines, log %q", len(lines), r.log.String())
	}
}

func TestTheShutdownFlushWritesButDoesNotPrune(t *testing.T) {
	r := newPruneRig(t)
	r.seed(10, 3)
	r.d.inserted = activityPruneAfter // a prune would be owed on the next flush
	r.d.offer("alpha/nightly", LiveActivityEvent{Sequence: 100, At: r.clock.now(), Level: "info", Event: obs.EventScrubPass})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		r.d.run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the writer did not stop")
	}
	if got := r.stored(); got != 11 {
		t.Errorf("%d rows stored after shutdown; want the queued event written and nothing pruned (11)", got)
	}
	if calls := r.journal.pruneCalls(); len(calls) != 0 {
		t.Errorf("the shutdown flush sent %d prune statements; stopping must not wait on a prune", len(calls))
	}
}

func TestAOneOffPruneFailureStaysOffTheOperatorTerminalAndWarnsTheLogOnce(t *testing.T) {
	r := newPruneRig(t)
	r.seed(10, 3)
	r.journal.hold()
	r.journal.failFast = true
	r.d.tick()
	r.clock.advance(activityPruneRetry)
	r.d.tick()
	if len(r.journal.pruneCalls()) < 2 {
		t.Fatal("setup: the prune did not run twice")
	}
	if lines := r.pruneLines(); len(lines) != 0 {
		t.Errorf("a prune that lost a race reached the terminal as %d lines (level %q): it is retried, and nobody can act on it", len(lines), lines[0].Level)
	}
	if r.loggedPruneError() {
		t.Errorf("a prune that lost a race reached the process log as an error: %q", r.log.String())
	}
	if got := r.logLines(obs.EventActivityPruneDeferred); len(got) != 1 || got[0] != obs.EventActivityPruneDeferred+"@WARN" {
		t.Errorf("the process log holds %v for two deferrals in one run of failures; want one warning, so a deferral is visible at the default level without repeating", got)
	}
}

func TestPruneContentionSustainedForAWindowSurfacesOnceAsAnError(t *testing.T) {
	r := newPruneRig(t)
	r.journal.hold()
	r.journal.failFast = true
	start := r.clock.now()
	var firstError time.Time
	for r.clock.now().Sub(start) < 2*activityPruneComplainAfter-activityPruneRetry {
		r.d.tick()
		if firstError.IsZero() && len(r.pruneErrors()) > 0 {
			firstError = r.clock.now()
		}
		r.clock.advance(activityPruneRetry)
	}
	if activityPruneComplainAfter > time.Hour {
		t.Errorf("contention may go quiet for %v; a table past its bound for longer than an hour is somebody's business", activityPruneComplainAfter)
	}
	if firstError.IsZero() {
		t.Fatalf("a prune that lost every race for %v never surfaced", 2*activityPruneComplainAfter)
	}
	if early := firstError.Sub(start); early < activityPruneComplainAfter {
		t.Errorf("the contention surfaced after %v; want quiet until it has lasted %v", early, activityPruneComplainAfter)
	}
	if got := len(r.pruneErrors()); got != 1 {
		t.Errorf("%d error lines in one window of sustained contention; want one", got)
	}
	if len(r.journal.pruneCalls()) < int(activityPruneComplainAfter/activityPruneRetry) {
		t.Errorf("only %d prune attempts in %v of contention; a deferred prune is owed a retry", len(r.journal.pruneCalls()), activityPruneComplainAfter)
	}
	var msg string
	for _, f := range r.pruneErrors()[0].Fields {
		if f.Key == "error" {
			msg = f.Value
		}
	}
	if !strings.Contains(msg, "deadline exceeded") {
		t.Errorf("the surfaced error %q does not say what kept failing", msg)
	}
}

func TestASuccessfulPruneStartsTheContentionWindowAgain(t *testing.T) {
	r := newPruneRig(t)
	r.journal.failFast = true
	failFor := func(d time.Duration) {
		r.journal.hold()
		end := r.clock.now().Add(d)
		for r.clock.now().Before(end) {
			r.d.tick()
			r.clock.advance(activityPruneRetry)
		}
		r.journal.release()
	}
	failFor(activityPruneComplainAfter - 10*time.Minute)
	r.d.tick()
	if calls := r.journal.pruneCalls(); len(calls) == 0 || calls[len(calls)-1].err != nil {
		t.Fatal("setup: the prune between the two failing spells did not succeed")
	}
	r.journal.forgetCalls()
	r.clock.advance(activityPruneEvery)
	failFor(activityPruneComplainAfter - 10*time.Minute)
	if lines := r.pruneErrors(); len(lines) != 0 {
		t.Errorf("two spells of contention, each shorter than %v and parted by a prune that worked, surfaced %d errors", activityPruneComplainAfter, len(lines))
	}
}

func TestAPruneFaultThatIsNotContentionSurfacesAtOnceAndOncePerWindow(t *testing.T) {
	r := newPruneRig(t)
	r.journal.failWith = errors.New("state: prune activity events by age: disk I/O error (10)")
	r.d.tick()
	if got := r.pruneErrors(); len(got) != 1 {
		t.Fatalf("a disk fault on the first prune put %d error lines in the terminal; a fault a retry will not fix is reported straight away", len(got))
	}
	for i := 0; i < 5; i++ {
		r.clock.advance(activityPruneRetry)
		r.d.tick()
	}
	if got := r.pruneErrors(); len(got) != 1 {
		t.Errorf("%d error lines for the same fault inside one window; want it reported once per window", len(got))
	}
	r.clock.advance(activityPruneComplainAfter)
	r.d.tick()
	if got := r.pruneErrors(); len(got) != 2 {
		t.Errorf("%d error lines once a window had passed with the fault still there; want it reported again", len(got))
	}
}
