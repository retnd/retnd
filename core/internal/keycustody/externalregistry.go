package keycustody

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/retnd/retnd/core/internal/secretref"
)

// External keys as registry entries (#1292).
//
// An external key is a row in the same encryption_keys table a managed key
// uses, with provider "external" and the recipient the reference resolved to.
// That is all the per-copy record needs (placement_encryption.key_id points
// at it), so re-home, verify and scrub, which already take a key id, are
// unchanged. The row holds no reference text and no material: the reference
// is config's, and a command reference can carry arguments that do not belong
// in a journal read by support exports.
//
// The id is derived from the recipient, so the same key reached through two
// references, or after a restart or a catalog rebuild, is one row.

var (
	// ErrExternalKeyUnresolvable is WithKey's refusal for an external key id
	// when no declared reference currently yields a key with the recorded
	// recipient. The callback is not called. The message names the
	// references tried (their descriptions) and secretref's own reasons,
	// never any material.
	ErrExternalKeyUnresolvable = errors.New("keycustody: no declared external reference currently yields the key recorded for this id")

	// ErrExternalKeyNotExportable is Export's refusal for an external key: a
	// bundle is the one moment material leaves the host, and an operator's
	// own key is already the operator's.
	ErrExternalKeyNotExportable = errors.New("keycustody: an external key is held by its operator and is not exported in a bundle")
)

// UseExternalRefs tells the store where the external references are declared.
// fn is called on every resolution, so a configuration reload is seen
// without reopening the store.
func (s *Store) UseExternalRefs(fn func() []secretref.Ref) { s.externals = fn }

// externalKeyID is "x" and sixteen hex digits of SHA-256 over the recipient:
// stable for the key, and told apart from a managed key's "k" id.
func externalKeyID(recipient string) string {
	sum := sha256.Sum256([]byte("retnd external key\x00" + recipient))
	return "x" + hex.EncodeToString(sum[:8])
}

// externalRecipient resolves ref and derives the recipient of the key it
// yields. The material never leaves the callback.
func (s *Store) externalRecipient(ctx context.Context, ref secretref.Ref) (string, error) {
	var recipient string
	err := WithExternalKey(ctx, ref, func(key []byte) error {
		r, err := s.format.Recipient(key)
		recipient = r
		return err
	})
	return recipient, err
}

// RegisterExternal resolves ref, checks the key is one the format can open,
// and returns its registry row, adding it when it is not there yet. It is
// idempotent per key. Nothing is written for a reference that does not
// resolve or does not yield a key of the format.
func (s *Store) RegisterExternal(ctx context.Context, ref secretref.Ref, now time.Time) (KeyRecord, error) {
	recipient, err := s.externalRecipient(ctx, ref)
	if err != nil {
		return KeyRecord{}, err
	}
	id := externalKeyID(recipient)
	if rec, err := s.record(ctx, id); err == nil {
		return rec, nil
	} else if !errors.Is(err, ErrUnknownKey) {
		return KeyRecord{}, err
	}
	rec := KeyRecord{ID: id, Provider: ProviderExternal, Recipient: recipient, Created: now.UTC(), Status: StatusActive}
	if err := s.registry.AddKey(ctx, rec, ""); err != nil {
		if errors.Is(err, ErrDuplicateKey) {
			// Another writer registered the same key between the read and the
			// write; it is the same row.
			return s.record(ctx, id)
		}
		return KeyRecord{}, err
	}
	return rec, nil
}

// RotateExternal registers the key newRef yields and supersedes the external
// key oldID in one durable write, with the semantics a managed rotation has:
// copies recorded against the old key keep decrypting through it, and
// nothing is re-encrypted.
func (s *Store) RotateExternal(ctx context.Context, oldID string, newRef secretref.Ref, now time.Time) (KeyRecord, error) {
	old, err := s.record(ctx, oldID)
	if err != nil {
		return KeyRecord{}, err
	}
	if old.Provider != ProviderExternal {
		return KeyRecord{}, fmt.Errorf("%w: %s is not an external key", ErrUnknownKey, oldID)
	}
	recipient, err := s.externalRecipient(ctx, newRef)
	if err != nil {
		return KeyRecord{}, err
	}
	rec := KeyRecord{ID: externalKeyID(recipient), Provider: ProviderExternal, Recipient: recipient, Created: now.UTC(), Status: StatusActive}
	if rec.ID == oldID {
		return KeyRecord{}, fmt.Errorf("%w: %s (the new reference yields the key already registered)", ErrDuplicateKey, oldID)
	}
	if err := s.registry.AddKey(ctx, rec, oldID); err != nil {
		return KeyRecord{}, err
	}
	return rec, nil
}

// withExternalRecord hands fn the material of external key rec through the
// first declared reference that yields a key deriving rec's recipient. fn is
// called at most once, only for a matching key, and never when none matches.
func (s *Store) withExternalRecord(ctx context.Context, rec KeyRecord, fn func(key []byte) error) error {
	var refs []secretref.Ref
	if s.externals != nil {
		refs = s.externals()
	}
	var tried []string
	for _, ref := range refs {
		if ref.IsZero() {
			continue
		}
		matched := false
		var fnErr error
		err := WithExternalKey(ctx, ref, func(key []byte) error {
			recipient, err := s.format.Recipient(key)
			if err != nil || recipient != rec.Recipient {
				return errExternalNoMatch
			}
			matched = true
			fnErr = fn(key)
			return fnErr
		})
		switch {
		case matched:
			return err
		case errors.Is(err, errExternalNoMatch):
			tried = append(tried, ref.String()+": yields a different key")
		default:
			tried = append(tried, ref.String()+": "+err.Error())
		}
	}
	if len(tried) == 0 {
		return fmt.Errorf("%w: %s (no external reference is declared)", ErrExternalKeyUnresolvable, rec.ID)
	}
	return fmt.Errorf("%w: %s (%s)", ErrExternalKeyUnresolvable, rec.ID, strings.Join(tried, "; "))
}

var errExternalNoMatch = errors.New("keycustody: reference yields a different key")
