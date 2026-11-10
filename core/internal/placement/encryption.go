package placement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/retnd/retnd/core/internal/archive"
	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// This file is where the move engine meets retained-copy encryption (EPIC N,
// N1.3, #991, ADR 0027). A backup set whose resolved
// retained_copy_encryption is "required" sends a local-to-medium copy
// through the encrypted staging pass (copycrypt.EncryptToStaging), and the
// copy that lands is only ever accepted by a streaming decrypt-verify.
//
// # Where the pieces sit
//
//   - Copy: the artifact is read through LocalStore.Open (or, for a plaintext
//     copy an operator asked to encrypt, streamed from its medium: #1291),
//     encrypted into the staging directory (the same .moves directory a medium-to-medium hop
//     uses, so sweepStagingTemps and discardDestination cover it), and the
//     staged file goes to UploadFromLocal unchanged. The staging file is
//     removed whether the upload worked or not. It is never a placement.
//   - Verify: a content-class verification of an encrypted copy streams the
//     object through copycrypt.DecryptVerify against the artifact's
//     plaintext digest (placements.hash), and in the same pass hashes the
//     bytes at the key. That second number, with the size and the key id,
//     is the per-copy record. It is measured from the object that is there
//     now, in the attempt that is committing, so it can never describe an
//     earlier attempt's ciphertext.
//   - Commit: the record rides on the destination's PlacementUpdate and is
//     written by AdvanceMove in the same transaction as the placement row.
//
// # What is refused rather than approximated
//
// The existence and attested classes cannot prove an encrypted copy
// decrypts, and the attested comparison (object digest against the recorded
// plaintext digest) would compare two different numbers, so both are
// ErrClassUnavailable for an encrypted copy: never recorded, never a
// quiet downgrade. An unresolvable key is ErrClassUnavailable too, and it
// is found before anything is encrypted or uploaded.
//
// # Re-homing an encrypted copy (#992)
//
// A move of an encrypted copy from one medium to another is defined: the
// ciphertext is staged as it is, accepted only if it hashes to the
// stored-bytes digest the journal recorded (never the plaintext digest, which
// no ciphertext can match), uploaded unchanged, and the destination is
// accepted only by a decrypt-verify through the key id recorded against the
// SOURCE copy, whatever the active key is by now. Nothing is re-encrypted: a
// re-home is not a retry, and the destination row carries the same format
// and key id, with the stored-bytes digest measured from the object that is
// there. Copying the ciphertext needs no key; accepting it does, so with the
// key gone the move stops with the source untouched.
//
// Moving an encrypted copy back to local disk stays refused with
// ErrClassRefused, now for a stated reason: a local placement is plaintext by
// design (the epic's objective is one plaintext local copy and an unreadable
// off-site one), so bringing the artifact home is a decrypt, which is a
// restore and not a move of the copy.
//
// A plaintext copy on a medium under a set that now requires encryption is
// still refused a medium-to-medium hop (it would stage and upload the
// artifact in the clear). Encrypting copies that already exist in the clear
// is the operator's explicit act, EncryptExisting (retroencrypt.go, #1291),
// and it reuses this file's encrypting pass and verification unchanged: the
// plaintext object is the source of the same copyEncryptedToMedium.

// Keys is the slice of key custody the byte path uses. *keycustody.Store
// satisfies it. The identity is only ever handed to a callback, which is
// how custody keeps it from being a value anything holds.
type Keys interface {
	Active(ctx context.Context) (keycustody.KeyRecord, bool, error)
	WithKey(ctx context.Context, id string, fn func(key []byte) error) error
}

// encryptedSet reports whether the artifact's backup set requires its
// retained copies to be encrypted.
func (e *Engine) encryptedSet(set model.BackupSetID) (bool, error) {
	bs, err := e.backupSet(set)
	if err != nil {
		return false, err
	}
	return bs.RetainedCopyEncryption.Enabled(), nil
}

