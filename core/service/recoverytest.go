package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/recovery/export"
	"github.com/retnd/retnd/core/internal/recoverytest"
	"github.com/retnd/retnd/core/internal/state"
)

// Recovery tests as the service offers them (EPIC Q, #980).
//
// internal/app owns the test itself, and the scheduler runs it on its own
// (AssuranceTick). This file is the two things the service layer adds: the
// recoverer that makes a test of a copy on a storage medium possible, by
// going through the very engine an operator's recovery goes through, and the
// operator's way to start one now.

// ErrRecoveryTestRunning is returned when a recovery test of that backup set
// is already under way. One at a time per set: a test reads a whole artifact,
// and two at once would be two reads of a copy that may be billed.
var ErrRecoveryTestRunning = errors.New("service: a recovery test of this backup set is already running")

// RecoveryTestOptions says what to test.
type RecoveryTestOptions struct {
	// ArtifactID (source/set/name) tests this artifact, which has to be in
	// the backup set being tested. Empty tests the newest known good one.
	ArtifactID string

	// AcknowledgeCost is the operator's yes to a read that costs money: the
	// download from a storage medium, or the restore of an archived object.
	// A scheduled test never has it.
	AcknowledgeCost bool

	// OperationID, when set, is the id the test's start, stage and end lines
	// carry on the activity feed, so a caller that wants to follow this one
	// test can choose the id before it starts. Empty means a fresh one.
	OperationID string

	// Actor is who asked, for the receipt. Empty for a caller with no
	// session (the scheduler never goes through StartRecoveryTest).
	Actor string
}

// RecoveryTestOutcome is what a recovery test concluded, reduced to what a
// surface may show: it names no path and carries no validator output.
type RecoveryTestOutcome struct {
	BackupSet string
	Artifact  string
	Medium    string

	// State is one of the state.Recovery* verdicts. For an incremental
	// (engine: kopia) set it is the claim read from the snapshot catalog and
	// ReadOnly is true: nothing was run.
	State    string
	ReadOnly bool

	// DigestMatched is the gate's outcome; nil means the digest was never
	// compared.
	DigestMatched *bool

	ValidatorID  string
	CleanupState string
	Reason       string

	// Check is "builtin" when the product's structure check produced the
	// verdict, empty for the operator's validator. CheckFormat is the one
	// line account of what the built-in read (#1111).
	Check       string
	CheckFormat string
}

// RecoveryTestStarted is what starting a test returns at once. The test runs
// in the background; OperationID is the id its start, stage and end lines
// carry on the activity feed, and the result lands on the copy's recovery
// claim.
type RecoveryTestStarted struct {
	BackupSetID string
	OperationID string

	// Coalesced is true when the request joined a test of the same set that
	// was already running, so OperationID is that test's and nothing new
	// started.
	Coalesced bool
}

// RunRecoveryTest runs one recovery test now and waits for it. It is the same
// code the scheduler runs, with a manual trigger: the set's mode does not
// stop it (asking is the decision), and a read that costs money needs
// AcknowledgeCost.
func (b *BackupService) RunRecoveryTest(ctx context.Context, set model.BackupSetID, opts RecoveryTestOptions) (RecoveryTestOutcome, error) {
	return b.runRecoveryTest(ctx, set, opts, opts.OperationID)
}

func (b *BackupService) runRecoveryTest(ctx context.Context, set model.BackupSetID, opts RecoveryTestOptions, opID string) (RecoveryTestOutcome, error) {
	artifact, err := recoveryTestArtifact(set, opts.ArtifactID)
	if err != nil {
		return RecoveryTestOutcome{}, err
	}
	inner := b.state.Load().inner
	res, err := inner.RecoveryTest(ctx, app.RecoveryTestRequest{
		Set: set, Artifact: artifact, AcknowledgeCost: opts.AcknowledgeCost, OperationID: opID,
	})
	if err != nil {
		if errors.Is(err, state.ErrCopyChanged) {
			return RecoveryTestOutcome{}, fmt.Errorf("service: the copy changed while it was being tested, so no result was recorded; run the test again: %w", err)
		}
		return RecoveryTestOutcome{}, err
	}
	out := RecoveryTestOutcome{BackupSet: set.String()}
	if res.ReadOnly {
		out.State, out.ReadOnly = res.Claim.State, true
		return out, nil
	}
	r := res.Result
	out.Artifact, out.Medium = r.Artifact.String(), r.Medium
	out.State, out.DigestMatched = WireRecoveryState(r.State, r.ValidatorID), r.DigestMatched
	out.ValidatorID, out.CleanupState, out.Reason = r.ValidatorID, r.CleanupState, r.Reason
	out.Check, out.CheckFormat = r.Check, r.CheckFormat
	return out, nil
}

