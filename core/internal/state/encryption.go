package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/retnd/retnd/core/internal/model"
)

// The encryption records migration 0015 adds (EPIC N, N1.1, #989): the key
// registry, the per-copy record with its stored-bytes digest, and the
// durable bundle exports and confirmations. The 0015 header says what each table is for;
// this file is the typed way in and the rules the writes enforce.
//
// # Journal truth
//
// A per-copy record is keyed by a placements row that already exists and
// names a key that is already registered. A listing of a destination is
// evidence about a row, never a source of one, and there is no function
// here that would let an object found at a key become a record of an
// encrypted copy.
//
// # Two digests, never one
//
// artifacts.local_hash (and placements.hash, which copies it) is the
// artifact's plaintext digest. PlacementEncryption.StoredHash is the digest
// of the ciphertext at the key. RecordPlacementEncryption refuses a write
// that hands the first over as the second, because two numbers that
// disagree about the same object are not a measurement (#662), and a
// record that says the ciphertext hashes to the plaintext is a record that
// will never verify.
//
// # No key material
//
// Nothing in these types is secret and nothing here can hold a secret.
// EncryptionKey.Recipient is the public half. The private half is a file
// under internal/secretref's custody rule, or outside this product.

// Key providers, the Go copy of the CHECK on encryption_keys.provider.
// They are config's KeyProviderManaged / KeyProviderExternal by value, so
// the two vocabularies cannot drift without a test noticing.
const (
	KeyProviderManaged  = "managed"
	KeyProviderExternal = "external"
)

// KeyProviders is the provider vocabulary in a fixed order.
var KeyProviders = []string{KeyProviderManaged, KeyProviderExternal}

// Key statuses, the Go copy of the CHECK on encryption_keys.status.
const (
	// EncryptionKeyActive means new copies may be written under the key.
	EncryptionKeyActive = "active"
	// EncryptionKeySuperseded means a rotation moved the key out of the
	// active slot. Copies recorded against it still decrypt through it.
	EncryptionKeySuperseded = "superseded"
	// EncryptionKeyRetired means historical copies still decrypt through
	// the key and new copies are not written under it.
	EncryptionKeyRetired = "retired"
)

// EncryptionKeyStatuses is the status vocabulary in a fixed order.
var EncryptionKeyStatuses = []string{EncryptionKeyActive, EncryptionKeySuperseded, EncryptionKeyRetired}

var (
	// ErrEncryptionKeyNotFound is returned when a record names a key id
	// the registry does not hold.
	ErrEncryptionKeyNotFound = errors.New("state: encryption key not found")

	// ErrEncryptionKeyExists is returned by RegisterEncryptionKey for an
	// id already in the registry. A key is never re-registered: the id is
	// what every copy made under it records.
	ErrEncryptionKeyExists = errors.New("state: encryption key already registered")

	// ErrRecoveryBundleExportNotFound is returned when a confirmation names
	// a bundle digest no export of that key recorded.
	ErrRecoveryBundleExportNotFound = errors.New("state: no recovery bundle export with that digest")

	// ErrStoredDigestIsPlaintext is FR-61's refusal: the stored-bytes
	// digest handed in equals the artifact's plaintext digest, so it
	// describes the plaintext and not the bytes at the key.
	ErrStoredDigestIsPlaintext = errors.New("state: the stored-bytes digest equals the artifact's plaintext digest; a ciphertext copy does not hash to its plaintext")
)

// EncryptionKey is one row of the key registry. It carries no material.
type EncryptionKey struct {
	// ID is the product's stable identifier for the key.
	ID string

	// Provider is KeyProviderManaged or KeyProviderExternal.
	Provider string

	// Recipient is the public half, as the format spells it. Empty for an
	// external key the product has not resolved.
	Recipient string

	// Status is EncryptionKeyActive, EncryptionKeySuperseded or
	// EncryptionKeyRetired. Empty on a
	// key being registered means active.
	Status string

	CreatedAt time.Time
	RetiredAt *time.Time
}

// RegisterEncryptionKey adds a key to the registry. It refuses an empty id,
// an unknown provider, a zero creation time, a status other than active or
// retired, a retired status without a time (and the reverse), and an id
// already registered.
func (j *Journal) RegisterEncryptionKey(ctx context.Context, k EncryptionKey) error {
	return j.AddEncryptionKey(ctx, k, "")
}

