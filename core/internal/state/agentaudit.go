package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// The read, batch-write and prune sides of agent_audit (migration 0022,
// EPIC U, #1348). InsertAgentAudit and ListAgentAudit in agentaccess.go are
// the single-row forms #1345 wrote for refusals; the writer behind the tool
// calls goes through these.

// AgentAuditFilter selects a page of agent_audit, newest first. A zero field
// does not filter.
type AgentAuditFilter struct {
	TokenID string
	// Since keeps rows at or after this time.
	Since time.Time
	// Before keeps rows with a smaller seq than this: the cursor.
	Before int64
	Limit  int
}

// InsertAgentAudits appends rows in one transaction, in order, so a batch is
// either all there or not there.
func (j *Journal) InsertAgentAudits(ctx context.Context, rows []AgentAudit) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: record agent audit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO agent_audit (at, token_id, tool, transport, arguments, status, code, correlation_id, confirmation_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("state: record agent audit: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, a := range rows {
		if _, err := stmt.ExecContext(ctx, formatTime(a.At), a.TokenID, a.Tool, a.Transport, a.Arguments, a.Status, a.Code, a.CorrelationID, a.ConfirmationID); err != nil {
			return fmt.Errorf("state: record agent audit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("state: record agent audit: %w", err)
	}
	return nil
}

// AgentAuditPage returns the rows the filter selects, newest first.
func (j *Journal) AgentAuditPage(ctx context.Context, f AgentAuditFilter) ([]AgentAudit, error) {
	var (
		where []string
		args  []any
	)
	if f.TokenID != "" {
		where = append(where, "a.token_id = ?")
		args = append(args, f.TokenID)
	}
	if !f.Since.IsZero() {
		where = append(where, "a.at >= ?")
		args = append(args, formatTime(f.Since))
	}
	if f.Before > 0 {
		where = append(where, "a.seq < ?")
		args = append(args, f.Before)
	}
	// The name is a read-time join (tokens are never deleted, so it is stable),
	// NULL when the row's token matched nothing.
	q := `SELECT a.seq, a.at, a.token_id, COALESCE(t.name, ''), a.tool, a.transport, a.arguments, a.status, a.code, a.correlation_id, a.confirmation_id
		FROM agent_audit a LEFT JOIN agent_tokens t ON t.id = a.token_id`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY a.seq DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := j.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("state: read agent audit: %w", err)
	}
	defer rows.Close()
	var out []AgentAudit
	for rows.Next() {
		var (
			a  AgentAudit
			at string
		)
		if err := rows.Scan(&a.Seq, &at, &a.TokenID, &a.TokenName, &a.Tool, &a.Transport, &a.Arguments, &a.Status, &a.Code, &a.CorrelationID, &a.ConfirmationID); err != nil {
			return nil, fmt.Errorf("state: read agent audit: %w", err)
		}
		if a.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("state: agent audit %d at: %w", a.Seq, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: read agent audit: %w", err)
	}
	return out, nil
}

// PruneAgentAuditByAge deletes at most batch rows written before cutoff,
// oldest first, and reports how many went. A caller loops until it gets less
// than batch. Rows are appended in time order, so seq order is age order and
// the walk stops at the first row that is young enough; the table has no
// index on at and this does not need one.
func (j *Journal) PruneAgentAuditByAge(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	var oldest sql.NullString
	err := j.db.QueryRowContext(ctx, `SELECT at FROM agent_audit ORDER BY seq LIMIT 1`).Scan(&oldest)
	if err == sql.ErrNoRows || (err == nil && oldest.String >= formatTime(cutoff)) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("state: prune agent audit: %w", err)
	}
	res, err := j.db.ExecContext(ctx,
		`DELETE FROM agent_audit WHERE seq IN (SELECT seq FROM agent_audit WHERE at < ? ORDER BY seq LIMIT ?)`,
		formatTime(cutoff), batch)
	if err != nil {
		return 0, fmt.Errorf("state: prune agent audit: %w", err)
	}
	return res.RowsAffected()
}

// PruneAgentAuditOverCount deletes at most batch of the oldest rows beyond
// keep. It is the second bound: an unauthenticated caller can write a refusal
// row per request, and a retention in days alone does not stop that filling a
// disk in the first day.
func (j *Journal) PruneAgentAuditOverCount(ctx context.Context, keep, batch int) (int64, error) {
	res, err := j.db.ExecContext(ctx,
		`DELETE FROM agent_audit WHERE seq IN (
			SELECT seq FROM agent_audit WHERE seq <= (SELECT COALESCE(MAX(seq), 0) FROM agent_audit) - ? ORDER BY seq LIMIT ?)`,
		keep, batch)
	if err != nil {
		return 0, fmt.Errorf("state: prune agent audit: %w", err)
	}
	return res.RowsAffected()
}
