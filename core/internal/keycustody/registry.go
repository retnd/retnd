package keycustody

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/retnd/retnd/core/internal/state"
)

// Status is where a managed key is in its life. The vocabulary is enforced
// here on the write path rather than by a schema constraint, for the reason
// core/migrations/0012 and 0013 give: widening a CHECK in this journal is a
// table rebuild, widening a Go constant is a line and a test.
type Status string

const (
	// StatusActive is the one key new copies are encrypted under. There is
	// at most one at a time; Rotate is the only thing that changes which.
	StatusActive Status = "active"

	// StatusSuperseded is a key a rotation moved out of the active slot.
	// Every copy recorded against it still decrypts through it, so it is
	// held under the same custody as the active key and probes the same way.
	StatusSuperseded Status = "superseded"

	// StatusRetired is a key no ACTIVE placement records any more. Nothing
	// in this package produces it: the refusal that stands between
	// superseded and retired (a placement still naming the key) is #992's.
	StatusRetired Status = "retired"
)

// Provider kinds a KeyRecord can carry. They are migration 0015's provider
// column by value (state.KeyProviderManaged / KeyProviderExternal).
const (
	ProviderManaged  = "managed"
	ProviderExternal = "external"
)

// KeyRecord is one key's identity and history, and never its
// material. It is safe to render anywhere.
type KeyRecord struct {
	// ID is stable for the life of the key and derived from nothing about
	// the process that minted it, so a restart, a reinstall over the same
	// state directory and a catalog rebuild all name the same key.
	ID string `json:"id"`

	// Provider is ProviderManaged for a key this product minted and holds, or
	// ProviderExternal for an operator's own reference (#1292). Empty on a
	// record handed to AddKey means managed. Never rendered: a surface that
	// wants it says so deliberately.
	Provider string `json:"-"`

	// Recipient is the key's public half (for age, "age1..."). It is not
	// secret, it is what migration 0015's encryption_keys.recipient holds,
	// and it is how a key file is checked against its record.
	Recipient string `json:"recipient"`

	// Created is when the key was minted.
	Created time.Time `json:"created"`

	// Retired is when the key left service, nil while it has not.
	Retired *time.Time `json:"retired,omitempty"`

	// Status is the key's current Status.
	Status Status `json:"status"`
}

// ExportRecord is the durable fact that a bundle for KeyID was written,
// identified by the digest the bundle carries. It holds no material.
type ExportRecord struct {
	KeyID      string    `json:"key_id"`
	Digest     string    `json:"digest"`
	ExportedAt time.Time `json:"exported_at"`
}

