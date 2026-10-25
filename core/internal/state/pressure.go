package state

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// What the journal's one connection costs its callers (#1247).
//
// A Journal is pinned to a single connection (see Open), so every caller
// queues for the same thing, and anything that keeps it shows up somewhere
// else as somebody's missed deadline. #1247 was exactly that: an hourly
// prune failing with "context deadline exceeded" and nothing to say what
// it had been waiting behind. This file is what says it next time.
//
// The connection is opened through a thin driver wrapper that times every
// HOLD of it: a statement from start to finish, a cursor from the query to
// its Close, and a transaction from Begin to Commit or Rollback, with the
// statements inside a transaction counted as part of it rather than on
// their own. A hold past SlowHoldThreshold is counted and handed to the
// hook SetSlowHoldHook installed, named by its SQL (whitespace folded, cut
// to opMax) and with the number of callers database/sql made wait while it
// lasted. The time those callers spent waiting is database/sql's own
// count, and Pressure reports it.
//
// The wrapper forwards everything else to modernc.org/sqlite unchanged, and
// in particular the context-aware Exec and Query paths: without them
// database/sql would fall back to Prepare, and a multi-statement Exec (the
// migrations) would run only its first statement.

// SlowHoldThreshold is how long one statement, cursor or transaction may
// keep the journal's connection before it is reported.
const SlowHoldThreshold = time.Second

// opMax bounds how much of a statement names a hold.
const opMax = 120

// SlowHold is one hold of the journal's connection that lasted past
// SlowHoldThreshold: what held it, for how long, and how many callers
// queued for the connection meanwhile.
type SlowHold struct {
	Op      string
	Held    time.Duration
	Waiters int64
}

// ConnectionPressure is what the journal's connection has cost its callers
// since Open: how many of them had to wait for it and for how long in all,
// and the slow holds, with the longest of them.
type ConnectionPressure struct {
	Waits         int64
	WaitTime      time.Duration
	SlowHolds     int64
	LongestHold   time.Duration
	LongestHoldOp string
}

// holdTracker times the journal connection's holds. now is a field so a
// test can stand a hold still for as long as it likes.
type holdTracker struct {
	now   func() time.Time
	hook  atomic.Pointer[func(SlowHold)]
	stats func() sql.DBStats

	mu        sync.Mutex
	slowHolds int64
	longest   time.Duration
	longestOp string
}

func newHoldTracker() *holdTracker {
	return &holdTracker{now: time.Now}
}

// hold is one hold in progress.
type hold struct {
	t       *holdTracker
	op      string
	started time.Time
	waits   int64
}

func (t *holdTracker) begin(query string) *hold {
	h := &hold{t: t, op: opOf(query), started: t.now()}
	if t.stats != nil {
		h.waits = t.stats().WaitCount
	}
	return h
}

// end closes the hold and reports it if it was slow. It runs while the
// connection is still held, so the hook must not touch the journal.
func (h *hold) end() {
	if h == nil {
		return
	}
	t := h.t
	held := t.now().Sub(h.started)
	if held < SlowHoldThreshold {
		return
	}
	var waiters int64
	if t.stats != nil {
		waiters = t.stats().WaitCount - h.waits
	}
	t.mu.Lock()
	t.slowHolds++
	if held > t.longest {
		t.longest, t.longestOp = held, h.op
	}
	t.mu.Unlock()
	if fn := t.hook.Load(); fn != nil {
		(*fn)(SlowHold{Op: h.op, Held: held, Waiters: waiters})
	}
}

// opOf folds a statement's whitespace and cuts it to opMax.
func opOf(query string) string {
	op := strings.Join(strings.Fields(query), " ")
	if len(op) > opMax {
		op = op[:opMax]
	}
	return op
}

// SetSlowHoldHook installs fn to be told about every hold of the journal's
// connection past SlowHoldThreshold, or turns that off when fn is nil. fn
// runs while the connection is still held, so it must not use the journal.
func (j *Journal) SetSlowHoldHook(fn func(SlowHold)) {
	if fn == nil {
		j.pressure.hook.Store(nil)
		return
	}
	j.pressure.hook.Store(&fn)
}

// Pressure reports what the connection has cost its callers since Open.
func (j *Journal) Pressure() ConnectionPressure {
	st := j.db.Stats()
	t := j.pressure
	t.mu.Lock()
	defer t.mu.Unlock()
	return ConnectionPressure{
		Waits: st.WaitCount, WaitTime: st.WaitDuration,
		SlowHolds: t.slowHolds, LongestHold: t.longest, LongestHoldOp: t.longestOp,
	}
}

