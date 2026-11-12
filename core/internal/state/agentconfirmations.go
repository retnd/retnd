package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The rows behind EPIC U's confirmations (migration 0022, #1349; FR-95).
//
// A confirmation is the request an agent's tool call would have made, kept
// byte for byte until a person approves or denies it. Nothing here caches a
// state: every read comes from the row, and a decision is one conditional
// UPDATE, so two approvals racing for the same confirmation cannot both win.

// The four states a confirmation moves through. pending is the only one that
// can change; the other three are final.
const (
	AgentConfirmationPending  = "pending"
	AgentConfirmationApproved = "approved"
	AgentConfirmationDenied   = "denied"
	AgentConfirmationExpired  = "expired"
)

// ErrAgentConfirmationLimit is returned by InsertAgentConfirmation when the
// token already has the most open confirmations it is allowed.
var ErrAgentConfirmationLimit = errors.New("state: the token has too many pending confirmations")

// ErrAgentConfirmationNotFound is returned when an id names no confirmation.
var ErrAgentConfirmationNotFound = errors.New("state: no such agent confirmation")

// ErrAgentTokenNotFound is returned by GetAgentToken for an unknown id.
var ErrAgentTokenNotFound = errors.New("state: no such agent token")

// AgentConfirmation is one row of agent_confirmations. Body is the request
// body exactly as the tool call would have sent it.
type AgentConfirmation struct {
	ID        string
	TokenID   string
	Tool      string
	Route     string
	Method    string
	Body      []byte
	Command   string
	BackupSet string
	State     string
	CreatedAt time.Time
	ExpiresAt time.Time
	DecidedAt *time.Time
	DecidedBy string
}

const agentConfirmationColumns = `id, token_id, tool, route, method, body, command, backup_set, state, created_at, expires_at, decided_at, decided_by`

// InsertAgentConfirmation stores a pending confirmation together with the
// audit row that records it, in one transaction, unless the token already has
// maxPending open ones. Open means pending and not yet past its time, so a
// confirmation nobody swept does not hold a place after it has expired.
func (j *Journal) InsertAgentConfirmation(ctx context.Context, c AgentConfirmation, maxPending int, record AgentAudit) error {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: insert agent confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT expires_at FROM agent_confirmations WHERE token_id = ? AND state = ?`, c.TokenID, AgentConfirmationPending)
	if err != nil {
		return fmt.Errorf("state: insert agent confirmation: %w", err)
	}
	open := 0
	for rows.Next() {
		var expires string
		if err := rows.Scan(&expires); err != nil {
			_ = rows.Close()
			return fmt.Errorf("state: insert agent confirmation: %w", err)
		}
		at, err := parseTime(expires)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("state: insert agent confirmation: expires_at: %w", err)
		}
		if c.CreatedAt.Before(at) {
			open++
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("state: insert agent confirmation: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("state: insert agent confirmation: %w", err)
	}
	if open >= maxPending {
		return ErrAgentConfirmationLimit
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_confirmations (`+agentConfirmationColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`,
		c.ID, c.TokenID, c.Tool, c.Route, c.Method, c.Body, c.Command, c.BackupSet, AgentConfirmationPending,
		formatTime(c.CreatedAt), formatTime(c.ExpiresAt)); err != nil {
		return fmt.Errorf("state: insert agent confirmation %s: %w", c.ID, err)
	}
	if err := insertAgentAudit(ctx, tx, record); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("state: insert agent confirmation %s: %w", c.ID, err)
	}
	return nil
}

// GetAgentConfirmation reads one confirmation, or ErrAgentConfirmationNotFound.
func (j *Journal) GetAgentConfirmation(ctx context.Context, id string) (AgentConfirmation, error) {
	got, err := j.queryAgentConfirmations(ctx, `SELECT `+agentConfirmationColumns+` FROM agent_confirmations WHERE id = ?`, id)
	if err != nil {
		return AgentConfirmation{}, err
	}
	if len(got) == 0 {
		return AgentConfirmation{}, fmt.Errorf("%w: %q", ErrAgentConfirmationNotFound, id)
	}
	return got[0], nil
}

// ListAgentConfirmations returns the newest confirmations first, at most limit.
func (j *Journal) ListAgentConfirmations(ctx context.Context, limit int) ([]AgentConfirmation, error) {
	return j.queryAgentConfirmations(ctx,
		`SELECT `+agentConfirmationColumns+` FROM agent_confirmations ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
}