// activeKey resolves the key new copies of artifact are written under, and
// proves it can be read before any byte is encrypted. Every failure wraps
// ErrClassUnavailable. The key row a per-copy record references is the one
// custody itself wrote: keycustody.JournalRegistry is the encryption_keys
// table, so nothing here registers anything.
func (e *Engine) activeKey(ctx context.Context, artifact model.ArtifactID) (keycustody.KeyRecord, error) {
	bs, err := e.backupSet(artifact.Set)
	if err != nil {
		return keycustody.KeyRecord{}, err
	}
	if !bs.RetainedCopyEncryption.KeyProvider.Managed {
		return keycustody.KeyRecord{}, fmt.Errorf("%w: backup set %s requires encryption under a key provider this engine cannot resolve yet (only a managed key has a registry row to record copies under)",
			ErrClassUnavailable, artifact.Set)
	}
	if e.Keys == nil {
		return keycustody.KeyRecord{}, fmt.Errorf("%w: backup set %s requires encryption and no key custody is configured", ErrClassUnavailable, artifact.Set)
	}
	rec, ok, err := e.Keys.Active(ctx)
	if err != nil {
		return keycustody.KeyRecord{}, fmt.Errorf("%w: reading the active key: %w", ErrClassUnavailable, err)
	}
	if !ok {
		return keycustody.KeyRecord{}, fmt.Errorf("%w: backup set %s requires encryption and no key is active", ErrClassUnavailable, artifact.Set)
	}
	if err := e.Keys.WithKey(ctx, rec.ID, func([]byte) error { return nil }); err != nil {
		return keycustody.KeyRecord{}, fmt.Errorf("%w: key %s does not resolve: %w", ErrClassUnavailable, rec.ID, err)
	}
	return rec, nil
}

// encryptionOf reads the per-copy encryption record of the copy at medium.
// A journal that cannot say is not asked: it has no encrypted copies.
func (e *Engine) encryptionOf(ctx context.Context, artifact model.ArtifactID, medium string) (state.PlacementEncryption, bool, error) {
	r, ok := e.Journal.(interface {
		PlacementEncryption(ctx context.Context, artifact model.ArtifactID, medium string) (state.PlacementEncryption, bool, error)
	})
	if !ok {
		return state.PlacementEncryption{}, false, nil
	}
	return r.PlacementEncryption(ctx, artifact, medium)
}

// sourceIsEncrypted reports whether the copy at (medium) is an encrypted one
// according to the journal.
func (e *Engine) sourceIsEncrypted(ctx context.Context, artifact model.ArtifactID, medium string) (bool, error) {
	_, enc, err := e.encryptionOf(ctx, artifact, medium)
	return enc, err
}

func refuseEncryptedMove(artifact model.ArtifactID, from string) error {
	return fmt.Errorf("%w: the copy of %s on %q is encrypted, and a local placement is plaintext by design, so bringing it home is a decrypt (a restore), not a move of the copy",
		ErrClassRefused, artifact, from)
}

