package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RecordEncryptExisting is the durable record that an operator asked for this
// move to encrypt a plaintext copy that already exists (#1291, migration
// 0019). It is bound to the move id, so it is never inherited by a later move
// of the same artifact, and it is refused for a move that does not exist or
// has already finished: a finished move's opt-in would be a standing
// permission nothing is waiting on. Recording it again is a no-op, which is
// what lets a resumed run record it without asking whether it already did.
func (j *Journal) RecordEncryptExisting(ctx context.Context, moveID int64, at time.Time) error {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: begin record encrypt-existing: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit has succeeded

	var phase string
	err = tx.QueryRowContext(ctx, `SELECT phase FROM placement_moves WHERE id = ?`, moveID).Scan(&phase)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %d", ErrMoveNotFound, moveID)
	}
	if err != nil {
		return fmt.Errorf("state: record encrypt-existing: %w", err)
	}
	if phase == MoveDone || phase == MoveAbandoned {
		return fmt.Errorf("state: move %d is %s, and an opt-in for a finished move is a permission nothing is waiting on", moveID, phase)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO encrypt_existing_consents (move_id, consented_at) VALUES (?, ?)`,
		moveID, formatTime(at)); err != nil {
		return fmt.Errorf("state: record encrypt-existing for move %d: %w", moveID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("state: commit encrypt-existing for move %d: %w", moveID, err)
	}
	return nil
}

// EncryptExistingConsented reports whether an operator opted this move in.
// False is the answer for a move nobody opted in, a move that does not exist
// and a move a retention cycle planned; none of them is an error.
func (j *Journal) EncryptExistingConsented(ctx context.Context, moveID int64) (bool, error) {
	var one int
	err := j.db.QueryRowContext(ctx, `SELECT 1 FROM encrypt_existing_consents WHERE move_id = ?`, moveID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("state: read encrypt-existing consent of move %d: %w", moveID, err)
	}
	return true, nil
}
