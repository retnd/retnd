package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/retnd/retnd/core/internal/model"
)

// Recovery-test output and request receipts (migration 0016, EPIC Q, #980).
//
// Two small tables sit beside the recovery claim. Neither one is a verdict:
// the claim on the copy (placement_assurance) stays the only place a pass or
// a failure is recorded.
//
//   - recovery_test_output keeps what the NEWEST recovery test of a backup
//     set printed, one row per set. A newer test replaces it, including a
//     newer test that printed nothing, so a reader is never shown an older
//     run's output as if it belonged to the newest result.
//   - recovery_test_receipts keeps one row per REQUEST for a recovery test,
//     because two operators asking for the same test while it runs share one
//     execution and each is still owed a receipt of their own.
//
// # What the output table will and will not hold
//
// A validator's output is untrusted terminal input. internal/recoverytest
// sanitises and bounds it before it gets here; this file refuses what
// should never have arrived, so a caller that skipped that step stores an
// error instead of a raw control byte. The truncation marker is a record of
// its own kind with one fixed text, and nothing but the runner writes that
// kind, so a validator that prints the marker's words is stored as the
// ordinary output it is.

const (
	// RecoveryOutputKindText is a line of the validator's own output.
	RecoveryOutputKindText = "output"
	// RecoveryOutputKindTruncation is the runner's statement that the
	// validator wrote more than the capture bound.
	RecoveryOutputKindTruncation = "truncation"

	// MaxRecoveryOutputRecords bounds the records kept for one test,
	// the truncation record included.
	MaxRecoveryOutputRecords = 200
	// MaxRecoveryOutputLineBytes bounds one record's text.
	MaxRecoveryOutputLineBytes = 1024
	// MaxRecoveryOutputBytes bounds the text of all records of one test.
	MaxRecoveryOutputBytes = 32 << 10

	// RecoveryOutputTruncationText is the only text a truncation record may
	// carry.
	RecoveryOutputTruncationText = "[retnd] output truncated: the validator wrote more than the capture bound, and the rest was discarded"

	maxReceiptFieldBytes = 400
)

// RecoveryOutputRecord is one stored line.
type RecoveryOutputRecord struct {
	Kind string
	Text string
}

// RecoveryOutput is the newest recovery test's output for one backup set.
type RecoveryOutput struct {
	Set         model.BackupSetID
	OperationID string
	Artifact    string
	Medium      string
	State       string
	RecordedAt  time.Time
	Truncated   bool
	Records     []RecoveryOutputRecord
}

// RecoveryReceipt is one request for a recovery test.
type RecoveryReceipt struct {
	OperationID string
	Set         model.BackupSetID
	RequestedAt time.Time
	RequestedBy string

	// ArtifactID is the artifact the request named, empty for the newest
	// known good copy. AcknowledgedCost is the request's yes to a billed read.
	ArtifactID       string
	AcknowledgedCost bool

	// Coalesced is true when the request joined a test that was already
	// running and so started no execution of its own.
	Coalesced bool
}

// storedRecord is the JSON shape of one record in recovery_test_output.records.
type storedRecord struct {
	K string `json:"k"`
	T string `json:"t"`
}

// inertText reports whether s is valid UTF-8 with no control character in it:
// no C0 (newline and tab included, a record is one line), no DEL, no C1.
func inertText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
			return false
		}
	}
	return true
}

func (o RecoveryOutput) validate() error {
	if o.Set.IsZero() {
		return errors.New("state: recovery output needs a backup set")
	}
	if o.OperationID == "" || len(o.OperationID) > maxReceiptFieldBytes || !inertText(o.OperationID) {
		return errors.New("state: recovery output needs the operation id of the test that printed it")
	}
	if len(o.Artifact) > maxReceiptFieldBytes || !inertText(o.Artifact) || len(o.Medium) > maxReceiptFieldBytes || !inertText(o.Medium) {
		return errors.New("state: the artifact and medium of recovery output are short printable names")
	}
	if o.RecordedAt.IsZero() {
		return errors.New("state: recovery output needs the time the test ended")
	}
	switch o.State {
	case RecoveryPassed, RecoveryFailed, RecoveryRefusedForCost, RecoveryCouldNotComplete:
	default:
		return fmt.Errorf("state: %q is not the result of a recovery test", o.State)
	}
	if len(o.Records) > MaxRecoveryOutputRecords {
		return fmt.Errorf("state: %d output records is over the bound of %d", len(o.Records), MaxRecoveryOutputRecords)
	}
	total, truncations := 0, 0
	for i, r := range o.Records {
		switch r.Kind {
		case RecoveryOutputKindText:
		case RecoveryOutputKindTruncation:
			truncations++
			if i != len(o.Records)-1 {
				return errors.New("state: a truncation record is the last record or it is nothing")
			}
			if r.Text != RecoveryOutputTruncationText {
				return errors.New("state: a truncation record says what the runner says and nothing else")
			}
		default:
			return fmt.Errorf("state: %q is not an output record kind", r.Kind)
		}
		if len(r.Text) > MaxRecoveryOutputLineBytes {
			return fmt.Errorf("state: an output record of %d bytes is over the bound of %d", len(r.Text), MaxRecoveryOutputLineBytes)
		}
		if !inertText(r.Text) {
			return errors.New("state: an output record holds a control character or invalid UTF-8; it has to be sanitised before it is stored")
		}
		total += len(r.Text)
	}
	if total > MaxRecoveryOutputBytes {
		return fmt.Errorf("state: %d bytes of output is over the bound of %d", total, MaxRecoveryOutputBytes)
	}
	if o.Truncated != (truncations == 1) {
		return errors.New("state: output is marked truncated exactly when it ends with a truncation record")
	}
	return nil
}