// copyEncryptedToMedium is the copy phase for a local source going to a
// medium under a set that requires encryption.
func (e *Engine) copyEncryptedToMedium(ctx context.Context, mv state.Move, src state.Placement) (int64, error) {
	if e.Store == nil {
		return 0, fmt.Errorf("no medium store is configured")
	}
	if e.Local == nil {
		return 0, fmt.Errorf("no local store is configured")
	}
	if !strings.EqualFold(src.HashAlg, string(transport.SHA256)) || src.Hash == "" {
		return 0, fmt.Errorf("%w: the artifact's recorded digest is %q %q, and the encrypting pass verifies its source against a recorded %s",
			ErrClassUnavailable, src.HashAlg, src.Hash, transport.SHA256)
	}
	medium, _, err := e.resolve(mv.DestinationMedium)
	if err != nil {
		return 0, err
	}
	key, err := e.activeKey(ctx, mv.Artifact)
	if err != nil {
		return 0, err
	}

	staged, err := e.stagingPath(mv.Artifact)
	if err != nil {
		return 0, err
	}
	dir := filepath.Dir(staged)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, fmt.Errorf("preparing the staging area for %s: %w", mv.Artifact, err)
	}
	if err := e.sweepStagingTemps(ctx, dir); err != nil {
		return 0, err
	}
	// Ciphertext is a little bigger than the artifact; the arithmetic is
	// the one a medium-to-medium hop does, against the plaintext size, and
	// the header and tags (about 0.024 percent) are inside the margin a
	// filesystem keeps. ADR 0027 names this cost.
	if err := e.roomToStage(staged, src); err != nil {
		return 0, err
	}

	rc, err := e.openEncryptionSource(ctx, src)
	if err != nil {
		return 0, err
	}
	pass, encErr := copycrypt.EncryptToStaging(ctx, dir, filepath.Base(staged), rc, key.Recipient, src.Hash)
	_ = rc.Close()
	e.Logger.CopyEncrypting(ctx, mv.Artifact.String(), mv.DestinationMedium, resultOf(encErr))
	if encErr != nil {
		if errors.Is(encErr, copycrypt.ErrKeyUnavailable) {
			return 0, fmt.Errorf("%w: %w", ErrClassUnavailable, encErr)
		}
		return 0, encErr
	}

	res, uploadErr := e.Store.UploadFromLocal(ctx, medium, pass.Path, mv.DestinationKey, transport.UploadOptions{})
	e.Logger.CopyUploadingEncrypted(ctx, mv.Artifact.String(), mv.DestinationMedium, resultOf(uploadErr))

	// The staged ciphertext goes whether the upload worked or not, for the
	// reason copyMediumToMedium gives for its staging copy. It is not a
	// placement and nothing may find it later and trust it.
	rmErr := e.Local.Remove(ctx, pass.Path)
	switch {
	case uploadErr != nil && rmErr != nil:
		return 0, fmt.Errorf("%w (and the staged ciphertext at %q could not be removed either: %v)", uploadErr, pass.Path, rmErr)
	case uploadErr != nil:
		return 0, uploadErr
	case rmErr != nil:
		return 0, fmt.Errorf("the copy to %q arrived and the staged ciphertext at %q could not be removed: %w", mv.DestinationMedium, pass.Path, rmErr)
	}
	return res.BytesUploaded, nil
}

// discardEncryptedStaging clears the encrypting pass's staging area for an
// artifact: the finished object and any temporary file a killed pass left.
// It is the piece of discardDestination that belongs to a local-to-medium
// move, so the one cleanup path covers both directions of staging.
func (e *Engine) discardEncryptedStaging(ctx context.Context, mv state.Move) error {
	if mv.SourceMedium != state.MediumLocal || mv.DestinationMedium == state.MediumLocal || e.Local == nil {
		return nil
	}
	on, err := e.encryptedSet(mv.Artifact.Set)
	if err != nil || !on {
		return nil
	}
	staged, err := e.stagingPath(mv.Artifact)
	if err != nil {
		return nil
	}
	if err := e.Local.Remove(ctx, staged); err != nil {
		return fmt.Errorf("placement: discarding %s's staged ciphertext at %q: %w", mv.Artifact, staged, err)
	}
	return e.sweepStagingTemps(ctx, filepath.Dir(staged))
}

// resultOf is the obs.Result a stage that returned err completed with.
func resultOf(err error) obs.Result {
	if err != nil {
		return obs.ResultError
	}
	return obs.ResultSuccess
}

