package state

import (
	"context"
	"errors"
	"time"

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

var errRecoveryOutputNotBuilt = errors.New("state: recovery-test output is not built yet")

// RecordRecoveryOutput replaces the set's stored output with o's.
func (j *Journal) RecordRecoveryOutput(_ context.Context, _ RecoveryOutput) error {
	return errRecoveryOutputNotBuilt
}

// RecoveryOutputOf returns the set's stored output, and false when no test of
// the set has stored one.
func (j *Journal) RecoveryOutputOf(_ context.Context, _ model.BackupSetID) (RecoveryOutput, bool, error) {
	return RecoveryOutput{}, false, errRecoveryOutputNotBuilt
}

// RecordRecoveryReceipt writes one request's receipt.
func (j *Journal) RecordRecoveryReceipt(_ context.Context, _ RecoveryReceipt) error {
	return errRecoveryOutputNotBuilt
}

// RecoveryReceipts lists the receipts written for one operation id, oldest
// first.
func (j *Journal) RecoveryReceipts(_ context.Context, _ string) ([]RecoveryReceipt, error) {
	return nil, errRecoveryOutputNotBuilt
}
