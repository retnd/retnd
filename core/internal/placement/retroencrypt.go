package placement

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/retnd/retnd/core/internal/state"
)

// This file is the operator's way out of a refusal EPIC N left standing
// (#1291, follow-up to #967): a plaintext copy that already sits on a medium,
// under a backup set whose retained-copy encryption is now "required", may not
// hop medium to medium (the hop would stage and upload it in the clear), and
// nothing encrypted existing copies. EncryptExisting does, when an operator
// asks it to and not otherwise.
//
// # It is an ordinary move with one extra fact
//
// There is no second state machine here. The encrypted copy is made by the
// same journaled move every other copy is made by, from the medium the
// plaintext copy is on to a destination medium the operator names, and the
// whole safety argument is the one phases.go already makes: the source is
// retired only after the destination has been decrypt-verified against the
// artifact's recorded plaintext digest and recorded, in one write, with its
// per-copy encryption record. Until then the plaintext object and its ACTIVE
// placement are untouched, a crash at any phase boundary resumes from the
// journal (core/tests/movecrash has a cell for each), and a copy that fails
// verification is discarded, never catalogued.
//
// The one extra fact is the opt-in, state.RecordEncryptExisting: a row bound
// to the move's id. copyMediumToMedium consults it, and without it the
// refusal that was there stays exactly as it was, now naming this verb. The
// retention cycle never writes one, so a policy change cannot turn into a
// rewrite, and because the row names a move and not an artifact, a later
// move of the same artifact to the same medium finds none.
//
// # Why a different medium
//
// The journal holds one placement per artifact per medium, and the object key
// on a medium is a function of the artifact, so an encrypted sibling cannot
// sit beside its plaintext source on the same medium: the placement row would
// be overwritten and the plaintext object left behind with nothing recording
// it, or the key would be overwritten and the only good copy lost. The
// destination therefore has to be another medium, and "encrypt in place" is
// not offered. See Plan.DestinationMedium.
//
// # What the plaintext is read through
//
// The plaintext object is streamed from its medium straight into the
// encrypting pass (copycrypt.EncryptToStaging checks it against the recorded
// digest as it goes), so plaintext is never written to local disk and no
// buffer holds the artifact. The only local file is the ciphertext in the
// staging directory, which the move engine already sweeps.
//
// # Cancel
//
// A cancelled context ends a run in one of two documented states. Before
// VERIFIED the move is abandoned: the destination object and the staging area
// are discarded and the plaintext copy is exactly as it was. From VERIFIED on
// the encrypted copy is already a catalogued, verified copy, and discarding
// verified data to tidy up is the thing phases.go refuses to do, so the move
// is left where it is and the next run (or the next ordinary cycle, which
// needs no opt-in to finish a move that is past VERIFIED) retires the
// plaintext. The terminal's interrupt and the browser's cancel both end in a
// cancelled context handed to EncryptExisting, which is why they leave the
// same state.

// The refusals. Each is a distinct error so the dry run can say which one
// applies to which copy and a caller can ask errors.Is. None of them is
// returned after anything has been written, and none touches a plaintext copy.
var (
	// ErrEncryptExistingNotRequired: the backup set does not require
	// retained-copy encryption, so there is nothing to bring a copy into line
	// with. Turn the policy on first.
	ErrEncryptExistingNotRequired = errors.New("placement: the backup set does not require retained-copy encryption")

	// ErrEncryptExistingAlreadyEncrypted: the copy is already ciphertext.
	ErrEncryptExistingAlreadyEncrypted = errors.New("placement: the copy is already encrypted")

	// ErrEncryptExistingNoPlaintextCopy: the artifact has no ACTIVE copy on a
	// medium. A copy on local disk is plaintext by design, and a hop from it
	// to a medium already encrypts.
	ErrEncryptExistingNoPlaintextCopy = errors.New("placement: the artifact has no ACTIVE copy on a medium to encrypt")

	// ErrEncryptExistingNoKey: no key is available to encrypt to. It also
	// wraps ErrClassUnavailable, the class every key problem already is.
	ErrEncryptExistingNoKey = errors.New("placement: no key is available to encrypt the copy under")

	// ErrEncryptExistingDestinationClass: the destination cannot prove an
	// encrypted copy decrypts (only a content-class verification can), or is
	// local disk, which is plaintext by design.
	ErrEncryptExistingDestinationClass = errors.New("placement: the destination cannot prove an encrypted copy decrypts")

	// ErrEncryptExistingNoRoom: the staging area on the backup set's own disk
	// cannot hold the ciphertext.
	ErrEncryptExistingNoRoom = errors.New("placement: no room to stage the ciphertext")

	// ErrEncryptExistingSourceUnreadable: the plaintext copy cannot be read
	// right now (an archive class with no restore in effect, say). It also
	// wraps the class error the reading check returned.
	ErrEncryptExistingSourceUnreadable = errors.New("placement: the plaintext copy cannot be read")
)