// IsContention reports whether err is the journal losing a race rather
// than failing: a statement that ran out of its deadline (waiting for the
// connection, or for the write lock under busy_timeout), or SQLite saying
// the database or a table is locked by another connection. A retry fixes
// those. A cancelled caller is not contention, it is a caller that left,
// and a disk, space, permission or schema fault is not either: retrying it
// quietly would hide the faults that matter most.
func IsContention(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var sqliteErr *sqlitedriver.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() & 0xff {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
			return true
		}
	}
	return false
}

// timedConnector opens the journal's connection through the wrapper.
type timedConnector struct {
	name    string
	drv     driver.Driver
	tracker *holdTracker
}

func (c *timedConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.name)
	if err != nil {
		return nil, err
	}
	return &timedConn{Conn: conn, t: c.tracker}, nil
}

func (c *timedConnector) Driver() driver.Driver { return c.drv }

// timedConn is one connection. tx is the transaction it is inside, if any:
// statements inside one are part of its hold and are not timed on their
// own. database/sql never uses one driver connection from two goroutines at
// once, so the field needs no lock of its own.
type timedConn struct {
	driver.Conn
	t  *holdTracker
	tx *timedTx
}

// statement starts timing a statement, or names the open transaction with
// it when it is the transaction's first. The returned hold is nil inside a
// transaction.
func (c *timedConn) statement(query string) *hold {
	if c.tx != nil {
		if c.tx.h.op == "transaction" {
			c.tx.h.op = "transaction: " + opOf(query)
			if len(c.tx.h.op) > opMax {
				c.tx.h.op = c.tx.h.op[:opMax]
			}
		}
		return nil
	}
	return c.t.begin(query)
}

func (c *timedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	ex, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	h := c.statement(query)
	defer h.end()
	return ex.ExecContext(ctx, query, args)
}

func (c *timedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	h := c.statement(query)
	rows, err := q.QueryContext(ctx, query, args)
	if err != nil {
		h.end()
		return nil, err
	}
	return &timedRows{Rows: rows, h: h}, nil
}

func (c *timedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, query)
	}
	return c.Prepare(query)
}

func (c *timedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	b, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("state: the sqlite driver cannot begin a transaction with a context")
	}
	h := c.t.begin("transaction")
	tx, err := b.BeginTx(ctx, opts)
	if err != nil {
		h.end()
		return nil, err
	}
	c.tx = &timedTx{Tx: tx, c: c, h: h}
	return c.tx, nil
}

func (c *timedConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *timedConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *timedConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// timedTx ends its connection's transaction hold.
type timedTx struct {
	driver.Tx
	c *timedConn
	h *hold
}

func (t *timedTx) Commit() error {
	defer t.done()
	return t.Tx.Commit()
}

func (t *timedTx) Rollback() error {
	defer t.done()
	return t.Tx.Rollback()
}

func (t *timedTx) done() {
	if t.c.tx == t {
		t.c.tx = nil
	}
	t.h.end()
}

// timedRows ends a cursor's hold at Close, and forwards the column type
// questions the sqlite driver answers.
type timedRows struct {
	driver.Rows
	h    *hold
	once sync.Once
}

func (r *timedRows) Close() error {
	err := r.Rows.Close()
	r.once.Do(r.h.end)
	return err
}

func (r *timedRows) ColumnTypeDatabaseTypeName(index int) string {
	if c, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return c.ColumnTypeDatabaseTypeName(index)
	}
	return ""
}

func (r *timedRows) ColumnTypeLength(index int) (int64, bool) {
	if c, ok := r.Rows.(driver.RowsColumnTypeLength); ok {
		return c.ColumnTypeLength(index)
	}
	return 0, false
}

func (r *timedRows) ColumnTypeNullable(index int) (bool, bool) {
	if c, ok := r.Rows.(driver.RowsColumnTypeNullable); ok {
		return c.ColumnTypeNullable(index)
	}
	return false, false
}

func (r *timedRows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	if c, ok := r.Rows.(driver.RowsColumnTypePrecisionScale); ok {
		return c.ColumnTypePrecisionScale(index)
	}
	return 0, 0, false
}

func (r *timedRows) ColumnTypeScanType(index int) reflect.Type {
	if c, ok := r.Rows.(driver.RowsColumnTypeScanType); ok {
		return c.ColumnTypeScanType(index)
	}
	return reflect.TypeOf(new(any)).Elem()
}

var (
	_ driver.ExecerContext      = (*timedConn)(nil)
	_ driver.QueryerContext     = (*timedConn)(nil)
	_ driver.ConnPrepareContext = (*timedConn)(nil)
	_ driver.ConnBeginTx        = (*timedConn)(nil)
	_ driver.Pinger             = (*timedConn)(nil)
	_ driver.SessionResetter    = (*timedConn)(nil)
	_ driver.Validator          = (*timedConn)(nil)
)