// verifyEncrypted is verifyCopy for a destination that holds ciphertext:
// either a fresh encrypted copy under a set that requires encryption, or a
// re-home of an encrypted copy. Only the content class can say an encrypted
// copy is the artifact, so any other class is refused (no retry can change
// that), and the archive gate in front of a read is asked exactly as it is
// for a plaintext copy. observed is the observation verifyCopy took (the one
// place allowed to), so this file never calls observe itself.
//
// keyID is the key the copy must open under: empty for a fresh copy, which
// opens under the key it was just written with (the active one), and the
// source copy's recorded key id for a re-home.
func (e *Engine) verifyEncrypted(ctx context.Context, mv state.Move, medium transport.Medium, want Class, candidate state.Placement, gated bool, keyID string, observed archive.Observation) (Result, Class, error) {
	if want != Content {
		return Result{}, want, fmt.Errorf("%w: %s verification cannot show that an encrypted copy decrypts to the artifact, and nothing weaker than a completed decrypt-verify may stand in for content",
			ErrClassRefused, want)
	}
	if keyID == "" {
		key, err := e.activeKey(ctx, mv.Artifact)
		if err != nil {
			return Result{}, want, err
		}
		keyID = key.ID
	}
	if gated {
		access, err := archive.Access(candidate.Medium, medium.StorageClass, observed, e.now())
		if err != nil {
			return Result{}, want, fmt.Errorf("%w: %w", ErrClassUnavailable, err)
		}
		if err := CheckClass(access, want); err != nil {
			return Result{}, want, err
		}
	}
	res, err := verifyEncryptedContent(ctx, e.Store, e.Keys, keyID, medium, candidate, e.now())
	if gated {
		// One row per copy per stage, on the verification that is recorded;
		// the re-read before the source delete is not a stage of its own.
		artifact := mv.Artifact.String()
		if err == nil && res.Passed {
			e.Logger.CopyVerifyingStored(ctx, artifact, mv.DestinationMedium, obs.ResultSuccess)
			e.Logger.CopyDecryptVerifying(ctx, artifact, mv.DestinationMedium, keyID, obs.ResultSuccess)
		} else {
			e.Logger.CopyDecryptVerifying(ctx, artifact, mv.DestinationMedium, keyID, obs.ResultError)
		}
	}
	return res, want, err
}

// tally hashes what passes through it and counts it: the stored-bytes digest
// and size of the object at the key, which vary together.
type tally struct {
	h hash.Hash
	n int64
}

func (t *tally) Write(p []byte) (int, error) {
	t.n += int64(len(p))
	return t.h.Write(p)
}

// verifyEncryptedContent is content-class verification of an encrypted
// copy: the object at p.Location is streamed through a decrypt that must
// authenticate every chunk, reach a final chunk, and produce a plaintext
// whose digest equals the artifact's recorded one (p.Hash). Only then is the
// copy content-verified, and the Result carries the per-copy record,
// measured over the same read.
//
// A key that cannot be read is ErrClassUnavailable. A stream that fails
// authentication, or decrypts to something other than the artifact, is a
// failed verification and never an error: it is a fact about the object.
func verifyEncryptedContent(ctx context.Context, store Store, keys Keys, keyID string, medium transport.Medium, p state.Placement, now time.Time) (Result, error) {
	if p.Hash == "" || !strings.EqualFold(p.HashAlg, string(transport.SHA256)) {
		return Result{}, fmt.Errorf("%w: decrypt-verification compares against a recorded %s, and this placement records %q",
			ErrClassUnavailable, transport.SHA256, p.HashAlg)
	}
	if keys == nil || keyID == "" {
		return Result{}, fmt.Errorf("%w: %w: no key custody is available to open %q", ErrClassUnavailable, ErrKeyUnresolvable, p.Location)
	}

	// The key is resolved BEFORE the object is opened, inside the same
	// callback, so a key that cannot be had costs no read at all: "could not
	// check" must not be preceded by a download of something that cannot be
	// checked.
	var (
		resolved bool
		stored   = &tally{h: sha256.New()}
	)
	err := keys.WithKey(ctx, keyID, func(secret []byte) error {
		resolved = true
		rc, err := store.OpenObject(ctx, medium, p.Location)
		if err != nil {
			return readFailure{err}
		}
		defer rc.Close()
		tee := io.TeeReader(rc, stored)
		if _, err := copycrypt.DecryptVerify(ctx, tee, secret, p.Hash); err != nil {
			return err
		}
		// Whatever follows the final chunk is an error DecryptVerify already
		// returned; draining makes the stored digest cover every byte at the key.
		if _, err := io.Copy(io.Discard, tee); err != nil {
			return readFailure{err}
		}
		return nil
	})
	var rf readFailure
	switch {
	case err == nil:
	case !resolved:
		return Result{}, fmt.Errorf("%w: %w: key %s cannot be resolved: %w", ErrClassUnavailable, ErrKeyUnresolvable, keyID, err)
	case errors.As(err, &rf):
		return Result{}, fmt.Errorf("%w: reading %q back from %q: %w", ErrClassUnavailable, p.Location, p.Medium, rf.err)
	case errors.Is(err, copycrypt.ErrCorrupt), errors.Is(err, copycrypt.ErrWrongKey), errors.Is(err, copycrypt.ErrDigestMismatch):
		return Result{Class: Content, Passed: false, At: now, Detail: fmt.Sprintf(
			"%s on %s did not decrypt to the artifact: %v", p.Location, p.Medium, err)}, nil
	default:
		return Result{}, fmt.Errorf("%w: decrypting %q from %q: %w", ErrClassUnavailable, p.Location, p.Medium, err)
	}
	return Result{
		Class: Content, Passed: true, At: now,
		Detail: fmt.Sprintf("%s on %s was read back, decrypted and authenticated, and its plaintext hashes to the %s recorded at ingestion", p.Location, p.Medium, p.HashAlg),
		Encryption: &state.PlacementEncryption{
			FormatID: copycrypt.FormatID, FormatVersion: copycrypt.FormatVersion, KeyID: keyID,
			StoredHash: hex.EncodeToString(stored.h.Sum(nil)), StoredHashAlg: string(transport.SHA256), StoredSize: stored.n,
		},
	}, nil
}