// consentJournal is the slice of the journal the opt-in lives in. *state.Journal
// satisfies it; a journal that does not has no way to record an opt-in, so
// nothing is encrypted in place of an existing copy against it. It is a type
// the engine needs by contract (Journal is the narrow MoveJournal and a crash
// harness decorates it), not a wrapper.
type consentJournal interface {
	RecordEncryptExisting(ctx context.Context, moveID int64, at time.Time) error
	EncryptExistingConsented(ctx context.Context, moveID int64) (bool, error)
}

// retroConsented reports whether an operator opted this move in. A journal
// that cannot say has no opted-in moves.
func (e *Engine) retroConsented(ctx context.Context, mv state.Move) (bool, error) {
	j, ok := e.Journal.(consentJournal)
	if !ok {
		return false, nil
	}
	return j.EncryptExistingConsented(ctx, mv.ID)
}

// CheckEncryptExisting answers whether p could be run, in the order a run
// would find out, and returns one of the named refusals (or ErrNotEligible,
// which eligibleSource wraps for the move engine's own reasons) when it
// cannot. It writes nothing and reads no object, which is what lets the
// dry run share it with the run: the two cannot disagree about a copy.
func (e *Engine) CheckEncryptExisting(ctx context.Context, p Plan) error {
	rec, err := e.Journal.Get(ctx, p.Artifact)
	if err != nil {
		return err
	}
	on, err := e.encryptedSet(p.Artifact.Set)
	if err != nil {
		return err
	}
	if !on {
		return fmt.Errorf("%w: %s does not require it, so turn the policy on first", ErrEncryptExistingNotRequired, p.Artifact.Set)
	}
	if _, err := e.activeKey(ctx, p.Artifact); err != nil {
		return fmt.Errorf("%w: %w", ErrEncryptExistingNoKey, err)
	}

	var src state.Placement
	var found bool
	for _, pl := range rec.Placements {
		if pl.Status == state.PlacementActive && pl.Medium != state.MediumLocal {
			src, found = pl, true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %s has no ACTIVE copy on a medium (a copy on local disk is plaintext by design, and a hop from it to a medium already encrypts)",
			ErrEncryptExistingNoPlaintextCopy, p.Artifact)
	}
	if encrypted, err := e.sourceIsEncrypted(ctx, p.Artifact, src.Medium); err != nil {
		return err
	} else if encrypted {
		return fmt.Errorf("%w: the copy of %s on %q is ciphertext already", ErrEncryptExistingAlreadyEncrypted, p.Artifact, src.Medium)
	}

	if p.DestinationMedium == state.MediumLocal {
		return fmt.Errorf("%w: local disk is plaintext by design, so an encrypted copy has to go to a medium", ErrEncryptExistingDestinationClass)
	}
	dest, class, err := e.resolve(p.DestinationMedium)
	if err != nil {
		return err
	}
	if class != Content {
		return fmt.Errorf("%w: %q verifies at %s, and nothing weaker than a completed decrypt-verify may stand in for content", ErrEncryptExistingDestinationClass, dest.ID, class)
	}

	// The move engine's own eligibility: COMPLETE, one ACTIVE copy, a recorded
	// SHA-256, a destination that is not the source's medium, somewhere to stage.
	if _, err := e.eligibleSource(rec, p.DestinationMedium); err != nil {
		return err
	}

	source, _, err := e.resolve(src.Medium)
	if err != nil {
		return err
	}
	if err := e.sourceCanBeStaged(ctx, source, src); err != nil {
		return fmt.Errorf("%w: %w", ErrEncryptExistingSourceUnreadable, err)
	}
	staged, err := e.stagingPath(p.Artifact)
	if err != nil {
		return err
	}
	if err := e.roomToStage(staged, src); err != nil {
		return fmt.Errorf("%w: %w", ErrEncryptExistingNoRoom, err)
	}
	return nil
}

