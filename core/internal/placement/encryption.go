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
//   - Copy: the artifact is read through LocalStore.Open, encrypted into the
//     staging directory (the same .moves directory a medium-to-medium hop
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
// Moving an encrypted copy anywhere (back to local, or to another medium)
// is refused here with ErrClassRefused: those paths would copy ciphertext as
// if it were the artifact, and carrying the record to the new row is #992's.

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

// sourceIsEncrypted reports whether the copy at (medium) is an encrypted one
// according to the journal. A journal that cannot say is not asked: it has
// no encrypted copies.
func (e *Engine) sourceIsEncrypted(ctx context.Context, artifact model.ArtifactID, medium string) (bool, error) {
	r, ok := e.Journal.(interface {
		PlacementEncryption(ctx context.Context, artifact model.ArtifactID, medium string) (state.PlacementEncryption, bool, error)
	})
	if !ok {
		return false, nil
	}
	_, enc, err := r.PlacementEncryption(ctx, artifact, medium)
	return enc, err
}

func refuseEncryptedMove(artifact model.ArtifactID, from string) error {
	return fmt.Errorf("%w: the copy of %s on %q is encrypted, and moving it would copy ciphertext as if it were the artifact; re-homing an encrypted copy is not implemented (#992)",
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

	rc, err := e.Local.Open(ctx, src.Location)
	if err != nil {
		return 0, err
	}
	pass, encErr := copycrypt.EncryptToStaging(ctx, dir, filepath.Base(staged), rc, key.Recipient, src.Hash)
	_ = rc.Close()
	if encErr != nil {
		if errors.Is(encErr, copycrypt.ErrKeyUnavailable) {
			return 0, fmt.Errorf("%w: %w", ErrClassUnavailable, encErr)
		}
		return 0, encErr
	}

	res, uploadErr := e.Store.UploadFromLocal(ctx, medium, pass.Path, mv.DestinationKey, transport.UploadOptions{})

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

// verifyEncrypted is verifyCopy for a destination under a set that requires
// encryption. Only the content class can say an encrypted copy is the
// artifact, so any other class is refused (no retry can change that), and
// the archive gate in front of a read is asked exactly as it is for a
// plaintext copy. obs is the observation verifyCopy took (the one place
// allowed to), so this file never calls observe itself.
func (e *Engine) verifyEncrypted(ctx context.Context, mv state.Move, medium transport.Medium, want Class, candidate state.Placement, gated bool, obs archive.Observation) (Result, Class, error) {
	if want != Content {
		return Result{}, want, fmt.Errorf("%w: %s verification cannot show that an encrypted copy decrypts to the artifact, and nothing weaker than a completed decrypt-verify may stand in for content",
			ErrClassRefused, want)
	}
	key, err := e.activeKey(ctx, mv.Artifact)
	if err != nil {
		return Result{}, want, err
	}
	if gated {
		access, err := archive.Access(candidate.Medium, medium.StorageClass, obs, e.now())
		if err != nil {
			return Result{}, want, fmt.Errorf("%w: %w", ErrClassUnavailable, err)
		}
		if err := CheckClass(access, want); err != nil {
			return Result{}, want, err
		}
	}
	res, err := verifyEncryptedContent(ctx, e.Store, e.Keys, key.ID, medium, candidate, e.now())
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
		return Result{}, fmt.Errorf("%w: no key is available to decrypt %q", ErrClassUnavailable, p.Location)
	}
	rc, err := store.OpenObject(ctx, medium, p.Location)
	if err != nil {
		return Result{}, fmt.Errorf("%w: reading %q back from %q: %w", ErrClassUnavailable, p.Location, p.Medium, err)
	}
	defer rc.Close()

	stored := &tally{h: sha256.New()}
	tee := io.TeeReader(rc, stored)
	err = keys.WithKey(ctx, keyID, func(secret []byte) error {
		_, err := copycrypt.DecryptVerify(ctx, tee, secret, p.Hash)
		return err
	})
	switch {
	case err == nil:
	case errors.Is(err, copycrypt.ErrCorrupt), errors.Is(err, copycrypt.ErrWrongKey), errors.Is(err, copycrypt.ErrDigestMismatch):
		return Result{Class: Content, Passed: false, At: now, Detail: fmt.Sprintf(
			"%s on %s did not decrypt to the artifact: %v", p.Location, p.Medium, err)}, nil
	default:
		return Result{}, fmt.Errorf("%w: decrypting %q from %q: %w", ErrClassUnavailable, p.Location, p.Medium, err)
	}
	// Whatever follows the final chunk is an error DecryptVerify already
	// returned; draining makes the stored digest cover every byte at the key.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return Result{}, fmt.Errorf("%w: reading %q back from %q: %w", ErrClassUnavailable, p.Location, p.Medium, err)
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