// readFailure marks an error from reading the object back, as opposed to
// one from opening it with the key, so the two are told apart after WithKey.
type readFailure struct{ err error }

func (r readFailure) Error() string { return r.err.Error() }
func (r readFailure) Unwrap() error { return r.err }

// ErrKeyUnresolvable is what a verification wraps, beside ErrClassUnavailable,
// when the key the copy needs cannot be had. It lets a surface say "could not
// check, the key is not available" instead of the generic "could not be
// asked", and it is never a verdict about the copy.
var ErrKeyUnresolvable = errors.New("placement: the key this encrypted copy needs cannot be resolved")

// VerifyCopy is Verify for a copy that may be encrypted: enc is the copy's
// per-copy record, or nil for a plaintext copy, which is Verify exactly.
//
// For an encrypted copy the generic ladder would compare numbers that are
// never equal (the plaintext size and digest against the object's), so each
// class is answered for what it can honestly prove:
//
//   - Existence compares the STORED size recorded for the copy. It proves an
//     object of the recorded stored size is at the key and nothing about its
//     bytes, which is all it ever proved.
//   - Content is a streaming decrypt through the key id RECORDED against the
//     copy (never the active key, which is what makes a rotation orphan
//     nothing) whose plaintext digest must equal the artifact's. An
//     unresolvable key is ErrClassUnavailable wrapping ErrKeyUnresolvable and
//     costs no read. A passing Content notes it when the bytes at the key are
//     not the ones recorded at commit.
//   - Attested is ErrClassUnavailable: an endpoint's checksum of the object
//     proves the stored bytes are intact, not that they decrypt, and must not
//     wear the same class.
func VerifyCopy(ctx context.Context, store Store, keys Keys, medium transport.Medium, p state.Placement, enc *state.PlacementEncryption, want Class, now time.Time) (Result, error) {
	if enc == nil {
		return Verify(ctx, store, medium, p, want, now)
	}
	switch want {
	case Content:
		res, err := verifyEncryptedContent(ctx, store, keys, enc.KeyID, medium, p, now)
		if err == nil && res.Passed && res.Encryption != nil && res.Encryption.StoredHash != enc.StoredHash {
			res.Detail += fmt.Sprintf("; the bytes at the key hash to %s, not the %s recorded when the copy was committed, so the object was replaced after that (it still decrypts to the artifact)",
				res.Encryption.StoredHash, enc.StoredHash)
		}
		return res, err
	case Existence:
		stored := enc.StoredSize
		q := p
		q.Size = &stored
		res, err := Verify(ctx, store, medium, q, Existence, now)
		if err == nil {
			res.Detail += " (the stored size of an encrypted copy)"
		}
		return res, err
	default:
		return Result{}, fmt.Errorf("%w: %s verification cannot show that an encrypted copy decrypts to the artifact", ErrClassUnavailable, want)
	}
}