// EncryptExisting is the operator's entry point: for each plan, encrypt the
// plaintext copy that already sits on a medium onto plan.DestinationMedium,
// retiring the plaintext copy only after the encrypted one has been
// decrypt-verified. It is not part of RunCycle and nothing calls it on a
// schedule.
//
// It first resumes moves an earlier run left in flight (those with a recorded
// opt-in, and a move planned for one of these plans but never opted in, which
// is what a crash between the plan and the opt-in write leaves), then plans
// and runs the rest. A plan that cannot be run is reported as refused with its
// reason and plans nothing. The per-run bound is MaxMovesPerCycle, as for a
// cycle; refusals do not spend it. Moves run one at a time, so memory is the
// encrypting pass's fixed buffers and nothing more.
func (e *Engine) EncryptExisting(ctx context.Context, plans []Plan) (CycleReport, error) {
	var report CycleReport
	if e.Journal == nil {
		return report, fmt.Errorf("placement: the move engine needs a journal")
	}
	consents, ok := e.Journal.(consentJournal)
	if !ok {
		return report, fmt.Errorf("placement: this journal cannot record an opt-in, so nothing can be encrypted in place of an existing copy")
	}
	if e.MaxMovesPerCycle <= 0 {
		return report, nil
	}

	live, err := e.Journal.ListMoves(ctx, NonTerminalPhaseStrings()...)
	if err != nil {
		return report, err
	}
	wanted := make(map[string]string, len(plans))
	for _, p := range plans {
		wanted[p.Artifact.String()] = p.DestinationMedium
	}

	budget := e.MaxMovesPerCycle
	handled := map[string]bool{}
	for _, mv := range live {
		if ctx.Err() != nil {
			return report, nil
		}
		consented, err := consents.EncryptExistingConsented(ctx, mv.ID)
		if err != nil {
			return report, err
		}
		if !consented {
			adopt, err := e.adoptable(ctx, mv, wanted)
			if err != nil {
				return report, err
			}
			if !adopt {
				continue
			}
		}
		if budget == 0 {
			break
		}
		if !consented {
			if err := consents.RecordEncryptExisting(ctx, mv.ID, e.now()); err != nil {
				return report, err
			}
		}
		budget--
		report.Resumed++
		handled[mv.Artifact.String()] = true
		report.record(e.advanceEncryptExisting(ctx, mv, true))
	}

	for _, p := range plans {
		if ctx.Err() != nil {
			break
		}
		if handled[p.Artifact.String()] {
			continue
		}
		if budget == 0 {
			break
		}
		if err := e.CheckEncryptExisting(ctx, p); err != nil {
			report.Refused++
			report.Outcomes = append(report.Outcomes, Outcome{Artifact: p.Artifact, Refused: err.Error(), Err: err})
			continue
		}
		mv, err := e.plan(ctx, p)
		if err != nil {
			report.Refused++
			report.Outcomes = append(report.Outcomes, Outcome{Artifact: p.Artifact, Refused: err.Error(), Err: err})
			continue
		}
		budget--
		report.Planned++
		if err := consents.RecordEncryptExisting(ctx, mv.ID, e.now()); err != nil {
			// Planned and not opted in: nothing has been copied, and the next
			// run for this artifact and destination adopts it.
			report.Refused++
			report.Outcomes = append(report.Outcomes, Outcome{Artifact: p.Artifact, MoveID: mv.ID, Phase: Planned, Refused: err.Error(), Err: err})
			continue
		}
		report.record(e.advanceEncryptExisting(ctx, mv, false))
	}
	return report, nil
}

// adoptable reports whether a live move nobody opted in to is the one an
// explicit run was asked for: the same artifact and destination, from a
// plaintext medium copy, before anything was copied.
func (e *Engine) adoptable(ctx context.Context, mv state.Move, wanted map[string]string) (bool, error) {
	if wanted[mv.Artifact.String()] != mv.DestinationMedium || mv.SourceMedium == state.MediumLocal {
		return false, nil
	}
	if p := Phase(mv.Phase); p != Planned && p != Copying {
		return false, nil
	}
	encrypted, err := e.sourceIsEncrypted(ctx, mv.Artifact, mv.SourceMedium)
	if err != nil {
		return false, err
	}
	return !encrypted, nil
}

// advanceEncryptExisting drives one move and, if the run was cancelled,
// brings it to one of the two documented end states. The cleanup runs on a
// context that outlives the cancel, because a cancel that could not clean up
// after itself would leave exactly the half-finished state it was asked to
// avoid.
func (e *Engine) advanceEncryptExisting(ctx context.Context, mv state.Move, resumed bool) Outcome {
	out := e.advance(ctx, mv, resumed)
	if ctx.Err() == nil {
		return out
	}
	cleanup := context.WithoutCancel(ctx)
	live, err := e.Journal.ListMoves(cleanup, NonTerminalPhaseStrings()...)
	if err != nil {
		out.Err, out.Refused = err, err.Error()
		return out
	}
	for _, cur := range live {
		if cur.ID != mv.ID {
			continue
		}
		switch Phase(cur.Phase) {
		case Planned, Copying, Copied, Verifying:
			next, err := e.abandon(cleanup, cur, "cancelled before the encrypted copy was verified; the plaintext copy was not touched")
			if err != nil {
				out.Err, out.Refused = err, err.Error()
				return out
			}
			out.Phase, out.Err, out.Refused = Phase(next.Phase), nil, next.Error
		default:
			out.Phase = Phase(cur.Phase)
			out.Refused = "cancelled after the encrypted copy was verified; the next run, or the next ordinary cycle, finishes retiring the plaintext copy"
		}
		return out
	}
	return out
}

// openEncryptionSource opens the bytes an encrypting pass reads: the local
// artifact for a local-to-medium copy, or the plaintext object on its medium
// for #1291's encrypt-existing. A medium source is asked first whether it can
// be read at all (an archive class with no restore in effect cannot), so the
// engine spends no GET on an object it knows will answer InvalidObjectState.
func (e *Engine) openEncryptionSource(ctx context.Context, src state.Placement) (io.ReadCloser, error) {
	if src.Medium == state.MediumLocal {
		return e.Local.Open(ctx, src.Location)
	}
	if e.Store == nil {
		return nil, fmt.Errorf("no medium store is configured")
	}
	medium, _, err := e.resolve(src.Medium)
	if err != nil {
		return nil, err
	}
	if err := e.sourceCanBeStaged(ctx, medium, src); err != nil {
		return nil, err
	}
	return e.Store.OpenObject(ctx, medium, src.Location)
}
