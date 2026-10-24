package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/retnd/retnd/core/internal/model"
)

// The recovery-test claim's writes (migration 0016, EPIC Q, #980).
//
// # What the claim is
//
// A scrub says a copy's bytes are still what was recorded. A recovery test
// says more: the copy was recovered to an isolated place, the recovered bytes
// were read end to end and matched the artifact's recorded digest, and only
// then did the operator's own validator look at them and accept. The two are
// separate columns with separate ages because passing one says nothing about
// the other: a copy can be Content verified and Recovery test failed, and
// that is the reading an operator needs, not a blended tick.
//
// # The rules the write path holds, so no caller has to
//
//   - A pass is storable only when the digest was compared and matched, the
//     validator is named and hashed, and the validator's process ended on its
//     own. A validator that was killed, or whose kill was not observed, did
//     not give a verdict; it is a failure, and recording it as anything else
//     is exactly how a hung script becomes a green tick.
//   - A digest mismatch is a failure that never ran the validator, and says
//     so by carrying no validator identity.
//   - Refused-for-cost and could-not-complete are not verdicts about the
//     copy. They record the attempt and leave the last verdict's evidence
//     (its age, its validator, its digest result) as it was, so the claim
//     keeps ageing honestly instead of being erased by an attempt that did
//     not run.
//   - The cleanup of the recovered copy is its own column. A cleanup failure
//     means a disk is filling, a test failure means the claim is unproven,
//     and neither is allowed to stand in for the other.

// CopyRef names one copy: an artifact and the medium its bytes are on.
type CopyRef struct {
	Artifact model.ArtifactID
	Medium   string
}

func (c CopyRef) validate() error {
	if c.Artifact.Set.IsZero() || c.Artifact.Name == "" || c.Medium == "" {
		return errors.New("state: a copy is named by an artifact and a medium")
	}
	return nil
}

// RecoveryTest is one recovery-test attempt's outcome for one copy.
type RecoveryTest struct {
	Copy CopyRef

	// State is one of RecoveryPassed, RecoveryFailed, RecoveryRefusedForCost
	// or RecoveryCouldNotComplete. The other values of the vocabulary are
	// evaluations (RecordRecoveryEvaluation, MarkRecoveryOverdue), not
	// results.
	State string

	// At is when the attempt ran. NextDue is when the copy is next owed one.
	At      time.Time
	NextDue time.Time

	// Reason is bounded free text for a failure or a refusal.
	Reason string

	// ValidatorID is the operator's name for the validator, a short
	// printable string and never a path. ValidatorHash is the hex SHA-256
	// of the validator's bytes, so a changed validator is a different
	// recorded identity.
	ValidatorID   string
	ValidatorHash string

	// DigestMatched is the gate: nil means the digest was never compared.
	DigestMatched *bool

	// Termination says how the validator's process ended and how sure
	// anyone is. OutputTruncated says its captured output was cut.
	Termination     string
	OutputTruncated bool

	// CleanupState says whether the recovered copy was removed.
	CleanupState string

	// Expect is the identity of the bytes the test staged and hashed. A
	// verdict is a statement about those bytes, so it is written only while
	// the copy still holds them (ErrCopyChanged otherwise). Nil means
	// unguarded, which is for tests; the runner always sets it.
	Expect *CopyIdentity

	BytesRead *int64
	Duration  time.Duration
}

const maxValidatorIDBytes = 128

func validHex256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
			return false
		}
	}
	return true
}

