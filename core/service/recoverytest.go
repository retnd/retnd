package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

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
	// Artifact tests this artifact. Nil tests the newest known good one.
	Artifact *model.ArtifactID

	// AcknowledgeCost is the operator's yes to a read that costs money: the
	// download from a storage medium, or the restore of an archived object.
	// A scheduled test never has it.
	AcknowledgeCost bool
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
}

// RecoveryTestStarted is what starting a test returns at once. The test runs
// in the background; OperationID is the id its start, stage and end lines
// carry on the activity feed, and the result lands on the copy's recovery
// claim.
type RecoveryTestStarted struct {
	BackupSetID string
	OperationID string
}

// RunRecoveryTest runs one recovery test now and waits for it. It is the same
// code the scheduler runs, with a manual trigger: the set's mode does not
// stop it (asking is the decision), and a read that costs money needs
// AcknowledgeCost.
func (b *BackupService) RunRecoveryTest(ctx context.Context, set model.BackupSetID, opts RecoveryTestOptions) (RecoveryTestOutcome, error) {
	return b.runRecoveryTest(ctx, set, opts, "")
}

func (b *BackupService) runRecoveryTest(ctx context.Context, set model.BackupSetID, opts RecoveryTestOptions, opID string) (RecoveryTestOutcome, error) {
	inner := b.state.Load().inner
	res, err := inner.RecoveryTest(ctx, app.RecoveryTestRequest{
		Set: set, Artifact: opts.Artifact, AcknowledgeCost: opts.AcknowledgeCost, OperationID: opID,
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
	out.State, out.DigestMatched = r.State, r.DigestMatched
	out.ValidatorID, out.CleanupState, out.Reason = r.ValidatorID, r.CleanupState, r.Reason
	return out, nil
}

// StartRecoveryTest begins a recovery test in the background and returns
// before it finishes. It is what POST /backup-sets/{source}/{set}/recovery-test
// calls. A test takes as long as reading the artifact and running the
// operator's validator, which is far longer than a request should be held
// open, and a browser that navigates away must not cancel it, so the test runs
// under the service's own lifetime and is followed on the activity feed by the
// operation id this returns.
func (b *BackupService) StartRecoveryTest(_ context.Context, id string, opts RecoveryTestOptions) (RecoveryTestStarted, error) {
	set, err := model.ParseBackupSetID(id)
	if err != nil {
		return RecoveryTestStarted{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	cfg := b.state.Load().inner.Config
	if _, ok := configuredSet(cfg.Sources, set); !ok {
		return RecoveryTestStarted{}, fmt.Errorf("%w: %s", ErrBackupSetNotFound, id)
	}

	b.recoveryTests.mu.Lock()
	if b.recoveryTests.running == nil {
		b.recoveryTests.running = map[string]bool{}
	}
	if b.recoveryTests.running[set.String()] {
		b.recoveryTests.mu.Unlock()
		return RecoveryTestStarted{}, ErrRecoveryTestRunning
	}
	b.recoveryTests.running[set.String()] = true
	b.recoveryTests.mu.Unlock()

	opID := uuid.NewString()
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer func() {
			b.recoveryTests.mu.Lock()
			delete(b.recoveryTests.running, set.String())
			b.recoveryTests.mu.Unlock()
		}()
		defer func() {
			if r := recover(); r != nil {
				b.logger.Error(context.Background(), "recovery-test-panic", fmt.Errorf("recovered panic: %v", r))
			}
		}()
		if _, err := b.runRecoveryTest(b.ctx, set, opts, opID); err != nil && b.ctx.Err() == nil {
			b.logger.Error(context.Background(), "recovery-test", err)
		}
	}()
	return RecoveryTestStarted{BackupSetID: set.String(), OperationID: opID}, nil
}

// recoveryTestRuns is the per-set single-flight table for StartRecoveryTest.
type recoveryTestRuns struct {
	mu      sync.Mutex
	running map[string]bool
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
