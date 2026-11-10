package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
)

// Encrypting retained copies that already exist (#1291, a follow-up to EPIC N,
// #967), the operator surface. `retnd encryption encrypt-existing` and
// POST/GET /api/v1/encryption/encrypt-existing both land on the methods here,
// so a refusal has one author and the terminal and the browser read the same
// sentence.
//
// The engine does the work (placement.Engine.EncryptExisting, reached through
// app.Service.EncryptExisting): this file validates the request before
// anything is planned, enumerates the set's copies, and runs the operation
// under the same registry a scrub and a decrypt-verify run under. That is
// what makes a cancel one call from a terminal and from a browser: both are
// CancelAssuranceOperation, which ends the operation's context, and the
// engine brings a move whose context ended to one of the two end states
// documented on placement.Engine.EncryptExisting. A terminal that runs the
// verb in its own process (RunEncryptExisting) is stopped by ending the
// context it passes, which reaches the same code.
//
// Nothing here starts on its own. There is no scheduler for it, a policy
// change does not call it, and the request has no switch to skip a
// verification.

// ErrEncryptExistingRunning is returned when a run for that backup set is
// already going. One at a time per set, so a double click cannot become two
// passes over the same copies.
var ErrEncryptExistingRunning = errors.New("service: an encrypt-existing run for this backup set is already running")

const opKindEncryptExisting = "encrypt_existing"