func (r RecoveryTest) validate() error {
	if err := r.Copy.validate(); err != nil {
		return err
	}
	if r.At.IsZero() || r.NextDue.IsZero() {
		return errors.New("state: a recovery test result needs the time it ran and the time the copy is next due")
	}
	switch r.Termination {
	case "", RecoveryTerminationExited, RecoveryTerminationKilledGroup, RecoveryTerminationUnconfirmed:
	default:
		return fmt.Errorf("state: %q is not a validator termination", r.Termination)
	}
	switch r.CleanupState {
	case "", RecoveryCleanupRemoved, RecoveryCleanupFailed:
	default:
		return fmt.Errorf("state: %q is not a cleanup state", r.CleanupState)
	}
	if len(r.ValidatorID) > maxValidatorIDBytes || !printable(r.ValidatorID) {
		return fmt.Errorf("state: a validator id is a short printable name, not %d bytes of free text", len(r.ValidatorID))
	}
	if r.ValidatorHash != "" && !validHex256(r.ValidatorHash) {
		return errors.New("state: a validator hash is the 64 hex characters of a SHA-256")
	}

	hasValidator := r.ValidatorID != "" || r.ValidatorHash != "" || r.Termination != ""
	switch r.State {
	case RecoveryPassed:
		switch {
		case r.DigestMatched == nil || !*r.DigestMatched:
			return errors.New("state: a recovery test passes only when the recovered bytes were read and matched the artifact's digest first")
		case r.ValidatorID == "" || r.ValidatorHash == "":
			return errors.New("state: a recovery test pass names the validator and the hash of what ran")
		case r.Termination != RecoveryTerminationExited:
			return fmt.Errorf("state: a validator that did not end on its own (%q) gave no verdict, so it cannot be a pass", r.Termination)
		}
	case RecoveryFailed:
		switch {
		case r.DigestMatched == nil:
			return errors.New("state: a recovery test fails on a digest result or a validator verdict; with neither, it could not complete")
		case !*r.DigestMatched && hasValidator:
			return errors.New("state: a digest mismatch ends the test before the validator, so it carries no validator identity")
		case *r.DigestMatched && (r.ValidatorID == "" || r.ValidatorHash == "" || r.Termination == ""):
			return errors.New("state: a validator failure names the validator, its hash and how it ended")
		}
	case RecoveryRefusedForCost, RecoveryCouldNotComplete:
		if r.DigestMatched != nil || hasValidator {
			return fmt.Errorf("state: a %s attempt reached no verdict, so it carries no digest result or validator", r.State)
		}
	default:
		return fmt.Errorf("state: %q is not a recorded recovery test result", r.State)
	}
	return nil
}

