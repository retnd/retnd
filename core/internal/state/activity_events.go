package state

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The durable half of the activity feed (migration 0018). service/
// liveactivity_durable.go decides what is worth keeping and how often to
// write it; this file is only the table's reads and writes.

// ActivityEventRow is one stored feed event. Fields is the JSON array the
// feed rendered (already redacted); this package does not look inside it.
type ActivityEventRow struct {
	Seq      int64
	At       time.Time
	Level    string
	Event    string
	Scope    string
	SetID    string
	OpID     string
	Message  string
	Result   string
	Action   string
	ActionID string
	Fields   string

	// Coalesce replaces any stored row with the same Event and ActionID
	// before this one is written, so a stream of "stage" lines for one
	// operation keeps only the latest. It is ignored when ActionID is
	// empty, because there is then nothing to say which rows are the same
	// stream.
	Coalesce bool
}

// AppendActivityEvents writes rows in one transaction.
func (j *Journal) AppendActivityEvents(ctx context.Context, rows []ActivityEventRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: append activity events: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range rows {
		if r.Seq <= 0 {
			return errors.New("state: an activity event needs a positive sequence")
		}
		if r.Coalesce && r.ActionID != "" {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM activity_events WHERE event = ? AND action_id = ?`, r.Event, r.ActionID); err != nil {
				return fmt.Errorf("state: coalesce activity events: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO activity_events
				(seq, at, level, event, scope, set_id, op_id, message, result, action, action_id, fields)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Seq, formatTime(r.At), r.Level, r.Event, r.Scope, r.SetID, r.OpID, r.Message, r.Result,
			r.Action, r.ActionID, r.Fields); err != nil {
			return fmt.Errorf("state: append activity event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("state: append activity events: %w", err)
	}
	return nil
}

// MaxActivityEventSeq is the highest sequence stored, or 0.
func (j *Journal) MaxActivityEventSeq(ctx context.Context) (int64, error) {
	var n *int64
	if err := j.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM activity_events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("state: max activity sequence: %w", err)
	}
	if n == nil {
		return 0, nil
	}
	return *n, nil
}

const activityColumns = `seq, at, level, event, scope, set_id, op_id, message, result, action, action_id, fields`

func scanActivityRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]ActivityEventRow, error) {
	var out []ActivityEventRow
	for rows.Next() {
		var (
			r  ActivityEventRow
			at string
		)
		if err := rows.Scan(&r.Seq, &at, &r.Level, &r.Event, &r.Scope, &r.SetID, &r.OpID, &r.Message,
			&r.Result, &r.Action, &r.ActionID, &r.Fields); err != nil {
			return nil, err
		}
		t, err := parseTime(at)
		if err != nil {
			return nil, fmt.Errorf("activity event %d has an unreadable time %q: %w", r.Seq, at, err)
		}
		r.At = t
		out = append(out, r)
	}
	return out, rows.Err()
}

// bucketClause selects one bucket: a backup set by id, or the events that
// belong to no set.
func bucketClause(setID string) (string, []any) {
	if setID == "" {
		return `scope = 'deployment'`, nil
	}
	return `set_id = ?`, []any{setID}
}

// RecentActivityEvents returns the newest limit events of one bucket,
// oldest first. An empty setID is the deployment's own bucket.
func (j *Journal) RecentActivityEvents(ctx context.Context, setID string, limit int) ([]ActivityEventRow, error) {
	if limit <= 0 {
		return nil, errors.New("state: RecentActivityEvents needs a positive limit")
	}
	clause, args := bucketClause(setID)
	rows, err := j.db.QueryContext(ctx,
		`SELECT `+activityColumns+` FROM activity_events WHERE `+clause+` ORDER BY seq DESC LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("state: recent activity events: %w", err)
	}
	defer rows.Close()
	out, err := scanActivityRows(rows)
	if err != nil {
		return nil, fmt.Errorf("state: recent activity events: %w", err)
	}
	reverseActivity(out)
	return out, nil
}

// ActivityEventsBefore returns up to limit events of one bucket with a
// sequence below before, oldest first, and whether older ones remain.
func (j *Journal) ActivityEventsBefore(ctx context.Context, setID string, before int64, limit int) ([]ActivityEventRow, bool, error) {
	if limit <= 0 {
		return nil, false, errors.New("state: ActivityEventsBefore needs a positive limit")
	}
	clause, args := bucketClause(setID)
	rows, err := j.db.QueryContext(ctx,
		`SELECT `+activityColumns+` FROM activity_events WHERE `+clause+` AND seq < ? ORDER BY seq DESC LIMIT ?`,
		append(args, before, limit+1)...)
	if err != nil {
		return nil, false, fmt.Errorf("state: activity events before: %w", err)
	}
	defer rows.Close()
	out, err := scanActivityRows(rows)
	if err != nil {
		return nil, false, fmt.Errorf("state: activity events before: %w", err)
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	reverseActivity(out)
	return out, more, nil
}

// ActivityEventsOfOperation returns every stored event carrying op_id,
// oldest first.
func (j *Journal) ActivityEventsOfOperation(ctx context.Context, opID string) ([]ActivityEventRow, error) {
	if opID == "" {
		return nil, errors.New("state: ActivityEventsOfOperation needs an operation id")
	}
	rows, err := j.db.QueryContext(ctx,
		`SELECT `+activityColumns+` FROM activity_events WHERE op_id = ? ORDER BY seq`, opID)
	if err != nil {
		return nil, fmt.Errorf("state: activity events of operation: %w", err)
	}
	defer rows.Close()
	return scanActivityRows(rows)
}

func reverseActivity(r []ActivityEventRow) {
	for i, k := 0, len(r)-1; i < k; i, k = i+1, k-1 {
		r[i], r[k] = r[k], r[i]
	}
}

// PruneActivityEvents applies the retention rule: rows older than
// olderThan go, and then, if more than maxRows remain, the oldest of the
// rest. It returns how many it removed.
func (j *Journal) PruneActivityEvents(ctx context.Context, olderThan time.Time, maxRows int) (int64, error) {
	if maxRows <= 0 {
		return 0, errors.New("state: PruneActivityEvents needs a positive row bound")
	}
	res, err := j.db.ExecContext(ctx, `DELETE FROM activity_events WHERE at < ?`, formatTime(olderThan))
	if err != nil {
		return 0, fmt.Errorf("state: prune activity events by age: %w", err)
	}
	n, _ := res.RowsAffected()
	res, err = j.db.ExecContext(ctx, `
		DELETE FROM activity_events
		 WHERE seq <= COALESCE((SELECT seq FROM activity_events ORDER BY seq DESC LIMIT 1 OFFSET ?), 0)`, maxRows)
	if err != nil {
		return n, fmt.Errorf("state: prune activity events by count: %w", err)
	}
	m, _ := res.RowsAffected()
	return n + m, nil
}
