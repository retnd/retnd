package service

import (
	"bytes"
	"context"
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
// removes a bounded batch, a prune that loses the connection to somebody
// else is tried again shortly and quietly, and only a prune that has kept
// failing for hours is worth an operator's attention.

// heldJournal is a real journal whose one connection a test can hold, the
// way a slow reader holds it. While it is held a prune statement waits for
// it until its own deadline, as database/sql makes a caller wait for a pool
// of one, or fails at once when failFast is set.
type heldJournal struct {
	*state.Journal

	mu       sync.Mutex
	held     chan struct{}
	failFast bool
	delay    map[string]time.Duration
	calls    []pruneCall
}

// pruneCall is one prune statement as the journal saw it: which kind, the
// batch it was asked for, and how long its context gave it on arrival.
type pruneCall struct {
	kind   string
	limit  int
	budget time.Duration
	n      int64
	err    error
}

func newHeldJournal(t *testing.T) *heldJournal {
	return &heldJournal{Journal: openTestJournal(t), delay: map[string]time.Duration{}}
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
	call := pruneCall{kind: kind, limit: limit, budget: -1}
	if dl, ok := ctx.Deadline(); ok {
		call.budget = time.Until(dl)
	}
	h.mu.Lock()
	held, failFast, delay := h.held, h.failFast, h.delay[kind]
	h.mu.Unlock()
	if held != nil {
		if failFast {
			call.err = fmt.Errorf("state: prune activity events (%s): %w", kind, context.DeadlineExceeded)
		} else {
			select {
			case <-held:
			case <-ctx.Done():
				call.err = fmt.Errorf("state: prune activity events (%s): %w", kind, ctx.Err())
			}
		}
	}
	if call.err == nil && delay > 0 {
		time.Sleep(delay)
	}
	if call.err == nil {
		call.n, call.err = run(ctx)
	}
	h.mu.Lock()
	h.calls = append(h.calls, call)
	h.mu.Unlock()
	return call.n, call.err
}

func (h *heldJournal) PruneActivityEvents(ctx context.Context, olderThan time.Time, maxRows int) (int64, error) {
	return h.statement(ctx, "both", 0, func(ctx context.Context) (int64, error) {
		return h.Journal.PruneActivityEvents(ctx, olderThan, maxRows)
	})
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
// and log is the process log at its default level.
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
	r.d.statementTimeout = 50 * time.Millisecond
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

func (r *pruneRig) logged() bool {
	return strings.Contains(r.log.String(), "activity_events_prune")
}

func TestAPruneThatLosesTheConnectionToASlowReaderFinishesOnALaterTickWithoutAnError(t *testing.T) {
	r := newPruneRig(t)
	r.seed(10, 3)
	r.journal.hold()
	r.d.tick()
	if got := r.stored(); got != 10 {
		t.Fatalf("setup: the prune removed rows through a held connection (%d left)", got)
	}
	if lines := r.pruneErrors(); len(lines) != 0 || r.logged() {
		t.Errorf("one prune that waited behind a slow reader put %d error lines in the terminal (process log: %q)", len(lines), r.log.String())
	}

	r.journal.release()
	r.clock.advance(activityPruneRetry)
	r.d.tick()
	if got := r.stored(); got != 7 {
		t.Errorf("%d rows left a retry interval after the reader let go; the prune is owed again by then and want 7", got)
	}
	if lines := r.pruneErrors(); len(lines) != 0 || r.logged() {
		t.Errorf("a prune that lost one race and then finished left %d error lines (process log: %q)", len(lines), r.log.String())
	}
}

func TestEveryPruneStatementHasADeadlineOfItsOwn(t *testing.T) {
	r := newPruneRig(t)
	r.seed(10, 3)
	// The first statement takes most of a budget. A deadline shared with
	// the second would leave it the rest, which is what turned a slow
	// first statement into an error naming the second one.
	r.journal.delay["age"] = 35 * time.Millisecond
	r.d.tick()
	calls := r.journal.pruneCalls()
	kinds := map[string]bool{}
	for _, c := range calls {
		kinds[c.kind] = true
		if c.budget < 0 {
			t.Errorf("a %s statement ran with no deadline at all", c.kind)
			continue
		}
		if c.budget > r.d.statementTimeout || c.budget < r.d.statementTimeout*3/4 {
			t.Errorf("a %s statement arrived with %v of a %v statement budget; each statement owes a fresh one", c.kind, c.budget, r.d.statementTimeout)
		}
	}
	if !kinds["age"] || !kinds["count"] {
		t.Fatalf("the prune sent %+v; the age rule and the row bound are a statement each", calls)
	}
}

func TestAPruneOfAnOverCapTableRemovesRowsInBoundedBatches(t *testing.T) {
	r := newPruneRig(t)
	r.d.statementTimeout = activityPruneStatementTimeout
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

func TestAOneOffPruneFailureStaysOffTheOperatorTerminal(t *testing.T) {
	r := newPruneRig(t)
	r.seed(10, 3)
	r.journal.hold()
	r.journal.failFast = true
	r.d.tick()
	if len(r.journal.pruneCalls()) == 0 {
		t.Fatal("setup: no prune ran")
	}
	if lines := r.pruneLines(); len(lines) != 0 {
		t.Errorf("one failed prune reached the terminal as %d lines (level %q): it is retried, and nobody can act on it", len(lines), lines[0].Level)
	}
	if r.logged() {
		t.Errorf("one failed prune reached the process log at its default level: %q", r.log.String())
	}
}

func TestPruneFailuresSustainedForHoursSurfaceOnceAsAnError(t *testing.T) {
	r := newPruneRig(t)
	r.journal.hold()
	r.journal.failFast = true
	start := r.clock.now()
	var firstError time.Time
	for r.clock.now().Sub(start) <= activityPruneComplainAfter+time.Hour {
		r.d.tick()
		if firstError.IsZero() && len(r.pruneErrors()) > 0 {
			firstError = r.clock.now()
		}
		r.clock.advance(activityPruneRetry)
	}
	if firstError.IsZero() {
		t.Fatalf("a prune that failed for %v never surfaced", activityPruneComplainAfter+time.Hour)
	}
	if early := firstError.Sub(start); early < activityPruneComplainAfter {
		t.Errorf("the failure surfaced after %v; want quiet until it has failed for %v", early, activityPruneComplainAfter)
	}
	if got := len(r.pruneErrors()); got != 1 {
		t.Errorf("%d error lines in one window of sustained failure; want one", got)
	}
	if len(r.journal.pruneCalls()) < int(activityPruneComplainAfter/time.Hour) {
		t.Errorf("only %d prune attempts in %v of failure; a failed prune is owed a retry", len(r.journal.pruneCalls()), activityPruneComplainAfter)
	}
	e := r.pruneErrors()[0]
	var msg string
	for _, f := range e.Fields {
		if f.Key == "error" {
			msg = f.Value
		}
	}
	if !strings.Contains(msg, "deadline exceeded") {
		t.Errorf("the surfaced error %q does not say what kept failing", msg)
	}
}

func TestASuccessfulPruneStartsTheFailureWindowAgain(t *testing.T) {
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
	failFor(activityPruneComplainAfter - time.Hour)
	r.d.tick()
	if calls := r.journal.pruneCalls(); len(calls) == 0 || calls[len(calls)-1].err != nil {
		t.Fatal("setup: the prune between the two failing spells did not succeed")
	}
	r.journal.forgetCalls()
	r.clock.advance(activityPruneEvery)
	failFor(activityPruneComplainAfter - time.Hour)
	if lines := r.pruneErrors(); len(lines) != 0 {
		t.Errorf("two failing spells, each shorter than %v and parted by a prune that worked, surfaced %d errors", activityPruneComplainAfter, len(lines))
	}
}
