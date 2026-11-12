package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The rows behind EPIC U's agent principal (migration 0022, #1345; ADR 0034).
//
// A token's status is read from its row on every request. Nothing in this
// file caches a token, a hash or a decision, which is what lets a revoke
// take effect on the very next request rather than on the next restart.

// ErrAgentTokenNameTaken is returned by InsertAgentToken when a token that can
// still authenticate already carries that name. A revoked token frees its
// name, because the name is the agent:<name> principal an operator reads.
var ErrAgentTokenNameTaken = errors.New("state: an agent token with that name already exists")

// AgentToken is one row of agent_tokens. Hash is the SHA-256 of the token as
// lowercase hex; the token itself is never stored and this type has no field
// that could hold it.
type AgentToken struct {
	ID         string
	Name       string
	Prefix     string
	Hash       string
	Scope      string
	Owner      string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// AgentAudit is one row of agent_audit. TokenID is empty when the presented
// token matched no row.
type AgentAudit struct {
	Seq            int64
	At             time.Time
	TokenID        string
	Tool           string
	Transport      string
	Arguments      string
	Status         int
	Code           string
	CorrelationID  string
	ConfirmationID string
}

const agentTokenColumns = `id, name, prefix, hash, scope, owner, created_at, expires_at, last_used_at, revoked_at`

// InsertAgentToken stores a new token row.
func (j *Journal) InsertAgentToken(ctx context.Context, t AgentToken) error {
	_, err := j.db.ExecContext(ctx,
		`INSERT INTO agent_tokens (`+agentTokenColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`,
		t.ID, t.Name, t.Prefix, t.Hash, t.Scope, t.Owner, formatTime(t.CreatedAt), formatTime(t.ExpiresAt))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("state: insert agent token %q: %w", t.Name, ErrAgentTokenNameTaken)
		}
		return fmt.Errorf("state: insert agent token %q: %w", t.Name, err)
	}
	return nil
}

// ListAgentTokens returns every token row, oldest first.
func (j *Journal) ListAgentTokens(ctx context.Context) ([]AgentToken, error) {
	return j.queryAgentTokens(ctx, `SELECT `+agentTokenColumns+` FROM agent_tokens ORDER BY created_at, id`)
}

// AgentTokensByPrefix returns the rows whose prefix matches, which is how an
// authenticating request finds the hash to compare against.
func (j *Journal) AgentTokensByPrefix(ctx context.Context, prefix string) ([]AgentToken, error) {
	return j.queryAgentTokens(ctx, `SELECT `+agentTokenColumns+` FROM agent_tokens WHERE prefix = ? ORDER BY id`, prefix)
}

func (j *Journal) queryAgentTokens(ctx context.Context, query string, args ...any) ([]AgentToken, error) {
	rows, err := j.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("state: read agent tokens: %w", err)
	}
	defer rows.Close()

	var out []AgentToken
	for rows.Next() {
		var (
			t                 AgentToken
			created, expires  string
			lastUsed, revoked sql.NullString
		)
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.Hash, &t.Scope, &t.Owner, &created, &expires, &lastUsed, &revoked); err != nil {
			return nil, fmt.Errorf("state: read agent tokens: %w", err)
		}
		if t.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("state: agent token %s created_at: %w", t.ID, err)
		}
		if t.ExpiresAt, err = parseTime(expires); err != nil {
			return nil, fmt.Errorf("state: agent token %s expires_at: %w", t.ID, err)
		}
		if t.LastUsedAt, err = parseTimePtr(lastUsed); err != nil {
			return nil, fmt.Errorf("state: agent token %s last_used_at: %w", t.ID, err)
		}
		if t.RevokedAt, err = parseTimePtr(revoked); err != nil {
			return nil, fmt.Errorf("state: agent token %s revoked_at: %w", t.ID, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: read agent tokens: %w", err)
	}
	return out, nil
}

// RevokeAgentToken marks a token revoked and reports whether the id exists.
// Revoking twice keeps the first time: the record says when access ended, and
// a second revoke must not move it.
func (j *Journal) RevokeAgentToken(ctx context.Context, id string, at time.Time) (bool, error) {
	res, err := j.db.ExecContext(ctx,
		`UPDATE agent_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, formatTime(at), id)
	if err != nil {
		return false, fmt.Errorf("state: revoke agent token %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("state: revoke agent token %s: %w", id, err)
	}
	return n > 0, nil
}

// TouchAgentToken records that a token authenticated a request.
func (j *Journal) TouchAgentToken(ctx context.Context, id string, at time.Time) error {
	if _, err := j.db.ExecContext(ctx, `UPDATE agent_tokens SET last_used_at = ? WHERE id = ?`, formatTime(at), id); err != nil {
		return fmt.Errorf("state: touch agent token %s: %w", id, err)
	}
	return nil
}

// InsertAgentAudit appends one row to agent_audit.
func (j *Journal) InsertAgentAudit(ctx context.Context, a AgentAudit) error {
	_, err := j.db.ExecContext(ctx,
		`INSERT INTO agent_audit (at, token_id, tool, transport, arguments, status, code, correlation_id, confirmation_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		formatTime(a.At), a.TokenID, a.Tool, a.Transport, a.Arguments, a.Status, a.Code, a.CorrelationID, a.ConfirmationID)
	if err != nil {
		return fmt.Errorf("state: record agent audit: %w", err)
	}
	return nil
}

// ListAgentAudit returns the newest rows first, at most limit of them.
func (j *Journal) ListAgentAudit(ctx context.Context, limit int) ([]AgentAudit, error) {
	rows, err := j.db.QueryContext(ctx,
		`SELECT seq, at, token_id, tool, transport, arguments, status, code, correlation_id, confirmation_id
		 FROM agent_audit ORDER BY seq DESC LIMIT ?`, limit)
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
		if err := rows.Scan(&a.Seq, &at, &a.TokenID, &a.Tool, &a.Transport, &a.Arguments, &a.Status, &a.Code, &a.CorrelationID, &a.ConfirmationID); err != nil {
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