// Confirmation is the durable fact that a person confirmed holding the
// bundle with Digest for KeyID. It is what clears the degraded state, and
// it is accepted only against an ExportRecord with the same digest, so
// confirming is a proof of possession rather than a checkbox.
type Confirmation struct {
	KeyID       string    `json:"key_id"`
	Digest      string    `json:"digest"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

// Registry is the durable record of keys, exports and confirmations. Every
// method is one durable write or one read; there is no in-memory state a
// restart could lose, which is what the restart tests in this package
// prove against JournalRegistry.
//
// JournalRegistry is the implementation, over migration 0015's tables.
type Registry interface {
	// Keys returns every key, oldest first.
	Keys(ctx context.Context) ([]KeyRecord, error)

	// AddKey records rec and, when supersede names a key, moves that key to
	// StatusSuperseded in the SAME durable write, so a crash between the
	// two cannot leave two active keys or none.
	AddKey(ctx context.Context, rec KeyRecord, supersede string) error

	// Exports returns every export recorded for keyID.
	Exports(ctx context.Context, keyID string) ([]ExportRecord, error)

	// RecordExport records that a bundle was written.
	RecordExport(ctx context.Context, rec ExportRecord) error

	// Confirmations returns every confirmation ever recorded.
	Confirmations(ctx context.Context) ([]Confirmation, error)

	// Confirm records a confirmation. The caller has already matched its
	// digest to an export; the registry stores what it is given.
	Confirm(ctx context.Context, c Confirmation) error
}

// ErrDuplicateKey is returned when AddKey is handed an id the registry
// already holds. Ids are random, so reaching this means two stores are
// writing one registry, which is a deployment error worth refusing loudly.
var ErrDuplicateKey = errors.New("keycustody: a key with this id is already registered")

// JournalRegistry is the Registry over migration 0015's encryption_keys,
// recovery_bundle_exports and recovery_bundle_confirmations tables. It holds
// no state of its own: every call is one journal call, so a second process
// over the same database sees the same keys, which is the restart property.
// Rows carry ids, recipients, digests and times and nothing else.
type JournalRegistry struct {
	j *state.Journal
}

// NewJournalRegistry wraps an open journal. The caller owns the journal's
// lifetime.
func NewJournalRegistry(j *state.Journal) *JournalRegistry {
	return &JournalRegistry{j: j}
}

// Keys implements Registry. Every managed key keeps its row for ever, so
// this is the history, oldest first.
func (r *JournalRegistry) Keys(ctx context.Context) ([]KeyRecord, error) {
	rows, err := r.j.EncryptionKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("keycustody: reading key registry: %w", err)
	}
	out := make([]KeyRecord, 0, len(rows))
	for _, k := range rows {
		out = append(out, KeyRecord{ID: k.ID, Provider: k.Provider, Recipient: k.Recipient, Created: k.CreatedAt, Retired: k.RetiredAt, Status: Status(k.Status)})
	}
	return out, nil
}

// AddKey implements Registry. The supersede and the insert are one
// transaction in the journal.
func (r *JournalRegistry) AddKey(ctx context.Context, rec KeyRecord, supersede string) error {
	provider := rec.Provider
	if provider == "" {
		provider = state.KeyProviderManaged
	}
	err := r.j.AddEncryptionKey(ctx, state.EncryptionKey{
		ID: rec.ID, Provider: provider, Recipient: rec.Recipient,
		Status: string(rec.Status), CreatedAt: rec.Created, RetiredAt: rec.Retired,
	}, supersede)
	switch {
	case errors.Is(err, state.ErrEncryptionKeyExists):
		return fmt.Errorf("%w: %s", ErrDuplicateKey, rec.ID)
	case errors.Is(err, state.ErrEncryptionKeyNotFound):
		return fmt.Errorf("%w: %s", ErrUnknownKey, supersede)
	}
	return err
}

// Exports implements Registry.
func (r *JournalRegistry) Exports(ctx context.Context, keyID string) ([]ExportRecord, error) {
	rows, err := r.j.RecoveryBundleExports(ctx, keyID)
	if err != nil {
		return nil, fmt.Errorf("keycustody: reading bundle exports: %w", err)
	}
	var out []ExportRecord
	for _, e := range rows {
		out = append(out, ExportRecord{KeyID: e.KeyID, Digest: e.Digest, ExportedAt: e.ExportedAt})
	}
	return out, nil
}

// RecordExport implements Registry.
func (r *JournalRegistry) RecordExport(ctx context.Context, rec ExportRecord) error {
	return r.j.RecordRecoveryBundleExport(ctx, state.RecoveryBundleExport{KeyID: rec.KeyID, Digest: rec.Digest, ExportedAt: rec.ExportedAt})
}

// Confirmations implements Registry. The journal reads per key, so this
// walks the registry's keys.
func (r *JournalRegistry) Confirmations(ctx context.Context) ([]Confirmation, error) {
	keys, err := r.j.EncryptionKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("keycustody: reading key registry: %w", err)
	}
	var out []Confirmation
	for _, k := range keys {
		rows, err := r.j.RecoveryBundleConfirmations(ctx, k.ID)
		if err != nil {
			return nil, fmt.Errorf("keycustody: reading bundle confirmations: %w", err)
		}
		for _, c := range rows {
			out = append(out, Confirmation{KeyID: c.KeyID, Digest: c.BundleDigest, ConfirmedAt: c.ConfirmedAt})
		}
	}
	return out, nil
}

// Confirm implements Registry. The journal refuses a digest no recorded
// export carries, and that refusal is ErrNoSuchExport here.
func (r *JournalRegistry) Confirm(ctx context.Context, c Confirmation) error {
	err := r.j.ConfirmRecoveryBundle(ctx, state.RecoveryBundleConfirmation{KeyID: c.KeyID, BundleDigest: c.Digest, ConfirmedAt: c.ConfirmedAt})
	if errors.Is(err, state.ErrRecoveryBundleExportNotFound) {
		return fmt.Errorf("%w: key %s", ErrNoSuchExport, c.KeyID)
	}
	return err
}
