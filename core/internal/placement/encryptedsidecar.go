package placement

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/retnd/retnd/core/internal/recovery"
	"github.com/retnd/retnd/core/internal/state"
)

// The recovery sidecar's encryption block (EPIC N, #992, FR-62).
//
// A deployment that has lost its state database rebuilds its catalog from the
// sidecars beside the artifacts (internal/app/catalog.go), and a sidecar
// written at commit predates any move: it knows the local copy and nothing
// else. So when a move lands an encrypted copy, the sidecar is rewritten with
// the artifact's placements, the encrypted ones carrying their non-secret
// metadata (format, key id, both digests and sizes, the time of the
// decrypt-verify that admitted the copy), BEFORE the source is marked for
// deletion. A procedure that needed a working deployment to learn which key
// a copy wants would not be a recovery procedure.
//
// It is written in the move, ahead of the source delete, rather than after
// it, because the failure it guards is the worst one this feature has: the
// only thing that can ever open an encrypted copy is its key, and the only
// thing that says which key is the sidecar. A write that fails stops the move
// with the source untouched, and the next cycle tries again.
//
// What this does not do: it never creates a sidecar. A set with no sidecar
// for the artifact (none was written at commit) is left as it is, because a
// manifest cannot be composed from a placement alone (it needs the remote
// path, the producer and received timestamps and the validation outcome that
// only commit saw). It is also not refreshed by a later scrub, so the
// decrypt-verification time in a sidecar is as of the last move that wrote it.
// Both are listed in the issue's hand-over.

// refreshSidecar rewrites the artifact's local sidecar for a move whose
// destination is an encrypted copy. It is a no-op for every other move, so a
// deployment that encrypts nothing writes exactly the files it always wrote.
func (e *Engine) refreshSidecar(ctx context.Context, mv state.Move, rec state.Record) error {
	if _, encrypted, err := e.encryptionOf(ctx, mv.Artifact, mv.DestinationMedium); err != nil {
		return err
	} else if !encrypted {
		return nil
	}
	bs, err := e.backupSet(mv.Artifact.Set)
	if err != nil {
		return err
	}
	if bs.LocalPath == "" {
		return nil
	}
	path := recovery.ManifestPath(bs.LocalPath, mv.Artifact.Name)
	m, err := recovery.ReadManifest(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("placement: %s's sidecar cannot be read, so it cannot be told about the encrypted copy on %q: %w", mv.Artifact, mv.DestinationMedium, err)
	}

	placements := make([]recovery.ManifestPlacement, 0, len(rec.Placements))
	for _, p := range rec.Placements {
		status := p.Status
		if p.Medium == mv.SourceMedium && status == state.PlacementActive {
			// What the journal is about to hold, so the sidecar never claims
			// a copy that is on its way out.
			status = state.PlacementDeletePending
		}
		mp := recovery.ManifestPlacement{
			Medium: p.Medium, Location: p.Location, SizeBytes: p.Size,
			Checksum: p.Hash, ChecksumAlgorithm: p.HashAlg,
			VerificationClass: p.VerificationClass, Status: status,
		}
		enc, encrypted, err := e.encryptionOf(ctx, mv.Artifact, p.Medium)
		if err != nil {
			return err
		}
		if encrypted {
			me := &recovery.ManifestEncryption{
				FormatID: enc.FormatID, FormatVersion: enc.FormatVersion, KeyID: enc.KeyID,
				PlaintextDigest: p.Hash, PlaintextDigestAlgorithm: p.HashAlg,
				StoredDigest: enc.StoredHash, StoredDigestAlgorithm: enc.StoredHashAlg, StoredSize: enc.StoredSize,
			}
			if p.Size != nil {
				me.PlaintextSize = *p.Size
			}
			if p.VerificationClass == state.VerificationContent && p.VerifiedAt != nil {
				at := *p.VerifiedAt
				me.LastDecryptVerified = &at
			}
			mp.Encryption = me
		}
		placements = append(placements, mp)
	}
	m.Placements = placements
	if err := recovery.WriteManifest(bs.LocalPath, m); err != nil {
		return fmt.Errorf("placement: writing %s's sidecar with the encrypted copy on %q: %w", mv.Artifact, mv.DestinationMedium, err)
	}
	return nil
}
