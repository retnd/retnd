package service

import (
	"context"
	"errors"
	"time"
)

// Recovering several backups to one destination (#1106, #1107).
//
// RecoverMany is the one general call. A whole backup set is the case that
// lists every restore point of the set (Set), and a selection from the
// Backups list is the case that names them (Artifacts). Both go through the
// same plan, the same per-file recovery code as a single recovery
// (planRecovery, stageGrant, the verifying engine) and the same manifest.

// RecoverManyRequest is one operator's ask. Exactly one of Artifacts and Set
// is given.
type RecoverManyRequest struct {
	// Artifacts are backup ids, "source/set/name" each.
	Artifacts []string

	// Set is a "source/set" id: every retained, known-good backup of the
	// set. NewestOnly narrows it to the newest restore point and Since to
	// those updated at or after that moment. Both only apply with Set.
	Set        string
	NewestOnly bool
	Since      time.Time

	// Copy is a medium id, or "" / "best-available", applied to every file.
	Copy string

	// Target is where everything goes: "staging" (a folder name under the
	// recovery staging root) or "storage" (a key prefix on another
	// configured destination). "download" is refused: many files do not fit
	// one browser download, and the refusal says where to send them.
	Target RecoveryTarget

	// AcknowledgeCost is the explicit yes to the cost shown by the plan,
	// once, for the whole request.
	AcknowledgeCost bool
}

// The statuses of one file in a plan or a result.
const (
	RecoverManyReady         = "ready"
	RecoverManyRecovered     = "recovered"
	RecoverManyFailed        = "failed"
	RecoverManySkipped       = "skipped"
	RecoverManyNeedsThaw     = "needs_thaw"
	RecoverManyQuarantined   = "quarantined"
	RecoverManyUnrecoverable = "unrecoverable"
)

// RecoverManyItem is one file of a plan or a result.
type RecoverManyItem struct {
	ArtifactID string
	Name       string
	Status     string
	Copy       string

	// SizeBytes is what the journal recorded, when it recorded one.
	SizeBytes *int64
	// Chargeable is true when reading this file bills the operator.
	Chargeable bool

	// Bytes and SHA256 are what was delivered, for a recovered file.
	Bytes  int64
	SHA256 string
	// Path is the full path under the staging root, Key the object key on a
	// storage destination, Rel the name the manifest lists the file under.
	Path string
	Key  string
	Rel  string

	// Code and Message say why a file was not recovered.
	Code    string
	Message string
	Thaw    *RecoveryThaw
}

// RecoverManyTotals counts a plan or a result.
type RecoverManyTotals struct {
	Count         int
	Ready         int
	Recovered     int
	Failed        int
	Skipped       int
	NeedsThaw     int
	Quarantined   int
	Unrecoverable int
	Bytes         int64
}

// RecoverManyPlan is the pre-flight: what would be recovered, what would not
// and why, and what the destination can hold.
type RecoverManyPlan struct {
	Set    string
	Target RecoveryTarget
	// Folder is where the files go, relative to the destination root.
	Folder string
	Items  []RecoverManyItem
	Totals RecoverManyTotals

	ChargeableCount int
	ChargeableBytes int64
	// UnknownSizes counts ready files the journal recorded no size for.
	UnknownSizes int

	// FreeBytes and ReserveBytes describe the staging disk, for a staging
	// target. FreeBytes is nil where the destination cannot say.
	FreeBytes    *int64
	ReserveBytes int64
}

// RecoverManyResult is what a run reports, and what its manifest says.
type RecoverManyResult struct {
	OperationID string
	Set         string
	Target      RecoveryTarget
	Folder      string
	Items       []RecoverManyItem
	Totals      RecoverManyTotals
	Cancelled   bool

	// ManifestPath (staging) or ManifestKey (storage) is the manifest file.
	ManifestPath   string
	ManifestKey    string
	ManifestSHA256 string
}

// RecoverManyProgress is one event of a run. Kind is "plan" (once, after the
// pre-flight passed and before any byte is read), "start" (a file is about to
// be read) or "item" (a file finished, with its outcome). DoneBytes and
// TotalBytes are the running total.
type RecoverManyProgress struct {
	Kind       string
	Index      int
	Total      int
	Item       RecoverManyItem
	Plan       *RecoverManyPlan
	DoneBytes  int64
	TotalBytes int64
}

// PlanRecoverMany is the pre-flight alone: it reads the journal and the
// destination, starts nothing and writes nothing. It returns the plan even
// when it refuses (the cost was not acknowledged, the files do not fit), so a
// caller can show the numbers the refusal is about.
func (b *BackupService) PlanRecoverMany(ctx context.Context, req RecoverManyRequest) (RecoverManyPlan, error) {
	return RecoverManyPlan{}, errors.New("service: PlanRecoverMany is not implemented")
}

// RecoverMany plans, refuses early when it must, and then recovers every
// ready file to the one destination, calling progress (which may be nil) as
// it goes. One file failing never stops the others; cancelling ctx stops the
// run after discarding the file in flight, and the manifest still says what
// happened.
func (b *BackupService) RecoverMany(ctx context.Context, sess RecoverySession, req RecoverManyRequest, progress func(RecoverManyProgress)) (RecoverManyResult, error) {
	return RecoverManyResult{}, errors.New("service: RecoverMany is not implemented")
}
