package placement

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// Retiring a managed encryption key (EPIC N, #992, FR-65).
//
// A rotation (keycustody.Store.Rotate) changes the key NEW copies are written
// under and nothing else: the old key becomes superseded and every copy
// recorded against it keeps decrypting through it. Retiring is the separate,
// deliberate act of taking a key out of service for good, and it is refused
// while any ACTIVE placement still records the key, because that copy could
// not be opened again. The refusal names the copies, not their number, since
// a count cannot be acted on and a list can.
//
// What counts as still needing a key: an ACTIVE placement. A DELETE_PENDING
// or GONE copy does not, because deleting an encrypted object needs no key at
// all (Reclaimer asks the guard ladder's existence and attested rungs and
// never content). The one exception someone will eventually want, retiring a
// key whose copies remain but are meant to stay unreadable ("external
// recovery only"), is an explicitly designed and audited policy that this
// epic does not define, and nothing here approximates it.
//
// The journal side is two methods the journal owner adds to state.Journal;
// KeyJournal is the narrow view this file needs and is what a test
// substitutes:
//
//	ActivePlacementsRecordingKey(ctx, keyID) ([]model.CopyRef, error)
//	RetireEncryptionKey(ctx, keyID, at) error  // one transaction: re-checks, then retires
//
// RetireEncryptionKey re-checks inside its own transaction because a re-home
// can record a new copy under a superseded key between this file's read and
// its write; RetireKey turns that refusal back into the named one.

// KeyJournal is what RetireKey needs of the journal.
type KeyJournal interface {
	EncryptionKey(ctx context.Context, id string) (state.EncryptionKey, error)
	ActivePlacementsRecordingKey(ctx context.Context, keyID string) ([]model.CopyRef, error)
	RetireEncryptionKey(ctx context.Context, keyID string, at time.Time) error
}

var (
	// ErrKeyInUse is a refusal to retire a key an ACTIVE placement records.
	// The concrete error is *KeyInUseError and carries the copies.
	ErrKeyInUse = errors.New("placement: the key is still recorded by an ACTIVE copy")

	// ErrKeyStillActive is a refusal to retire the key new copies are
	// written under. Rotate first.
	ErrKeyStillActive = errors.New("placement: the key is the active one; rotate to a new key before retiring it")
)

// KeyInUseError is ErrKeyInUse with the copies that stand in the way.
type KeyInUseError struct {
	KeyID  string
	Copies []model.CopyRef
}

// maxNamedCopies is how many copies the message lists before cutting. The
// error value always carries every one.
const maxNamedCopies = 20

func (e *KeyInUseError) Error() string {
	names := make([]string, 0, maxNamedCopies)
	for i, c := range e.Copies {
		if i == maxNamedCopies {
			break
		}
		names = append(names, c.String())
	}
	msg := fmt.Sprintf("%v: refusing to retire key %s, %d ACTIVE copies still record it: %s",
		ErrKeyInUse, e.KeyID, len(e.Copies), strings.Join(names, ", "))
	if more := len(e.Copies) - len(names); more > 0 {
		msg += fmt.Sprintf(", and %d more", more)
	}
	return msg
}

// Is makes errors.Is(err, ErrKeyInUse) true.
func (e *KeyInUseError) Is(target error) bool { return target == ErrKeyInUse }

// RetireKey retires keyID at now, or refuses and changes nothing. A nil log
// is fine. Retiring a key that is already retired is not an error and writes
// nothing.
func RetireKey(ctx context.Context, j KeyJournal, log *obs.Logger, keyID string, now time.Time) (err error) {
	defer func() {
		if err != nil {
			log.KeyRetired(ctx, keyID, obs.ResultError)
		}
	}()

	key, err := j.EncryptionKey(ctx, keyID)
	if errors.Is(err, state.ErrEncryptionKeyNotFound) {
		return fmt.Errorf("%w: %s", keycustody.ErrUnknownKey, keyID)
	}
	if err != nil {
		return fmt.Errorf("placement: reading key %s: %w", keyID, err)
	}
	switch key.Status {
	case state.EncryptionKeyRetired:
		return nil
	case state.EncryptionKeyActive:
		return fmt.Errorf("%w: %s", ErrKeyStillActive, keyID)
	}

	copies, err := j.ActivePlacementsRecordingKey(ctx, keyID)
	if err != nil {
		return fmt.Errorf("placement: listing the copies that record key %s: %w", keyID, err)
	}
	if len(copies) > 0 {
		return &KeyInUseError{KeyID: keyID, Copies: copies}
	}

	if err := j.RetireEncryptionKey(ctx, keyID, now); err != nil {
		// The journal re-checks in its own transaction. If it refused because
		// a copy appeared since the read above, say which, not "refused".
		if late, lerr := j.ActivePlacementsRecordingKey(ctx, keyID); lerr == nil && len(late) > 0 {
			return &KeyInUseError{KeyID: keyID, Copies: late}
		}
		return fmt.Errorf("placement: retiring key %s: %w", keyID, err)
	}
	log.KeyRetired(ctx, keyID, obs.ResultSuccess)
	return nil
}
