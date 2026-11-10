package placement

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
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
//
// # Re-encrypting an encrypted copy (#1317, EPIC T)
//
// The source may also be an ENCRYPTED copy whose suite or key is not the
// active one, when the plan says Reencrypt. Nothing about the move changes:
// same journal, same opt-in row (the 0019 table, reused as it is), same
// destination rule, same decrypt-verify against the artifact's recorded
// plaintext digest before the source is retired. The only difference is the
// bytes the encrypting pass reads: the ciphertext object is streamed from its
// medium through the old suite's Open, under the key its own record names,
// straight into the active suite's Seal (withEncryptionSource). The plaintext
// exists only in the two passes' buffers; it is never written to local disk,
// and the only local file is the new ciphertext in the staging directory.
//
// A superseded key still opens its copies, so a copy under one is
// re-encrypted. A key that cannot be resolved at all is a refusal before
// anything is planned, ErrReencryptSourceKeyUnavailable, and the copy is left
// as it is and named. A copy already under the active suite and key is
// ErrReencryptAlreadyCurrent. The same medium is refused for the reason above,
// ErrEncryptExistingSameMedium, and the refusal says what to do instead.

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

	// ErrEncryptExistingSameMedium: the destination is the medium the copy is
	// already on. The journal holds one placement per artifact per medium and
	// the object key is derived from the artifact, so the new copy would
	// overwrite the only good one. The refusal names the alternatives.
	ErrEncryptExistingSameMedium = errors.New("placement: the destination is the medium the copy is already on")

	// ErrReencryptAlreadyCurrent: the encrypted copy is already under the
	// active suite and the active key, so there is nothing to rewrite (#1317).
	ErrReencryptAlreadyCurrent = errors.New("placement: the copy is already under the active suite and key")

	// ErrReencryptSourceKeyUnavailable: the key the encrypted copy was written
	// under cannot be resolved, so the copy cannot be opened to be rewritten.
	// The copy is left exactly as it is. It also wraps ErrClassUnavailable,
	// the class every key problem already is (#1317).
	ErrReencryptSourceKeyUnavailable = errors.New("placement: the key the copy was written under cannot be resolved")
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
	key, err := e.activeKey(ctx, p.Artifact)
	if err != nil {
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
	enc, encrypted, err := e.encryptionOf(ctx, p.Artifact, src.Medium)
	if err != nil {
		return err
	}
	if encrypted {
		if err := e.checkReencryptSource(ctx, p, src, enc, key); err != nil {
			return err
		}
	}

	if p.DestinationMedium == src.Medium {
		return fmt.Errorf("%w: the copy of %s is on %q already, and the journal holds one copy per medium with an object key derived from the artifact, so a new copy there would overwrite the only good one; name a second medium, or, where a plaintext local copy still exists, let the ordinary move write a fresh copy from it (that needs no verb)",
			ErrEncryptExistingSameMedium, p.Artifact, src.Medium)
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

// checkReencryptSource is CheckEncryptExisting's part for an encrypted source:
// it is refused unless the plan asked for a re-encryption, unless the copy is
// under something other than the active suite and key, and unless the copy
// can be opened (its suite is one this build reads and its key resolves). It
// reads no object: resolving the key is a callback that returns at once.
func (e *Engine) checkReencryptSource(ctx context.Context, p Plan, src state.Placement, enc state.PlacementEncryption, active keycustody.KeyRecord) error {
	if !p.Reencrypt {
		return fmt.Errorf("%w: the copy of %s on %q is ciphertext already", ErrEncryptExistingAlreadyEncrypted, p.Artifact, src.Medium)
	}
	if enc.FormatID == keySuite(active) && enc.KeyID == active.ID {
		return fmt.Errorf("%w: the copy of %s on %q is under suite %s and key %s, which are the active ones",
			ErrReencryptAlreadyCurrent, p.Artifact, src.Medium, enc.FormatID, enc.KeyID)
	}
	if _, ok := copycrypt.Lookup(enc.FormatID); !ok {
		return fmt.Errorf("%w: the copy of %s on %q records suite %q, which this build cannot read, so it is left as it is",
			ErrEncryptExistingSourceUnreadable, p.Artifact, src.Medium, enc.FormatID)
	}
	if err := e.Keys.WithKey(ctx, enc.KeyID, func([]byte) error { return nil }); err != nil {
		return fmt.Errorf("%w: %w: the copy of %s on %q was written under key %s, which does not resolve (%v), so it is left as it is",
			ErrReencryptSourceKeyUnavailable, ErrClassUnavailable, p.Artifact, src.Medium, enc.KeyID, err)
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
	wanted := make(map[string]Plan, len(plans))
	for _, p := range plans {
		wanted[p.Artifact.String()] = p
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
// plaintext medium copy (or, for a plan that asked to re-encrypt, an encrypted
// one), before anything was copied.
func (e *Engine) adoptable(ctx context.Context, mv state.Move, wanted map[string]Plan) (bool, error) {
	p, ok := wanted[mv.Artifact.String()]
	if !ok || p.DestinationMedium != mv.DestinationMedium || mv.SourceMedium == state.MediumLocal {
		return false, nil
	}
	if ph := Phase(mv.Phase); ph != Planned && ph != Copying {
		return false, nil
	}
	encrypted, err := e.sourceIsEncrypted(ctx, mv.Artifact, mv.SourceMedium)
	if err != nil {
		return false, err
	}
	return !encrypted || p.Reencrypt, nil
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
			next, err := e.abandon(cleanup, cur, "cancelled before the encrypted copy was verified; the copy it was made from was not touched")
			if err != nil {
				out.Err, out.Refused = err, err.Error()
				return out
			}
			out.Phase, out.Err, out.Refused = Phase(next.Phase), nil, next.Error
		default:
			out.Phase = Phase(cur.Phase)
			out.Refused = "cancelled after the encrypted copy was verified; the next run, or the next ordinary cycle, finishes retiring the copy it was made from"
		}
		return out
	}
	return out
}

// withEncryptionSource hands fn the plaintext an encrypting pass reads, and
// closes what it opened when fn returns. For a plaintext source that is the
// local artifact (a local-to-medium copy) or the plaintext object on its
// medium (#1291). For an encrypted source (#1317) it is the ciphertext object
// streamed through the suite its own record names, opened under the key its
// own record names, never the active ones: the plaintext lives in the two
// passes' buffers and is never written to local disk.
//
// The key is resolved BEFORE the object is opened, inside custody's callback,
// so a key that cannot be had costs no read, and fn runs inside the same
// callback because custody's secret does not outlive it.
func (e *Engine) withEncryptionSource(ctx context.Context, mv state.Move, src state.Placement, fn func(plaintext io.Reader) error) error {
	enc, encrypted := state.PlacementEncryption{}, false
	if src.Medium != state.MediumLocal {
		var err error
		if enc, encrypted, err = e.encryptionOf(ctx, mv.Artifact, src.Medium); err != nil {
			return err
		}
	}
	if !encrypted {
		rc, err := e.openEncryptionSource(ctx, src)
		if err != nil {
			return err
		}
		defer rc.Close()
		return fn(rc)
	}

	suite, ok := copycrypt.Lookup(enc.FormatID)
	if !ok {
		return fmt.Errorf("%w: %w: the copy of %s on %q records suite %q, which this build cannot read",
			ErrClassUnavailable, ErrEncryptExistingSourceUnreadable, mv.Artifact, src.Medium, enc.FormatID)
	}
	if e.Keys == nil {
		return fmt.Errorf("%w: %w: no key custody is configured to open the copy of %s on %q",
			ErrClassUnavailable, ErrReencryptSourceKeyUnavailable, mv.Artifact, src.Medium)
	}
	if e.Store == nil {
		return fmt.Errorf("no medium store is configured")
	}
	medium, _, err := e.resolve(src.Medium)
	if err != nil {
		return err
	}
	if err := e.sourceCanBeStaged(ctx, medium, src); err != nil {
		return err
	}

	var (
		resolved bool
		in       notingReader // the ciphertext as it comes off the medium
		out      notingReader // the plaintext as the old suite releases it
	)
	err = e.Keys.WithKey(ctx, enc.KeyID, func(secret []byte) error {
		resolved = true
		rc, err := e.Store.OpenObject(ctx, medium, src.Location)
		if err != nil {
			return readFailure{err}
		}
		defer rc.Close()
		in.r = rc
		plain, err := suite.Open(secret, &in)
		if err != nil {
			if in.err != nil {
				return readFailure{in.err}
			}
			return undecryptable{err}
		}
		out.r = plain
		err = fn(&out)
		switch {
		case err == nil:
			return nil
		case in.err != nil:
			return readFailure{in.err}
		case out.err != nil:
			return undecryptable{out.err}
		}
		return err
	})
	var rf readFailure
	var bad undecryptable
	switch {
	case err == nil:
		return nil
	case !resolved:
		return fmt.Errorf("%w: %w: key %s, which the copy of %s on %q was written under, cannot be resolved, so the copy is left as it is: %w",
			ErrClassUnavailable, ErrReencryptSourceKeyUnavailable, enc.KeyID, mv.Artifact, src.Medium, err)
	case errors.As(err, &rf):
		// A read that failed is the medium's problem, not the copy's, and the
		// move retries it like any other failed copy.
		return fmt.Errorf("reading the copy of %s on %q: %w", mv.Artifact, src.Medium, rf.err)
	case errors.As(err, &bad):
		// The bytes at the key do not open under their own key and suite. No
		// retry can change that, so the move is abandoned with the copy as it
		// is; nothing was uploaded.
		return fmt.Errorf("%w: the copy of %s on %q does not decrypt under key %s and suite %s, so it cannot be re-encrypted and is left as it is: %v",
			ErrClassRefused, mv.Artifact, src.Medium, enc.KeyID, enc.FormatID, bad.err)
	}
	return err
}

// notingReader remembers the first error other than EOF its reader returned,
// so a failure of the medium read is told apart from a failure of the
// decryption that reads from it.
type notingReader struct {
	r   io.Reader
	err error
}

func (n *notingReader) Read(p []byte) (int, error) {
	k, err := n.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && n.err == nil {
		n.err = err
	}
	return k, err
}

// undecryptable marks an error from opening or authenticating the old
// ciphertext, as opposed to one from reading it off the medium.
type undecryptable struct{ err error }

func (u undecryptable) Error() string { return u.err.Error() }
func (u undecryptable) Unwrap() error { return u.err }

// openEncryptionSource opens the bytes an encrypting pass reads from a
// plaintext source: the local artifact for a local-to-medium copy, or the
// plaintext object on its medium for #1291's encrypt-existing. A medium source
// is asked first whether it can be read at all (an archive class with no
// restore in effect cannot), so the engine spends no GET on an object it knows
// will answer InvalidObjectState.
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
