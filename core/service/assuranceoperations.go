package service

import (
	"context"
	"errors"
	"time"
)

// Scrub now, cancel, and the validator output of the newest recovery test
// (EPIC Q, #980, #1071).

// ErrScrubRunning is returned when a scrub of that backup set is already
// running.
var ErrScrubRunning = errors.New("service: a scrub of this backup set is already running")

var errAssuranceOperationsNotBuilt = errors.New("service: not built yet")

// ScrubOptions says who asked.
type ScrubOptions struct {
	Actor string
}

// ScrubStarted is what starting a scrub returns at once.
type ScrubStarted struct {
	BackupSetID string
	OperationID string
}

// AssuranceOperationCancelled is the answer to a cancel: the running
// operation was told to stop.
type AssuranceOperationCancelled struct {
	OperationID string
	Kind        string
	BackupSetID string
	Status      string
}

// RecoveryTestOutputRecord is one stored line of validator output.
type RecoveryTestOutputRecord struct {
	Kind string
	Text string
}

// RecoveryTestOutput is what the newest recovery test of a backup set
// printed.
type RecoveryTestOutput struct {
	BackupSetID string
	Recorded    bool
	OperationID string
	ArtifactID  string
	Medium      string
	State       string
	RecordedAt  time.Time
	Truncated   bool
	Records     []RecoveryTestOutputRecord
}

// StartScrub scrubs a backup set's durable copies now, in the background.
func (b *BackupService) StartScrub(_ context.Context, _ string, _ ScrubOptions) (ScrubStarted, error) {
	return ScrubStarted{}, errAssuranceOperationsNotBuilt
}

// CancelAssuranceOperation tells a running scrub or recovery test to stop.
func (b *BackupService) CancelAssuranceOperation(_ context.Context, _ string) (AssuranceOperationCancelled, error) {
	return AssuranceOperationCancelled{}, errAssuranceOperationsNotBuilt
}

// RecoveryTestOutput reads the newest recovery test's output for a set.
func (b *BackupService) RecoveryTestOutput(_ context.Context, _ string) (RecoveryTestOutput, error) {
	return RecoveryTestOutput{}, errAssuranceOperationsNotBuilt
}