// EncryptExistingOutcome is what happened to one backup in a run: the phase
// its move was left in, and the reason when it was refused or stopped short.
type EncryptExistingOutcome struct {
	ArtifactID string `json:"artifact_id"`
	Phase      string `json:"phase,omitempty"`
	Refusal    string `json:"refusal,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// EncryptExistingResult is what a run in the terminal's own process did. It
// is the body `retnd encryption encrypt-existing --json` prints when nothing
// serves the deployment; beside an engine the verb starts the engine's
// operation and prints its id instead.
type EncryptExistingResult struct {
	Planned   int                      `json:"planned"`
	Resumed   int                      `json:"resumed"`
	Completed int                      `json:"completed"`
	Abandoned int                      `json:"abandoned"`
	Refused   int                      `json:"refused"`
	Outcomes  []EncryptExistingOutcome `json:"outcomes"`
}

// encryptExistingRefusal is the stable code of an engine refusal. ok is false
// for an error that is not one of them (a journal that would not answer),
// which the caller reports as a failure of the request, not as a verdict.
func encryptExistingRefusal(err error) (code string, ok bool) {
	for _, r := range []struct {
		err  error
		code string
	}{
		// #1317's refusals come first: the source-key one also wraps
		// ErrClassUnavailable, and the more specific code is the one to say.
		{placement.ErrReencryptAlreadyCurrent, "already_current"},
		{placement.ErrReencryptSourceKeyUnavailable, "source_key_unavailable"},
		{placement.ErrEncryptExistingSameMedium, "same_medium"},
		{placement.ErrEncryptExistingNotRequired, "not_required"},
		{placement.ErrEncryptExistingAlreadyEncrypted, "already_encrypted"},
		{placement.ErrEncryptExistingNoPlaintextCopy, "no_plaintext_copy"},
		{placement.ErrEncryptExistingNoKey, "key_unavailable"},
		{placement.ErrEncryptExistingDestinationClass, "destination_unverifiable"},
		{placement.ErrEncryptExistingNoRoom, "no_room"},
		{placement.ErrEncryptExistingSourceUnreadable, "source_unreadable"},
		{placement.ErrNotEligible, "not_eligible"},
	} {
		if errors.Is(err, r.err) {
			return r.code, true
		}
	}
	return "", false
}

// prepareEncryptExisting is every refusal a request has before anything is
// planned, in the order it has them: a bad set id, a set that cannot be
// operated on, a set that does not require encryption, a destination that is
// not a configured medium, an artifact that is not in the set. It returns the
// plans a run would take, one per backup that has an ACTIVE copy on a medium
// (the engine says per copy which of them it would refuse, and why).
func (b *BackupService) prepareEncryptExisting(ctx context.Context, setID, artifactID, destination string) (model.BackupSetID, []placement.Plan, error) {
	set, err := b.assuranceSet(setID)
	if err != nil {
		return model.BackupSetID{}, nil, err
	}
	inner := b.state.Load().inner
	bs, _ := configuredSet(inner.Config.Sources, set)
	if !bs.RetainedCopyEncryption.Enabled() {
		return model.BackupSetID{}, nil, fmt.Errorf("%w: %s does not require retained-copy encryption, so there is nothing to bring its copies in line with; turn the policy on first", ErrInvalidRequest, set)
	}
	if destination == "" {
		return model.BackupSetID{}, nil, fmt.Errorf("%w: a destination medium is required, because an encrypted copy cannot sit beside its plaintext source on the same medium", ErrInvalidRequest)
	}
	var ids []string
	var found bool
	for _, m := range inner.Config.StorageMediums {
		ids = append(ids, m.ID)
		if m.ID == destination {
			found = true
		}
	}
	if !found {
		return model.BackupSetID{}, nil, fmt.Errorf("%w: %q is not a configured storage medium (configured: %s)", ErrInvalidRequest, destination, strings.Join(ids, ", "))
	}

	if artifactID != "" {
		id, err := app.ParseArtifactID(artifactID)
		if err != nil {
			return model.BackupSetID{}, nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
		if id.Set != set {
			return model.BackupSetID{}, nil, fmt.Errorf("%w: %s is not a backup of %s", ErrInvalidRequest, artifactID, set)
		}
		if _, err := b.journal.Get(ctx, id); err != nil {
			if errors.Is(err, state.ErrArtifactNotFound) {
				return model.BackupSetID{}, nil, fmt.Errorf("%w: %s", ErrArtifactNotFound, artifactID)
			}
			return model.BackupSetID{}, nil, fmt.Errorf("service: loading artifact %s: %w", artifactID, err)
		}
		return set, []placement.Plan{{Artifact: id, DestinationMedium: destination}}, nil
	}

	recs, err := b.journal.ListByBackupSet(ctx, set)
	if err != nil {
		return model.BackupSetID{}, nil, fmt.Errorf("service: listing the backups of %s: %w", set, err)
	}
	var plans []placement.Plan
	for _, rec := range recs {
		if rec.State == string(lifecycle.Complete) && encryptExistingSource(rec) != "" {
			plans = append(plans, placement.Plan{Artifact: rec.Artifact, DestinationMedium: destination})
		}
	}
	return set, plans, nil
}

// encryptExistingSource is the medium a backup's ACTIVE copy on a medium is
// on, or "" when it has none.
func encryptExistingSource(rec state.Record) string {
	for _, p := range rec.Placements {
		if p.Status == state.PlacementActive && p.Medium != state.MediumLocal {
			return p.Medium
		}
	}
	return ""
}

// PlanEncryptExisting is GET /encryption/encrypt-existing, the dry run: for
// each backup of the set with a copy on a medium, whether a run would encrypt
// it onto destination and, when not, which refusal applies. It asks the engine
// the question a run asks, so the table and the run cannot disagree, and it
// plans nothing, reads no object and writes nothing.
func (b *BackupService) PlanEncryptExisting(ctx context.Context, setID, artifactID, destination string) (apicontract.EncryptExistingPlan, error) {
	_, plans, err := b.prepareEncryptExisting(ctx, setID, artifactID, destination)
	if err != nil {
		return apicontract.EncryptExistingPlan{}, err
	}
	checks, err := b.state.Load().inner.CheckEncryptExisting(ctx, plans)
	if err != nil {
		return apicontract.EncryptExistingPlan{}, err
	}
	out := apicontract.EncryptExistingPlan{Candidates: []apicontract.EncryptExistingCandidate{}}
	for i, p := range plans {
		rec, err := b.journal.Get(ctx, p.Artifact)
		if err != nil {
			return apicontract.EncryptExistingPlan{}, fmt.Errorf("service: loading artifact %s: %w", p.Artifact, err)
		}
		c := apicontract.EncryptExistingCandidate{ArtifactID: p.Artifact.String(), SourceMedium: encryptExistingSource(rec), Verdict: "would_encrypt"}
		if checks[i] != nil {
			code, ok := encryptExistingRefusal(checks[i])
			if !ok {
				return apicontract.EncryptExistingPlan{}, fmt.Errorf("service: checking %s: %w", p.Artifact, checks[i])
			}
			c.Verdict, c.Refusal, c.Reason = "refused", code, checks[i].Error()
			out.Refused++
		} else {
			out.WouldEncrypt++
		}
		out.Candidates = append(out.Candidates, c)
	}
	return out, nil
}

// requireEncryptExistingKey refuses a request, before anything is planned,
// when the key the copies would be encrypted under cannot be used: only a
// managed key can write copies today, and it has to resolve.
func (b *BackupService) requireEncryptExistingKey(ctx context.Context, set model.BackupSetID) error {
	bs, _ := configuredSet(b.state.Load().inner.Config.Sources, set)
	if !bs.RetainedCopyEncryption.KeyProvider.Managed {
		return fmt.Errorf("%w: %s requires encryption under an external key provider, which cannot write copies yet; only the managed key can", ErrInvalidRequest, set)
	}
	rec, ok, err := b.keys.Active(ctx)
	if err != nil {
		return fmt.Errorf("%w: reading the active key: %v", ErrEncryptionKeyUnresolvable, err)
	}
	if !ok {
		return fmt.Errorf("%w: no managed key is active, so there is nothing to encrypt the copies under", ErrEncryptionKeyUnresolvable)
	}
	if !b.keyResolves(ctx, rec.ID) {
		return fmt.Errorf("%w: the active key %s does not resolve", ErrEncryptionKeyUnresolvable, rec.ID)
	}
	return nil
}

// RunEncryptExisting is the terminal's run when no engine is serving: the same
// refusals, then the same engine run, to the end in this process, with ctx as
// the only way to stop it.
func (b *BackupService) RunEncryptExisting(ctx context.Context, req apicontract.StartEncryptExistingRequest) (EncryptExistingResult, error) {
	set, plans, err := b.prepareEncryptExisting(ctx, req.BackupSetID, req.ArtifactID, req.DestinationMedium)
	if err != nil {
		return EncryptExistingResult{}, err
	}
	if err := b.requireEncryptExistingKey(ctx, set); err != nil {
		return EncryptExistingResult{}, err
	}
	// The cycle's single-flight guard: a normal move cycle must not drive the
	// same moves while this runs.
	if !b.runOnce.TryLock() {
		return EncryptExistingResult{}, fmt.Errorf("%w: a backup cycle is in progress", ErrOperationAlreadyRunning)
	}
	defer b.runOnce.Unlock()
	report, err := b.state.Load().inner.EncryptExisting(ctx, plans)
	if err != nil {
		return EncryptExistingResult{}, err
	}
	out := EncryptExistingResult{
		Planned: report.Planned, Resumed: report.Resumed, Completed: report.Completed,
		Abandoned: report.Abandoned, Refused: report.Refused, Outcomes: []EncryptExistingOutcome{},
	}
	for _, o := range report.Outcomes {
		line := EncryptExistingOutcome{ArtifactID: o.Artifact.String(), Phase: string(o.Phase), Reason: o.Refused}
		if code, ok := encryptExistingRefusal(o.Err); ok && o.Err != nil {
			line.Refusal = code
		}
		out.Outcomes = append(out.Outcomes, line)
	}
	return out, nil
}

// StartEncryptExisting is POST /encryption/encrypt-existing: the same run,
// started in the background under the operation registry a scrub uses, so the
// cancel route is the same route from a terminal and from a browser. The
// request is refused before any read when the set does not require encryption,
// the destination is not a configured medium, or the key does not resolve.
func (b *BackupService) StartEncryptExisting(ctx context.Context, req apicontract.StartEncryptExistingRequest) (ScrubStarted, error) {
	set, plans, err := b.prepareEncryptExisting(ctx, req.BackupSetID, req.ArtifactID, req.DestinationMedium)
	if err != nil {
		return ScrubStarted{}, err
	}
	if err := b.requireEncryptExistingKey(ctx, set); err != nil {
		return ScrubStarted{}, err
	}

	opID := uuid.NewString()
	opCtx, cancel := context.WithCancelCause(b.ctx)
	b.ops.mu.Lock()
	if b.ops.running(opKindEncryptExisting, set.String()) != nil {
		b.ops.mu.Unlock()
		cancel(nil)
		return ScrubStarted{}, ErrEncryptExistingRunning
	}
	// The cycle's single-flight guard, held until the goroutine ends.
	if !b.runOnce.TryLock() {
		b.ops.mu.Unlock()
		cancel(nil)
		return ScrubStarted{}, fmt.Errorf("%w: a backup cycle is in progress", ErrOperationAlreadyRunning)
	}
	b.ops.add(&assuranceOp{id: opID, kind: opKindEncryptExisting, set: set.String(), cancel: cancel})
	b.ops.mu.Unlock()

	inner := b.state.Load().inner
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer b.runOnce.Unlock()
		defer b.ops.finish(opID)
		defer func() {
			if r := recover(); r != nil {
				b.logger.Error(context.Background(), "encrypt-existing-panic", fmt.Errorf("recovered panic: %v", r))
			}
		}()
		if _, err := inner.EncryptExisting(opCtx, plans); err != nil && opCtx.Err() == nil {
			b.logger.Error(context.Background(), "encrypt-existing", err)
		}
	}()
	return ScrubStarted{BackupSetID: set.String(), OperationID: opID}, nil
}
