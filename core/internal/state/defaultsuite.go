package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/retnd/retnd/core/internal/copycrypt"
)

// NoteDefaultSuite records current as the build default suite this
// deployment runs under (migration 0021) and reports whether that is a
// change worth announcing, with the previous value (EPIC T, #1314).
//
// It is a compare-and-set in one transaction, so two callers cannot both
// announce one change. The first time a deployment that has ever held an
// encryption key meets this, the previous value is EPIC N's implicit
// default, age/v1; a deployment that never held a key records current
// silently, because nothing it does changed.
func (j *Journal) NoteDefaultSuite(ctx context.Context, current string, at time.Time) (previous string, changed bool, err error) {
	if current == "" {
		return "", false, fmt.Errorf("state: note default suite: the suite id must not be empty")
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("state: note default suite: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed

	var recorded string
	err = tx.QueryRowContext(ctx, `SELECT suite_id FROM encryption_default_suite WHERE id = 1`).Scan(&recorded)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var keys int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM encryption_keys`).Scan(&keys); err != nil {
			return "", false, fmt.Errorf("state: note default suite: %w", err)
		}
		if keys > 0 {
			previous = copycrypt.AgeX25519ID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO encryption_default_suite (id, suite_id, recorded_at) VALUES (1, ?, ?)`,
			current, formatTime(at)); err != nil {
			return "", false, fmt.Errorf("state: note default suite: %w", err)
		}
	case err != nil:
		return "", false, fmt.Errorf("state: note default suite: %w", err)
	case recorded == current:
		return recorded, false, nil
	default:
		previous = recorded
		if _, err := tx.ExecContext(ctx, `UPDATE encryption_default_suite SET suite_id = ?, recorded_at = ? WHERE id = 1`,
			current, formatTime(at)); err != nil {
			return "", false, fmt.Errorf("state: note default suite: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("state: note default suite: %w", err)
	}
	return previous, previous != "" && previous != current, nil
}