// recoveryTestArtifact parses the optional artifact of a request and refuses
// one that is in another backup set: a test of set A's copy recorded against
// set B's claim would be a statement about nothing.
func recoveryTestArtifact(set model.BackupSetID, id string) (*model.ArtifactID, error) {
	if id == "" {
		return nil, nil
	}
	a, err := app.ParseArtifactID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: %q is not an artifact id (source/set/name)", ErrInvalidRequest, id)
	}
	if a.Set != set {
		return nil, fmt.Errorf("%w: %s is not in backup set %s", ErrInvalidRequest, id, set)
	}
	return &a, nil
}

// StartRecoveryTest begins a recovery test in the background and returns
// before it finishes. It is what POST /backup-sets/{source}/{set}/recovery-test
// calls. A test takes as long as reading the artifact and running the
// operator's validator, which is far longer than a request should be held
// open, and a browser that navigates away must not cancel it, so the test runs
// under the service's own lifetime and is followed on the activity feed by the
// operation id this returns.
func (b *BackupService) StartRecoveryTest(ctx context.Context, id string, opts RecoveryTestOptions) (RecoveryTestStarted, error) {
	set, err := model.ParseBackupSetID(id)
	if err != nil {
		return RecoveryTestStarted{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	cfg := b.state.Load().inner.Config
	bs, ok := configuredSet(cfg.Sources, set)
	if !ok {
		return RecoveryTestStarted{}, fmt.Errorf("%w: %s", ErrBackupSetNotFound, id)
	}
	if bs.Engine == model.EngineKopia {
		return RecoveryTestStarted{}, fmt.Errorf("%w: %s is stored by the incremental engine, whose restore drill is its own and is read from its snapshot history", ErrInvalidRequest, id)
	}
	// Refused now, while the caller is still on the line, rather than in a
	// background job nobody is watching.
	artifact, err := recoveryTestArtifact(set, opts.ArtifactID)
	if err != nil {
		return RecoveryTestStarted{}, err
	}
	if artifact != nil {
		if _, err := b.journal.Get(context.Background(), *artifact); err != nil {
			if errors.Is(err, state.ErrArtifactNotFound) {
				return RecoveryTestStarted{}, fmt.Errorf("%w: %s", ErrArtifactNotFound, opts.ArtifactID)
			}
			return RecoveryTestStarted{}, fmt.Errorf("service: loading artifact %s: %w", opts.ArtifactID, err)
		}
	}

	receipt := state.RecoveryReceipt{
		Set: set, RequestedAt: time.Now().UTC(), RequestedBy: opts.Actor,
		ArtifactID: opts.ArtifactID, AcknowledgedCost: opts.AcknowledgeCost,
	}

	// One execution per set at a time. A request for exactly what is already
	// running joins it: it answers with the running operation's id and gets a
	// receipt of its own. A request for anything else is refused, because it
	// would be a second read of a copy that may be billed.
	b.ops.mu.Lock()
	if cur := b.ops.running(opKindRecoveryTest, set.String()); cur != nil {
		if cur.artifact != opts.ArtifactID || cur.ack != opts.AcknowledgeCost {
			b.ops.mu.Unlock()
			return RecoveryTestStarted{}, ErrRecoveryTestRunning
		}
		opID := cur.id
		b.ops.mu.Unlock()
		receipt.OperationID, receipt.Coalesced = opID, true
		if err := b.journal.RecordRecoveryReceipt(context.WithoutCancel(ctx), receipt); err != nil {
			return RecoveryTestStarted{}, fmt.Errorf("service: recording the receipt for a recovery test of %s: %w", id, err)
		}
		return RecoveryTestStarted{BackupSetID: set.String(), OperationID: opID, Coalesced: true}, nil
	}
	opID := uuid.NewString()
	runCtx, cancel := context.WithCancelCause(b.ctx)
	b.ops.add(&assuranceOp{
		id: opID, kind: opKindRecoveryTest, set: set.String(), artifact: opts.ArtifactID, ack: opts.AcknowledgeCost, cancel: cancel,
	})
	b.ops.mu.Unlock()

	receipt.OperationID = opID
	if err := b.journal.RecordRecoveryReceipt(context.WithoutCancel(ctx), receipt); err != nil {
		b.ops.finish(opID)
		return RecoveryTestStarted{}, fmt.Errorf("service: recording the receipt for a recovery test of %s: %w", id, err)
	}

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer b.ops.finish(opID)
		defer func() {
			if r := recover(); r != nil {
				b.logger.Error(context.Background(), "recovery-test-panic", fmt.Errorf("recovered panic: %v", r))
			}
		}()
		if _, err := b.runRecoveryTest(runCtx, set, opts, opID); err != nil && runCtx.Err() == nil {
			b.logger.Error(context.Background(), "recovery-test", err)
		}
	}()
	return RecoveryTestStarted{BackupSetID: set.String(), OperationID: opID}, nil
}

// engineRecoverer is recoverytest.Recoverer over the recovery engine
// (core/internal/recovery/export): a test of a copy, local or on a storage
// medium, is staged by the same code, through the same plan, budgets,
// provider-checksum probe and digest check an operator's recovery uses, so
// "the recovery test passed" is a statement about the path a real recovery
// takes and not about a lookalike.
//
// What it will not do is start a provider restore. A test of an archived copy
// that needs a thaw is refused, and the test records it could not complete;
// it never reads a stand-in and never passes.
type engineRecoverer struct {
	b *BackupService

	mu     sync.Mutex
	staged map[string]*export.Staged
}

var _ recoverytest.Recoverer = (*engineRecoverer)(nil)

func newEngineRecoverer(b *BackupService) *engineRecoverer {
	return &engineRecoverer{b: b, staged: map[string]*export.Staged{}}
}

func (e *engineRecoverer) Recover(ctx context.Context, req recoverytest.RecoverRequest) (recoverytest.Staged, error) {
	b := e.b
	h, err := b.recoveryReady()
	if err != nil {
		return recoverytest.Staged{}, err
	}
	plan, err := b.planRecovery(ctx, req.Artifact.String(), req.Copy.Medium, req.AcknowledgeCost)
	if err != nil {
		return recoverytest.Staged{}, testRecoveryError(err)
	}
	if plan.resolved.NeedsThaw {
		return recoverytest.Staged{}, fmt.Errorf("the copy on %s is in an archive tier and needs a provider restore first, which a recovery test never starts", req.Copy.Medium)
	}
	if err := checkPlanDigest(plan); err != nil {
		return recoverytest.Staged{}, testRecoveryError(err)
	}
	st := b.state.Load()
	if err := b.checkProviderChecksum(ctx, st, plan); err != nil {
		return recoverytest.Staged{}, testRecoveryError(err)
	}
	src, err := b.sourceFor(st, plan)
	if err != nil {
		return recoverytest.Staged{}, testRecoveryError(err)
	}
	// The scheduler's work per pass is already bounded, so it queues for a
	// read slot; an operator who asked is told the host is busy.
	eng := *h.engine
	eng.WaitForRead = req.Scheduled
	staged, err := eng.Stage(ctx, src, plan.expect)
	if err != nil {
		return recoverytest.Staged{}, testRecoveryError(err)
	}
	e.mu.Lock()
	e.staged[staged.Path] = staged
	e.mu.Unlock()
	return recoverytest.Staged{Path: staged.Path}, nil
}

func (e *engineRecoverer) Remove(_ context.Context, s recoverytest.Staged) error {
	e.mu.Lock()
	staged, ok := e.staged[s.Path]
	delete(e.staged, s.Path)
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("refusing to remove a staged copy this recoverer did not stage")
	}
	return staged.Discard()
}

// testRecoveryError turns the recovery domain's coded refusals into what the
// recovery-test runner understands: bytes that did not verify are the digest
// gate, a refused cost is a refused cost, and anything else is an attempt that
// could not complete, described by the domain's own sentence (never a
// provider's body or a path).
func testRecoveryError(err error) error {
	re, ok := AsRecoveryError(mapRecoveryError(err))
	if !ok {
		return err
	}
	switch re.Code {
	case RecoveryLocalCopyCorrupt, RecoveryRemoteCopyCorrupt:
		return fmt.Errorf("%w: %s", recoverytest.ErrCorruptCopy, re.Message)
	case RecoveryCostNotAcknowledged:
		return fmt.Errorf("%w: %s", recoverytest.ErrCostRefused, re.Message)
	}
	return errors.New(re.Message)
}
