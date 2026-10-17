package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/retnd/retnd/core/internal/recoverytest"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// Scrub now, cancel, and the validator output of the newest recovery test
// (EPIC Q, #980, #1071).
//
// # The registry
//
// A scrub or a recovery test an operator starts runs in the background, so
// the only way to stop one is to hold something that can reach it. That is
// assuranceOps: one entry per running operation, keyed by the operation id
// the start returned, holding the cancel function of the context the work
// runs under. Cancelling cancels that context, and the context is what the
// work already honours: the recovery engine's read, the hash of the staged
// copy, and the validator's whole process group (lifecycle.RunRestoreCheck
// kills the group when its context ends, and the stage runner's script runs
// under the same contract).
//
// It is in memory on purpose. An operation is the work of THIS process: when
// the process ends the work ends with it, and an id that outlived it would
// name nothing. A cancel that reaches an engine which is not running the
// operation (it finished, or it is another process's) is a not-found, never a
// success.
//
// # Why cancel is not under /operations
//
// POST /operations is the destructive gate's. A cancel deletes nothing and
// reads less than the operation it stops, so it answers on a deployment whose
// gate is shut. The route is /assurance/operations/{op}/cancel for that
// reason.

// ErrScrubRunning is returned when a scrub of that backup set is already
// running. One at a time per set, so a double click on "Scrub now" cannot
// become two passes over the same copies.
var ErrScrubRunning = errors.New("service: a scrub of this backup set is already running")

const (
	opKindScrub        = "scrub"
	opKindRecoveryTest = "recovery_test"
)

// assuranceOp is one running scrub or recovery test.
type assuranceOp struct {
	id   string
	kind string
	set  string

	// artifact and ack are what a recovery test was asked for. An identical
	// request joins the running test; a different one is refused.
	artifact string
	ack      bool

	// cancel ends the work's context. A cancel request passes
	// app.ErrOperationCancelled as the cause, which is how the work tells a
	// stop on request from the context ending for another reason (#1200).
	cancel context.CancelCauseFunc
	// startedAt is when the operation was registered.
	startedAt time.Time
}

// assuranceOps is the registry of running operations.
type assuranceOps struct {
	mu   sync.Mutex
	byID map[string]*assuranceOp
}

// running returns the operation of that kind running for set, or nil. Callers
// hold mu.
func (r *assuranceOps) running(kind, set string) *assuranceOp {
	for _, op := range r.byID {
		if op.kind == kind && op.set == set {
			return op
		}
	}
	return nil
}

// add registers op. Callers hold mu.
func (r *assuranceOps) add(op *assuranceOp) {
	if r.byID == nil {
		r.byID = map[string]*assuranceOp{}
	}
	if op.startedAt.IsZero() {
		op.startedAt = time.Now().UTC()
	}
	r.byID[op.id] = op
}

// get is the operation running under id, or nil. It is the one way to ask
// the registry, so nothing outside it reaches into mu.
func (r *assuranceOps) get(id string) *assuranceOp {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id]
}

// finish removes the operation and releases its context.
func (r *assuranceOps) finish(id string) {
	r.mu.Lock()
	op := r.byID[id]
	delete(r.byID, id)
	r.mu.Unlock()
	if op != nil {
		op.cancel(nil)
	}
}

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

	// Status is always "cancelling": the stop is asynchronous, and the
	// operation's end line on the activity feed is what says it stopped.
	Status string
}

// RecoveryTestOutputRecord is one stored line of validator output.
type RecoveryTestOutputRecord struct {
	// Kind is "output" or "truncation". It is the only thing a reader may
	// branch on: only the runner writes a truncation record.
	Kind string
	Text string
}

// RecoveryTestOutput is what the newest recovery test of a backup set
// printed. Recorded is false, and everything but BackupSetID empty, when no
// test of the set has ended yet.
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