// RecordRecoveryOutput replaces the set's stored output with o's.
func (j *Journal) RecordRecoveryOutput(ctx context.Context, o RecoveryOutput) error {
	if err := o.validate(); err != nil {
		return err
	}
	stored := make([]storedRecord, 0, len(o.Records))
	for _, r := range o.Records {
		stored = append(stored, storedRecord{K: r.Kind, T: r.Text})
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("state: encoding recovery output of %s: %w", o.Set, err)
	}
	_, err = j.db.ExecContext(ctx, `
		INSERT INTO recovery_test_output
			(source, backup_set, operation_id, artifact, medium, state, recorded_at, truncated, records)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, backup_set) DO UPDATE SET
			operation_id = excluded.operation_id, artifact = excluded.artifact, medium = excluded.medium,
			state = excluded.state, recorded_at = excluded.recorded_at, truncated = excluded.truncated,
			records = excluded.records`,
		o.Set.Source, o.Set.Set, o.OperationID, o.Artifact, o.Medium, o.State, formatTime(o.RecordedAt),
		boolToInt(o.Truncated), string(raw))
	if err != nil {
		return fmt.Errorf("state: record recovery output of %s: %w", o.Set, err)
	}
	return nil
}

// RecoveryOutputOf returns the set's stored output, and false when no test of
// the set has stored one.
func (j *Journal) RecoveryOutputOf(ctx context.Context, set model.BackupSetID) (RecoveryOutput, bool, error) {
	var (
		o         = RecoveryOutput{Set: set}
		recorded  string
		truncated int64
		raw       string
	)
	err := j.db.QueryRowContext(ctx, `
		SELECT operation_id, artifact, medium, state, recorded_at, truncated, records
		  FROM recovery_test_output WHERE source = ? AND backup_set = ?`,
		set.Source, set.Set).Scan(&o.OperationID, &o.Artifact, &o.Medium, &o.State, &recorded, &truncated, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return RecoveryOutput{}, false, nil
	}
	if err != nil {
		return RecoveryOutput{}, false, fmt.Errorf("state: read recovery output of %s: %w", set, err)
	}
	if o.RecordedAt, err = parseTime(recorded); err != nil {
		return RecoveryOutput{}, false, fmt.Errorf("state: recovery output of %s has an unreadable time: %w", set, err)
	}
	o.Truncated = truncated != 0
	var stored []storedRecord
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return RecoveryOutput{}, false, fmt.Errorf("state: recovery output of %s is unreadable: %w", set, err)
	}
	o.Records = make([]RecoveryOutputRecord, 0, len(stored))
	for _, r := range stored {
		o.Records = append(o.Records, RecoveryOutputRecord{Kind: r.K, Text: r.T})
	}
	return o, true, nil
}

func (r RecoveryReceipt) validate() error {
	if r.OperationID == "" || len(r.OperationID) > maxReceiptFieldBytes || !inertText(r.OperationID) {
		return errors.New("state: a recovery-test receipt names the operation")
	}
	if r.Set.IsZero() {
		return errors.New("state: a recovery-test receipt names the backup set")
	}
	if r.RequestedAt.IsZero() {
		return errors.New("state: a recovery-test receipt records when it was asked for")
	}
	if len(r.RequestedBy) > maxReceiptFieldBytes || !inertText(r.RequestedBy) || len(r.ArtifactID) > maxReceiptFieldBytes || !inertText(r.ArtifactID) {
		return errors.New("state: who asked and what they named are short printable strings")
	}
	return nil
}

// RecordRecoveryReceipt writes one request's receipt.
func (j *Journal) RecordRecoveryReceipt(ctx context.Context, r RecoveryReceipt) error {
	if err := r.validate(); err != nil {
		return err
	}
	_, err := j.db.ExecContext(ctx, `
		INSERT INTO recovery_test_receipts
			(operation_id, source, backup_set, requested_at, requested_by, artifact_id, acknowledged_cost, coalesced)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.OperationID, r.Set.Source, r.Set.Set, formatTime(r.RequestedAt), r.RequestedBy, r.ArtifactID,
		boolToInt(r.AcknowledgedCost), boolToInt(r.Coalesced))
	if err != nil {
		return fmt.Errorf("state: record recovery-test receipt for %s: %w", r.OperationID, err)
	}
	return nil
}

// RecoveryReceipts lists the receipts written for one operation id, oldest
// first.
func (j *Journal) RecoveryReceipts(ctx context.Context, operationID string) ([]RecoveryReceipt, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT operation_id, source, backup_set, requested_at, requested_by, artifact_id, acknowledged_cost, coalesced
		  FROM recovery_test_receipts WHERE operation_id = ? ORDER BY id`, operationID)
	if err != nil {
		return nil, fmt.Errorf("state: list recovery-test receipts of %s: %w", operationID, err)
	}
	defer rows.Close()
	var out []RecoveryReceipt
	for rows.Next() {
		var (
			r         RecoveryReceipt
			at        string
			ack, coal int64
		)
		if err := rows.Scan(&r.OperationID, &r.Set.Source, &r.Set.Set, &at, &r.RequestedBy, &r.ArtifactID, &ack, &coal); err != nil {
			return nil, fmt.Errorf("state: read a recovery-test receipt of %s: %w", operationID, err)
		}
		if r.RequestedAt, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("state: a recovery-test receipt of %s has an unreadable time: %w", operationID, err)
		}
		r.AcknowledgedCost, r.Coalesced = ack != 0, coal != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: list recovery-test receipts of %s: %w", operationID, err)
	}
	return out, nil
}