// AddEncryptionKey registers k and, when supersede names a key, moves that
// key to EncryptionKeySuperseded in the same transaction, so a crash cannot
// leave two active managed keys or none. An unknown supersede id is
// ErrEncryptionKeyNotFound and rolls the new key back.
func (j *Journal) AddEncryptionKey(ctx context.Context, k EncryptionKey, supersede string) error {
	if k.ID == "" {
		return fmt.Errorf("state: register encryption key: id must not be empty")
	}
	if !containsString(KeyProviders, k.Provider) {
		return fmt.Errorf("state: register encryption key %s: provider %q is not one of %v", k.ID, k.Provider, KeyProviders)
	}
	if k.CreatedAt.IsZero() {
		return fmt.Errorf("state: register encryption key %s: created_at must be set", k.ID)
	}
	status := k.Status
	if status == "" {
		status = EncryptionKeyActive
	}
	if !containsString(EncryptionKeyStatuses, status) {
		return fmt.Errorf("state: register encryption key %s: status %q is not one of %v", k.ID, status, EncryptionKeyStatuses)
	}
	if (status == EncryptionKeyRetired) != (k.RetiredAt != nil) {
		return fmt.Errorf("state: register encryption key %s: status %s and retired_at disagree", k.ID, status)
	}

	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: register encryption key %s: %w", k.ID, err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed

	if supersede != "" {
		// Superseded first, so the one-active-managed-key index never sees
		// two active rows inside the transaction.
		res, err := tx.ExecContext(ctx, `UPDATE encryption_keys SET status = ? WHERE id = ? AND status = ?`,
			EncryptionKeySuperseded, supersede, EncryptionKeyActive)
		if err != nil {
			return fmt.Errorf("state: supersede encryption key %s: %w", supersede, err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return fmt.Errorf("%w: %s (no active key with that id to supersede)", ErrEncryptionKeyNotFound, supersede)
		}
	}

	// DO NOTHING plus a rows-affected check, rather than reading the
	// driver's constraint code: a TEXT primary key reports a different
	// code from a UNIQUE index, and the question here is only "was a row
	// written".
	res, err := tx.ExecContext(ctx, `
		INSERT INTO encryption_keys (id, provider, recipient, status, created_at, retired_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		k.ID, k.Provider, k.Recipient, status, formatTime(k.CreatedAt), formatTimePtr(k.RetiredAt))
	if err != nil {
		return fmt.Errorf("state: register encryption key %s: %w", k.ID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", ErrEncryptionKeyExists, k.ID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("state: register encryption key %s: %w", k.ID, err)
	}
	return nil
}

const encryptionKeyColumns = `id, provider, recipient, status, created_at, retired_at`

func scanEncryptionKey(row scanRow) (EncryptionKey, error) {
	var (
		k         EncryptionKey
		createdAt string
		retiredAt sql.NullString
	)
	if err := row.Scan(&k.ID, &k.Provider, &k.Recipient, &k.Status, &createdAt, &retiredAt); err != nil {
		return EncryptionKey{}, err
	}
	created, err := parseTime(createdAt)
	if err != nil {
		return EncryptionKey{}, fmt.Errorf("state: stored encryption key %s created_at %q is invalid: %w", k.ID, createdAt, err)
	}
	k.CreatedAt = created
	retired, err := parseTimePtr(retiredAt)
	if err != nil {
		return EncryptionKey{}, fmt.Errorf("state: stored encryption key %s retired_at is invalid: %w", k.ID, err)
	}
	k.RetiredAt = retired
	return k, nil
}

// EncryptionKeys lists every registered key, oldest first.
func (j *Journal) EncryptionKeys(ctx context.Context) ([]EncryptionKey, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT `+encryptionKeyColumns+` FROM encryption_keys ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("state: list encryption keys: %w", err)
	}
	defer rows.Close()
	var out []EncryptionKey
	for rows.Next() {
		k, err := scanEncryptionKey(rows)
		if err != nil {
			return nil, fmt.Errorf("state: list encryption keys: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: list encryption keys: %w", err)
	}
	return out, nil
}

// EncryptionKey returns one registered key, or ErrEncryptionKeyNotFound.
func (j *Journal) EncryptionKey(ctx context.Context, id string) (EncryptionKey, error) {
	k, err := scanEncryptionKey(j.db.QueryRowContext(ctx, `SELECT `+encryptionKeyColumns+` FROM encryption_keys WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return EncryptionKey{}, fmt.Errorf("%w: %s", ErrEncryptionKeyNotFound, id)
	}
	if err != nil {
		return EncryptionKey{}, fmt.Errorf("state: read encryption key %s: %w", id, err)
	}
	return k, nil
}

// PlacementEncryption is the per-copy record: how one encrypted copy was
// written and what its stored bytes hash to. A copy with no record is
// plaintext.
type PlacementEncryption struct {
	// FormatID and FormatVersion name the on-the-wire format (ADR 0027).
	FormatID      string
	FormatVersion int

	// KeyID is the registry id of the key the copy was written under.
	KeyID string

	// StoredHash and StoredHashAlg are the digest of the bytes at the
	// key, header included: the ciphertext, never the plaintext.
	StoredHash    string
	StoredHashAlg string

	// StoredSize is the byte count at the key.
	StoredSize int64
}

func (e PlacementEncryption) validate() error {
	switch {
	case e.FormatID == "":
		return fmt.Errorf("state: record placement encryption: format id must not be empty")
	case e.FormatVersion < 1:
		return fmt.Errorf("state: record placement encryption: format version %d must be at least 1", e.FormatVersion)
	case e.KeyID == "":
		return fmt.Errorf("state: record placement encryption: key id must not be empty")
	case e.StoredHash == "" || e.StoredHashAlg == "":
		return fmt.Errorf("state: record placement encryption: the stored-bytes digest and its algorithm must both be set")
	case e.StoredSize < 0:
		return fmt.Errorf("state: record placement encryption: stored size %d must not be negative", e.StoredSize)
	}
	return nil
}

// RecordPlacementEncryption writes the encryption record for one copy, in
// its own transaction. The copy must be an ACTIVE placement the journal
// knows and the key must be registered.
//
// Two refusals are the point of this function rather than bookkeeping. A
// StoredHash equal to the artifact's plaintext digest, or to the copy's own
// placements.hash, is ErrStoredDigestIsPlaintext (FR-61). A StoredSize no
// larger than the copy's recorded plaintext size is refused too: every
// authenticated format carries overhead, so a size that does not exceed the
// plaintext describes something other than ciphertext.
//
// #991 writes this record inside the same transaction as the phase advance
// that makes the copy durable; recordPlacementEncryptionTx is the half it
// shares with this entry point.
func (j *Journal) RecordPlacementEncryption(ctx context.Context, artifact model.ArtifactID, medium string, e PlacementEncryption) error {
	if err := e.validate(); err != nil {
		return err
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: record encryption of %s on %s: %w", artifact, medium, err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed

	if err := recordPlacementEncryptionTx(ctx, tx, artifact, medium, e); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("state: record encryption of %s on %s: %w", artifact, medium, err)
	}
	return nil
}

func recordPlacementEncryptionTx(ctx context.Context, tx *sql.Tx, artifact model.ArtifactID, medium string, e PlacementEncryption) error {
	var (
		id                       int64
		status, localHash, pHash string
		size                     sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `
		SELECT p.id, p.status, a.local_hash, p.hash, p.size_bytes
		  FROM placements p JOIN artifacts a ON a.id = p.artifact_id
		 WHERE a.source = ? AND a.backup_set = ? AND a.artifact_name = ? AND p.medium = ?`,
		artifact.Set.Source, artifact.Set.Set, artifact.Name, medium,
	).Scan(&id, &status, &localHash, &pHash, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s has no copy on %q", ErrPlacementNotFound, artifact, medium)
	}
	if err != nil {
		return fmt.Errorf("state: record encryption of %s on %s: %w", artifact, medium, err)
	}
	if status != PlacementActive {
		return fmt.Errorf("%w: %s on %q is %s", ErrPlacementRetired, artifact, medium, status)
	}
	if (localHash != "" && e.StoredHash == localHash) || (pHash != "" && e.StoredHash == pHash) {
		return fmt.Errorf("%w: %s on %q", ErrStoredDigestIsPlaintext, artifact, medium)
	}
	if size.Valid && e.StoredSize <= size.Int64 {
		return fmt.Errorf("state: record encryption of %s on %s: stored size %d does not exceed the plaintext size %d, so it does not describe ciphertext",
			artifact, medium, e.StoredSize, size.Int64)
	}

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM encryption_keys WHERE id = ?`, e.KeyID).Scan(&n); err != nil {
		return fmt.Errorf("state: record encryption of %s on %s: %w", artifact, medium, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrEncryptionKeyNotFound, e.KeyID)
	}

	// A re-copy is a fresh encryption, so a record already there describes
	// bytes that are gone; the new one replaces it in full.
	_, err = tx.ExecContext(ctx, `
		INSERT INTO placement_encryption (placement_id, format_id, format_version, key_id, stored_hash, stored_hash_alg, stored_size)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (placement_id) DO UPDATE SET
		    format_id = excluded.format_id, format_version = excluded.format_version, key_id = excluded.key_id,
		    stored_hash = excluded.stored_hash, stored_hash_alg = excluded.stored_hash_alg, stored_size = excluded.stored_size`,
		id, e.FormatID, e.FormatVersion, e.KeyID, e.StoredHash, e.StoredHashAlg, e.StoredSize)
	if err != nil {
		return fmt.Errorf("state: record encryption of %s on %s: %w", artifact, medium, err)
	}
	return nil
}

// PlacementEncryption reads one copy's record. ok is false for a plaintext
// copy and for a copy the journal does not know; neither is an error,
// because "not encrypted" is the honest answer for both.
func (j *Journal) PlacementEncryption(ctx context.Context, artifact model.ArtifactID, medium string) (e PlacementEncryption, ok bool, err error) {
	err = j.db.QueryRowContext(ctx, `
		SELECT pe.format_id, pe.format_version, pe.key_id, pe.stored_hash, pe.stored_hash_alg, pe.stored_size
		  FROM placement_encryption pe
		  JOIN placements p ON p.id = pe.placement_id
		  JOIN artifacts a ON a.id = p.artifact_id
		 WHERE a.source = ? AND a.backup_set = ? AND a.artifact_name = ? AND p.medium = ?`,
		artifact.Set.Source, artifact.Set.Set, artifact.Name, medium,
	).Scan(&e.FormatID, &e.FormatVersion, &e.KeyID, &e.StoredHash, &e.StoredHashAlg, &e.StoredSize)
	if errors.Is(err, sql.ErrNoRows) {
		return PlacementEncryption{}, false, nil
	}
	if err != nil {
		return PlacementEncryption{}, false, fmt.Errorf("state: read encryption of %s on %s: %w", artifact, medium, err)
	}
	return e, true, nil
}

// RecoveryBundleExport is the durable fact that a recovery bundle for a key
// was written, identified by the digest the bundle carries. It holds no
// key material.
type RecoveryBundleExport struct {
	KeyID      string
	Digest     string
	ExportedAt time.Time
}

// RecordRecoveryBundleExport records an export for a registered key. The
// same digest is recorded once per key; a repeat is an error.
func (j *Journal) RecordRecoveryBundleExport(ctx context.Context, e RecoveryBundleExport) error {
	switch {
	case e.KeyID == "":
		return fmt.Errorf("state: record recovery bundle export: key id must not be empty")
	case e.Digest == "":
		return fmt.Errorf("state: record recovery bundle export for %s: digest must not be empty", e.KeyID)
	case e.ExportedAt.IsZero():
		return fmt.Errorf("state: record recovery bundle export for %s: exported_at must be set", e.KeyID)
	}
	if _, err := j.EncryptionKey(ctx, e.KeyID); err != nil {
		return err
	}
	if _, err := j.db.ExecContext(ctx, `
		INSERT INTO recovery_bundle_exports (key_id, digest, exported_at) VALUES (?, ?, ?)`,
		e.KeyID, e.Digest, formatTime(e.ExportedAt)); err != nil {
		return fmt.Errorf("state: record recovery bundle export for %s: %w", e.KeyID, err)
	}
	return nil
}

// RecoveryBundleExports lists the exports recorded for a key, oldest first.
func (j *Journal) RecoveryBundleExports(ctx context.Context, keyID string) ([]RecoveryBundleExport, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT key_id, digest, exported_at FROM recovery_bundle_exports
		 WHERE key_id = ? ORDER BY exported_at, digest`, keyID)
	if err != nil {
		return nil, fmt.Errorf("state: list recovery bundle exports for %s: %w", keyID, err)
	}
	defer rows.Close()
	var out []RecoveryBundleExport
	for rows.Next() {
		var (
			e  RecoveryBundleExport
			at string
		)
		if err := rows.Scan(&e.KeyID, &e.Digest, &at); err != nil {
			return nil, fmt.Errorf("state: list recovery bundle exports for %s: %w", keyID, err)
		}
		if e.ExportedAt, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("state: stored bundle export for %s exported_at %q is invalid: %w", keyID, at, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: list recovery bundle exports for %s: %w", keyID, err)
	}
	return out, nil
}

// RecoveryBundleConfirmation is the durable fact that an operator has
// confirmed holding the recovery bundle with BundleDigest for a key (FR-66).
// A managed key with no confirmation is the degraded state health renders.
type RecoveryBundleConfirmation struct {
	KeyID        string
	ConfirmedAt  time.Time
	ConfirmedBy  string
	BundleDigest string
}

// ConfirmRecoveryBundle records a confirmation for a registered key. The
// digest must match an export recorded for the key, else
// ErrRecoveryBundleExportNotFound. Confirming the same bundle again
// replaces the earlier statement, so there is one row per key and digest.
func (j *Journal) ConfirmRecoveryBundle(ctx context.Context, c RecoveryBundleConfirmation) error {
	if c.KeyID == "" {
		return fmt.Errorf("state: confirm recovery bundle: key id must not be empty")
	}
	if c.ConfirmedAt.IsZero() {
		return fmt.Errorf("state: confirm recovery bundle for %s: confirmed_at must be set", c.KeyID)
	}
	if _, err := j.EncryptionKey(ctx, c.KeyID); err != nil {
		return err
	}
	var n int
	if err := j.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_bundle_exports WHERE key_id = ? AND digest = ?`,
		c.KeyID, c.BundleDigest).Scan(&n); err != nil {
		return fmt.Errorf("state: confirm recovery bundle for %s: %w", c.KeyID, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: key %s", ErrRecoveryBundleExportNotFound, c.KeyID)
	}
	_, err := j.db.ExecContext(ctx, `
		INSERT INTO recovery_bundle_confirmations (key_id, bundle_digest, confirmed_at, confirmed_by)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (key_id, bundle_digest) DO UPDATE SET
		    confirmed_at = excluded.confirmed_at, confirmed_by = excluded.confirmed_by`,
		c.KeyID, c.BundleDigest, formatTime(c.ConfirmedAt), c.ConfirmedBy)
	if err != nil {
		return fmt.Errorf("state: confirm recovery bundle for %s: %w", c.KeyID, err)
	}
	return nil
}

// RecoveryBundleConfirmations lists the confirmations recorded for a key,
// oldest first. An empty list is the degraded state, not an error.
func (j *Journal) RecoveryBundleConfirmations(ctx context.Context, keyID string) ([]RecoveryBundleConfirmation, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT key_id, bundle_digest, confirmed_at, confirmed_by
		  FROM recovery_bundle_confirmations WHERE key_id = ? ORDER BY confirmed_at, bundle_digest`, keyID)
	if err != nil {
		return nil, fmt.Errorf("state: list recovery bundle confirmations for %s: %w", keyID, err)
	}
	defer rows.Close()
	var out []RecoveryBundleConfirmation
	for rows.Next() {
		var (
			c  RecoveryBundleConfirmation
			at string
		)
		if err := rows.Scan(&c.KeyID, &c.BundleDigest, &at, &c.ConfirmedBy); err != nil {
			return nil, fmt.Errorf("state: list recovery bundle confirmations for %s: %w", keyID, err)
		}
		if c.ConfirmedAt, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("state: stored bundle confirmation for %s confirmed_at %q is invalid: %w", keyID, at, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: list recovery bundle confirmations for %s: %w", keyID, err)
	}
	return out, nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