// DecideAgentConfirmation moves a confirmation out of pending and reports
// whether this call did it. A confirmation that is no longer pending is left
// alone and reported false, which is what makes the first decision the only
// one.
func (j *Journal) DecideAgentConfirmation(ctx context.Context, id, to, by string, at time.Time) (bool, error) {
	res, err := j.db.ExecContext(ctx,
		`UPDATE agent_confirmations SET state = ?, decided_by = ?, decided_at = ? WHERE id = ? AND state = ?`,
		to, by, formatTime(at), id, AgentConfirmationPending)
	if err != nil {
		return false, fmt.Errorf("state: decide agent confirmation %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("state: decide agent confirmation %s: %w", id, err)
	}
	return n > 0, nil
}

// ExpireAgentConfirmations marks every pending confirmation whose time has
// come as expired and returns them, so the caller can say so once.
func (j *Journal) ExpireAgentConfirmations(ctx context.Context, at time.Time) ([]AgentConfirmation, error) {
	pending, err := j.queryAgentConfirmations(ctx,
		`SELECT `+agentConfirmationColumns+` FROM agent_confirmations WHERE state = ? ORDER BY created_at, id`, AgentConfirmationPending)
	if err != nil {
		return nil, err
	}
	var expired []AgentConfirmation
	for _, c := range pending {
		if at.Before(c.ExpiresAt) {
			continue
		}
		ok, err := j.DecideAgentConfirmation(ctx, c.ID, AgentConfirmationExpired, "", at)
		if err != nil {
			return nil, err
		}
		if ok {
			c.State, c.DecidedAt = AgentConfirmationExpired, &at
			expired = append(expired, c)
		}
	}
	return expired, nil
}

func (j *Journal) queryAgentConfirmations(ctx context.Context, query string, args ...any) ([]AgentConfirmation, error) {
	rows, err := j.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("state: read agent confirmations: %w", err)
	}
	defer rows.Close()

	var out []AgentConfirmation
	for rows.Next() {
		var (
			c                 AgentConfirmation
			created, expires  string
			decidedAt, decide sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.TokenID, &c.Tool, &c.Route, &c.Method, &c.Body, &c.Command, &c.BackupSet, &c.State, &created, &expires, &decidedAt, &decide); err != nil {
			return nil, fmt.Errorf("state: read agent confirmations: %w", err)
		}
		if c.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("state: agent confirmation %s created_at: %w", c.ID, err)
		}
		if c.ExpiresAt, err = parseTime(expires); err != nil {
			return nil, fmt.Errorf("state: agent confirmation %s expires_at: %w", c.ID, err)
		}
		if c.DecidedAt, err = parseTimePtr(decidedAt); err != nil {
			return nil, fmt.Errorf("state: agent confirmation %s decided_at: %w", c.ID, err)
		}
		c.DecidedBy = decide.String
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: read agent confirmations: %w", err)
	}
	return out, nil
}

// GetAgentToken reads one token row by id, or ErrAgentTokenNotFound.
func (j *Journal) GetAgentToken(ctx context.Context, id string) (AgentToken, error) {
	got, err := j.queryAgentTokens(ctx, `SELECT `+agentTokenColumns+` FROM agent_tokens WHERE id = ?`, id)
	if err != nil {
		return AgentToken{}, err
	}
	if len(got) == 0 {
		return AgentToken{}, fmt.Errorf("%w: %q", ErrAgentTokenNotFound, id)
	}
	return got[0], nil
}

// AgentAuditForConfirmation returns the audit rows that name a confirmation,
// oldest first: the record of its creation, then its decision and the
// execution that followed an approval.
func (j *Journal) AgentAuditForConfirmation(ctx context.Context, id string) ([]AgentAudit, error) {
	rows, err := j.db.QueryContext(ctx,
		`SELECT seq, at, token_id, tool, transport, arguments, status, code, correlation_id, confirmation_id
		 FROM agent_audit WHERE confirmation_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, fmt.Errorf("state: read agent audit for %s: %w", id, err)
	}
	defer rows.Close()

	var out []AgentAudit
	for rows.Next() {
		var (
			a  AgentAudit
			at string
		)
		if err := rows.Scan(&a.Seq, &at, &a.TokenID, &a.Tool, &a.Transport, &a.Arguments, &a.Status, &a.Code, &a.CorrelationID, &a.ConfirmationID); err != nil {
			return nil, fmt.Errorf("state: read agent audit for %s: %w", id, err)
		}
		if a.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("state: agent audit %d at: %w", a.Seq, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: read agent audit for %s: %w", id, err)
	}
	return out, nil
}

// insertAgentAudit appends an audit row inside a transaction.
func insertAgentAudit(ctx context.Context, tx *sql.Tx, a AgentAudit) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO agent_audit (at, token_id, tool, transport, arguments, status, code, correlation_id, confirmation_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		formatTime(a.At), a.TokenID, a.Tool, a.Transport, a.Arguments, a.Status, a.Code, a.CorrelationID, a.ConfirmationID)
	if err != nil {
		return fmt.Errorf("state: record agent audit: %w", err)
	}
	return nil
}