// RecordRecoveryTest durably writes one attempt's outcome onto its copy. It
// never touches the scrub claim.
//
// The claim lives in placement_assurance, keyed by the placement, and the
// first result creates that row. When r.Expect is set and the copy no longer
// holds those bytes, nothing is written and the answer is ErrCopyChanged: a
// "passed" stamped on bytes the artifact no longer has is the stale claim
// this record exists to rule out.
func (j *Journal) RecordRecoveryTest(ctx context.Context, r RecoveryTest) error {
	if err := r.validate(); err != nil {
		return err
	}
	reason := boundReason(r.Reason)
	at, next := formatTime(r.At), formatTime(r.NextDue)
	durMS := r.Duration.Milliseconds()

	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: record recovery test of %s on %s: %w", r.Copy.Artifact, r.Copy.Medium, err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed

	id, err := lockedActiveCopy(ctx, tx, r.Copy, r.Expect)
	if err != nil {
		return err
	}

	switch r.State {
	case RecoveryPassed, RecoveryFailed:
		var matched any
		if r.DigestMatched != nil {
			matched = boolToInt(*r.DigestMatched)
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE placement_assurance SET
				recovery_state = ?, recovery_tested_at = ?, recovery_next_due_at = ?,
				recovery_failure_reason = ?,
				recovery_validator_id = ?, recovery_validator_hash = ?,
				recovery_digest_matched = ?, recovery_termination = ?, recovery_output_truncated = ?,
				recovery_cleanup_state = ?, recovery_bytes_read = ?, recovery_duration_ms = ?
			 WHERE placement_id = ?`,
			r.State, at, next, reason, r.ValidatorID, r.ValidatorHash, matched, r.Termination,
			boolToInt(r.OutputTruncated), r.CleanupState, optionalInt64(r.BytesRead), durMS, id)
	default:
		// No verdict: the last verdict's evidence stays as it was.
		_, err = tx.ExecContext(ctx, `
			UPDATE placement_assurance SET
				recovery_state = ?, recovery_next_due_at = ?, recovery_failure_reason = ?,
				recovery_cleanup_state = ?, recovery_duration_ms = ?
			 WHERE placement_id = ?`,
			r.State, next, reason, r.CleanupState, durMS, id)
	}
	if err != nil {
		return fmt.Errorf("state: record recovery test of %s on %s: %w", r.Copy.Artifact, r.Copy.Medium, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("state: record recovery test of %s on %s: %w", r.Copy.Artifact, r.Copy.Medium, err)
	}
	return nil
}

// lockedActiveCopy finds the copy's placement inside tx, refuses one that is
// retired (ErrPlacementRetired) or whose bytes moved since expect was taken
// (ErrCopyChanged), makes sure it has a placement_assurance row, and returns
// the placement id.
func lockedActiveCopy(ctx context.Context, tx *sql.Tx, c CopyRef, expect *CopyIdentity) (int64, error) {
	var (
		id     int64
		status string
		cur    CopyIdentity
	)
	err := tx.QueryRowContext(ctx, `
		SELECT p.id, p.status, p.location, p.hash, p.hash_alg
		  FROM placements p JOIN artifacts a ON a.id = p.artifact_id
		 WHERE a.source = ? AND a.backup_set = ? AND a.artifact_name = ? AND p.medium = ?`,
		c.Artifact.Set.Source, c.Artifact.Set.Set, c.Artifact.Name, c.Medium,
	).Scan(&id, &status, &cur.Location, &cur.Hash, &cur.HashAlg)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: %s has no copy on %q", ErrPlacementNotFound, c.Artifact, c.Medium)
	}
	if err != nil {
		return 0, fmt.Errorf("state: find the copy of %s on %s: %w", c.Artifact, c.Medium, err)
	}
	if status != PlacementActive {
		return 0, fmt.Errorf("%w: %s on %q is %s", ErrPlacementRetired, c.Artifact, c.Medium, status)
	}
	if expect != nil && *expect != cur {
		return 0, fmt.Errorf("%w: %s on %q", ErrCopyChanged, c.Artifact, c.Medium)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO placement_assurance (placement_id) VALUES (?) ON CONFLICT (placement_id) DO NOTHING`, id); err != nil {
		return 0, fmt.Errorf("state: open the assurance record of %s on %s: %w", c.Artifact, c.Medium, err)
	}
	return id, nil
}

// ErrRecoveryHistory is returned when an evaluation would overwrite a
// recorded result. An evaluation says what a copy's claim is when nothing has
// been tried; it must never erase a pass or a failure.
var ErrRecoveryHistory = errors.New("state: this copy already has a recovery test result, and an evaluation does not overwrite one")

// RecordRecoveryEvaluation records the two states that are statements about
// configuration and history and not about a test: RecoveryNotConfigured (no
// validator is declared) and RecoveryNeverRun (one is, and nothing has run).
// It only moves a copy that has no result yet.
func (j *Journal) RecordRecoveryEvaluation(ctx context.Context, c CopyRef, st string) error {
	if st != RecoveryNotConfigured && st != RecoveryNeverRun {
		return fmt.Errorf("state: %q is not an evaluation (not_configured or never_run)", st)
	}
	if err := c.validate(); err != nil {
		return err
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: evaluate recovery claim of %s on %s: %w", c.Artifact, c.Medium, err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed
	id, err := lockedActiveCopy(ctx, tx, c, nil)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE placement_assurance SET recovery_state = ?
		 WHERE placement_id = ? AND recovery_state IN ('unknown', 'not_configured', 'never_run')`, st, id)
	if err != nil {
		return fmt.Errorf("state: evaluate recovery claim of %s on %s: %w", c.Artifact, c.Medium, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRecoveryHistory
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("state: evaluate recovery claim of %s on %s: %w", c.Artifact, c.Medium, err)
	}
	return nil
}

// RecordRecoveryCleanup settles the cleanup state of the last recovered copy
// without touching the verdict, for a sweep that removes what a test could
// not.
func (j *Journal) RecordRecoveryCleanup(ctx context.Context, c CopyRef, st string) error {
	if st != RecoveryCleanupRemoved && st != RecoveryCleanupFailed {
		return fmt.Errorf("state: %q is not a cleanup state", st)
	}
	if err := c.validate(); err != nil {
		return err
	}
	id, _, err := j.placementRow(ctx, c)
	if err != nil {
		return err
	}
	// The row may not exist yet, and removing what a test staged is true of
	// a copy that has since been retired, so this one writer creates it
	// without the ACTIVE check the verdict writers hold.
	if _, err := j.db.ExecContext(ctx, `INSERT INTO placement_assurance (placement_id) VALUES (?) ON CONFLICT (placement_id) DO NOTHING`, id); err != nil {
		return fmt.Errorf("state: record recovery cleanup of %s on %s: %w", c.Artifact, c.Medium, err)
	}
	if _, err := j.db.ExecContext(ctx, `UPDATE placement_assurance SET recovery_cleanup_state = ? WHERE placement_id = ?`, st, id); err != nil {
		return fmt.Errorf("state: record recovery cleanup of %s on %s: %w", c.Artifact, c.Medium, err)
	}
	return nil
}

// MarkRecoveryOverdue turns every ACTIVE copy whose last recovery test passed
// and whose next test is due by now into RecoveryOverdue, and returns how
// many. It keeps the pass's evidence: overdue is the same claim, older than
// its cadence allows, and its age is the thing it reports.
func (j *Journal) MarkRecoveryOverdue(ctx context.Context, now time.Time) (int, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT pa.placement_id, pa.recovery_next_due_at
		  FROM placement_assurance pa JOIN placements p ON p.id = pa.placement_id
		 WHERE pa.recovery_state = 'passed' AND p.status = 'ACTIVE' AND pa.recovery_next_due_at IS NOT NULL`)
	if err != nil {
		return 0, fmt.Errorf("state: mark overdue recovery tests: %w", err)
	}
	var due []int64
	for rows.Next() {
		var (
			id  int64
			raw sql.NullString
		)
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return 0, fmt.Errorf("state: mark overdue recovery tests: %w", err)
		}
		t, perr := parseTime(raw.String)
		if perr != nil {
			rows.Close()
			return 0, fmt.Errorf("state: stored recovery_next_due_at %q is invalid: %w", raw.String, perr)
		}
		if !t.After(now) {
			due = append(due, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("state: mark overdue recovery tests: %w", err)
	}
	for _, id := range due {
		if _, err := j.db.ExecContext(ctx, `UPDATE placement_assurance SET recovery_state = 'overdue' WHERE placement_id = ? AND recovery_state = 'passed'`, id); err != nil {
			return 0, fmt.Errorf("state: mark overdue recovery tests: %w", err)
		}
	}
	return len(due), nil
}

func (j *Journal) placementRow(ctx context.Context, c CopyRef) (int64, string, error) {
	var (
		id     int64
		status string
	)
	err := j.db.QueryRowContext(ctx, `
		SELECT p.id, p.status FROM placements p JOIN artifacts a ON a.id = p.artifact_id
		 WHERE a.source = ? AND a.backup_set = ? AND a.artifact_name = ? AND p.medium = ?`,
		c.Artifact.Set.Source, c.Artifact.Set.Set, c.Artifact.Name, c.Medium).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", fmt.Errorf("%w: %s has no copy on %q", ErrPlacementNotFound, c.Artifact, c.Medium)
	}
	if err != nil {
		return 0, "", fmt.Errorf("state: find the copy of %s on %s: %w", c.Artifact, c.Medium, err)
	}
	return id, status, nil
}

var _ = strings.TrimSpace