// assuranceSet resolves and checks the backup set a scrub is asked for.
func (b *BackupService) assuranceSet(id string) (model.BackupSetID, error) {
	set, err := model.ParseBackupSetID(id)
	if err != nil {
		return model.BackupSetID{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	bs, ok := configuredSet(b.state.Load().inner.Config.Sources, set)
	if !ok {
		return model.BackupSetID{}, fmt.Errorf("%w: %s", ErrBackupSetNotFound, id)
	}
	if bs.Engine == model.EngineKopia {
		return model.BackupSetID{}, fmt.Errorf("%w: %s is stored by the incremental engine, whose verification ladder is its own and is read from its snapshot history", ErrInvalidRequest, id)
	}
	if bs.Disabled {
		return model.BackupSetID{}, fmt.Errorf("%w: %s is disabled, and a disabled backup set is left untouched", ErrInvalidRequest, id)
	}
	return set, nil
}

// StartScrub scrubs a backup set's durable copies now, in the background. It
// is what POST /backup-sets/{source}/{set}/scrub calls, and the same code
// `retnd scrub <set>` runs: app.Service.Scrub with a manual trigger, which
// checks every copy of the set whether or not it is due, at the strongest
// class that costs nothing.
//
// The pass runs under the service's own lifetime, not the request's: a
// browser that navigates away must not cancel it, and CancelAssuranceOperation
// is how it is stopped. A set held for editing is skipped, as it is for the
// scheduler.
func (b *BackupService) StartScrub(_ context.Context, id string, _ ScrubOptions) (ScrubStarted, error) {
	set, err := b.assuranceSet(id)
	if err != nil {
		return ScrubStarted{}, err
	}
	opID := uuid.NewString()
	ctx, cancel := context.WithCancelCause(b.ctx)

	b.ops.mu.Lock()
	if b.ops.running(opKindScrub, set.String()) != nil {
		b.ops.mu.Unlock()
		cancel(nil)
		return ScrubStarted{}, ErrScrubRunning
	}
	b.ops.add(&assuranceOp{id: opID, kind: opKindScrub, set: set.String(), cancel: cancel})
	b.ops.mu.Unlock()

	inner := b.state.Load().inner
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer b.ops.finish(opID)
		defer func() {
			if r := recover(); r != nil {
				b.logger.Error(context.Background(), "scrub-now-panic", fmt.Errorf("recovered panic: %v", r))
			}
		}()
		_, err := inner.Scrub(app.WithBackupSetHolds(ctx, b.holds), app.ScrubRequest{Trigger: state.ScrubByManual, Set: &set, OpID: opID})
		if err != nil && ctx.Err() == nil {
			b.logger.Error(context.Background(), "scrub-now", err)
		}
	}()
	return ScrubStarted{BackupSetID: set.String(), OperationID: opID}, nil
}

// CancelAssuranceOperation tells a running scrub or recovery test to stop. It
// answers once the operation has been told, not once it has stopped; the
// operation's own end line says it stopped. An id nothing is running under
// (never started, already finished, or another process's) is
// ErrOperationNotFound.
func (b *BackupService) CancelAssuranceOperation(_ context.Context, id string) (AssuranceOperationCancelled, error) {
	op := b.ops.get(id)
	if op == nil || id == "" {
		return AssuranceOperationCancelled{}, fmt.Errorf("%w: no scrub or recovery test is running under %q", ErrOperationNotFound, id)
	}
	op.cancel(app.ErrOperationCancelled)
	return AssuranceOperationCancelled{OperationID: op.id, Kind: op.kind, BackupSetID: op.set, Status: "cancelling"}, nil
}

// RecoveryTestOutput reads the newest recovery test's output for a backup set.
func (b *BackupService) RecoveryTestOutput(ctx context.Context, id string) (RecoveryTestOutput, error) {
	set, err := model.ParseBackupSetID(id)
	if err != nil {
		return RecoveryTestOutput{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if _, ok := configuredSet(b.state.Load().inner.Config.Sources, set); !ok {
		return RecoveryTestOutput{}, fmt.Errorf("%w: %s", ErrBackupSetNotFound, id)
	}
	out := RecoveryTestOutput{BackupSetID: set.String(), Records: []RecoveryTestOutputRecord{}}
	stored, ok, err := b.journal.RecoveryOutputOf(ctx, set)
	if err != nil {
		return RecoveryTestOutput{}, fmt.Errorf("service: reading the recovery-test output of %s: %w", id, err)
	}
	if !ok {
		return out, nil
	}
	out.Recorded, out.OperationID, out.ArtifactID, out.Medium, out.State = true, stored.OperationID, stored.Artifact, stored.Medium, stored.State
	if recoverytest.RecordsAreBuiltin(stored.Records) {
		out.State = WireRecoveryState(stored.State, recoverytest.BuiltinValidatorID)
	}
	out.RecordedAt, out.Truncated = stored.RecordedAt, stored.Truncated
	for _, r := range stored.Records {
		out.Records = append(out.Records, RecoveryTestOutputRecord{Kind: r.Kind, Text: r.Text})
	}
	return out, nil
}
