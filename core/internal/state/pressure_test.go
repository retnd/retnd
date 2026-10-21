package state

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The journal's one connection is the thing every caller queues for, and
// #1247 was a deadline missed behind something that held it with nothing
// to say what. These cases hold what the journal now says about it: a hold
// past SlowHoldThreshold is reported with what held it and how many callers
// waited meanwhile, and the wait itself is counted.

// stepClock is a clock a test moves by hand.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// holdRig is a journal on a hand-moved clock whose slow holds are recorded.
type holdRig struct {
	j     *Journal
	clock *stepClock
	mu    sync.Mutex
	holds []SlowHold
}

func newHoldRig(t *testing.T) *holdRig {
	t.Helper()
	j, _ := openJournal(t)
	r := &holdRig{j: j, clock: &stepClock{t: time.Now()}}
	j.pressure.now = r.clock.now
	j.SetSlowHoldHook(func(h SlowHold) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.holds = append(r.holds, h)
	})
	return r
}

func (r *holdRig) reported() []SlowHold {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]SlowHold(nil), r.holds...)
}

func TestACursorHeldPastTheThresholdIsReportedWithWhatItRead(t *testing.T) {
	r := newHoldRig(t)
	ctx := context.Background()
	rows, err := r.j.db.QueryContext(ctx, "SELECT seq FROM   activity_events\n\tORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	r.clock.advance(3 * time.Second)
	for rows.Next() {
	}
	_ = rows.Close()

	got := r.reported()
	if len(got) != 1 {
		t.Fatalf("a cursor held for 3s was reported %d times: %+v", len(got), got)
	}
	if got[0].Held != 3*time.Second || got[0].Op != "SELECT seq FROM activity_events ORDER BY seq" {
		t.Fatalf("reported %+v; want the 3s hold named by its statement, whitespace folded", got[0])
	}
	if p := r.j.Pressure(); p.SlowHolds != 1 || p.LongestHold != 3*time.Second || p.LongestHoldOp != got[0].Op {
		t.Fatalf("pressure = %+v; it does not remember the slow hold", p)
	}
}

func TestATransactionIsOneHoldNamedByItsFirstStatement(t *testing.T) {
	r := newHoldRig(t)
	ctx := context.Background()
	tx, err := r.j.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM activity_events WHERE seq = ?`, 1); err != nil {
		t.Fatal(err)
	}
	r.clock.advance(2 * time.Second)
	if _, err := tx.ExecContext(ctx, `DELETE FROM activity_events WHERE seq = ?`, 2); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := r.reported()
	if len(got) != 1 || got[0].Held != 2*time.Second || got[0].Op != "transaction: DELETE FROM activity_events WHERE seq = ?" {
		t.Fatalf("reported %+v; want one 2s hold for the transaction, named by its first statement", got)
	}
}

func TestAQuickStatementIsNotReported(t *testing.T) {
	r := newHoldRig(t)
	if _, err := r.j.MaxActivityEventSeq(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.reported(); len(got) != 0 {
		t.Fatalf("a statement that took no time was reported: %+v", got)
	}
}

func TestTheCallersAHoldKeptWaitingAreCounted(t *testing.T) {
	r := newHoldRig(t)
	ctx := context.Background()
	rows, err := r.j.db.QueryContext(ctx, `SELECT seq FROM activity_events`)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := r.j.MaxActivityEventSeq(wctx)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for r.j.db.Stats().WaitCount == 0 {
		if time.Now().After(deadline) {
			t.Fatal("setup: the second caller never queued for the connection")
		}
		time.Sleep(time.Millisecond)
	}
	r.clock.advance(2 * time.Second)
	_ = rows.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := r.reported()
	if len(got) != 1 || got[0].Waiters < 1 {
		t.Fatalf("reported %+v; the hold kept a caller waiting and should say so", got)
	}
	if p := r.j.Pressure(); p.Waits < 1 || p.WaitTime <= 0 {
		t.Fatalf("pressure = %+v; a caller waited for the connection and it was not counted", p)
	}
}

func TestContentionIsTheDeadlineAndTheLockAndNothingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	ctx := context.Background()
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.db.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		t.Fatal(err)
	}
	conn, err := a.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "ROLLBACK") //nolint:errcheck // the test is over
	_, busy := b.PruneActivityEventsByAge(ctx, time.Now(), 10)
	if busy == nil {
		t.Fatal("setup: a write under another connection's write lock succeeded")
	}

	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"a statement that missed its deadline", fmt.Errorf("state: prune: %w", context.DeadlineExceeded), true},
		{"the write lock held by another connection", busy, true},
		{"a cancelled caller", fmt.Errorf("state: prune: %w", context.Canceled), false},
		{"a disk fault", errors.New("disk I/O error"), false},
		{"no error", nil, false},
	} {
		if got := IsContention(c.err); got != c.want {
			t.Errorf("IsContention(%s: %v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}
