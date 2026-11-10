package keycustody

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
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

// KeyRecord is one managed key's identity and history, and never its
// material. It is safe to render anywhere.
type KeyRecord struct {
	// ID is stable for the life of the key and derived from nothing about
	// the process that minted it, so a restart, a reinstall over the same
	// state directory and a catalog rebuild all name the same key.
	ID string `json:"id"`

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
// prove against FileRegistry.
//
// FileRegistry is the durable implementation today. Migration 0015's
// encryption_keys has no 'superseded' status and no table for exports, so a
// journal-backed implementation needs a schema addition first; see doc.go.
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

// FileRegistry is a Registry over one JSON file, rewritten whole and
// atomically on every mutation. It is the durable implementation until the
// journal-backed one exists (migration 0015 is in the tree but cannot hold
// a superseded key or an export record yet), and it is small enough that
// reading it on every call is the right trade: there is no cache to
// invalidate and nothing a crash can leave half-applied.
type FileRegistry struct {
	path string
	mu   sync.Mutex
}

// registryFile is the on-disk shape. The version field is what a later
// shape change keys on.
type registryFile struct {
	Version       int            `json:"version"`
	Keys          []KeyRecord    `json:"keys"`
	Exports       []ExportRecord `json:"exports"`
	Confirmations []Confirmation `json:"confirmations"`
}

const registryFileVersion = 1

// OpenFileRegistry opens, or creates empty, the registry file at path. The
// directory containing it must already exist; the file is written 0600.
func OpenFileRegistry(path string) (*FileRegistry, error) {
	r := &FileRegistry{path: path}
	if _, err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *FileRegistry) load() (registryFile, error) {
	var f registryFile
	b, err := os.ReadFile(r.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		f.Version = registryFileVersion
		return f, nil
	case err != nil:
		return f, fmt.Errorf("keycustody: reading registry %s: %w", r.path, err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("keycustody: registry %s is not readable: %w", r.path, err)
	}
	if f.Version != registryFileVersion {
		return f, fmt.Errorf("keycustody: registry %s is version %d and this build reads version %d", r.path, f.Version, registryFileVersion)
	}
	return f, nil
}

// save writes the whole file through a temporary name and a rename, fsyncing
// both the file and its directory, so a reader never sees a torn registry.
func (r *FileRegistry) save(f registryFile) error {
	f.Version = registryFileVersion
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("keycustody: encoding registry: %w", err)
	}
	tmp := r.path + ".partial"
	if err := writeFileSync(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("keycustody: writing registry: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("keycustody: replacing registry: %w", err)
	}
	return fsyncDir(filepath.Dir(r.path))
}

// Keys implements Registry.
func (r *FileRegistry) Keys(_ context.Context) ([]KeyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := r.load()
	if err != nil {
		return nil, err
	}
	return append([]KeyRecord(nil), f.Keys...), nil
}

// AddKey implements Registry.
func (r *FileRegistry) AddKey(_ context.Context, rec KeyRecord, supersede string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := r.load()
	if err != nil {
		return err
	}
	for i := range f.Keys {
		if f.Keys[i].ID == rec.ID {
			return fmt.Errorf("%w: %s", ErrDuplicateKey, rec.ID)
		}
		if supersede != "" && f.Keys[i].ID == supersede {
			f.Keys[i].Status = StatusSuperseded
		}
	}
	f.Keys = append(f.Keys, rec)
	return r.save(f)
}

// Exports implements Registry.
func (r *FileRegistry) Exports(_ context.Context, keyID string) ([]ExportRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := r.load()
	if err != nil {
		return nil, err
	}
	var out []ExportRecord
	for _, e := range f.Exports {
		if e.KeyID == keyID {
			out = append(out, e)
		}
	}
	return out, nil
}

// RecordExport implements Registry.
func (r *FileRegistry) RecordExport(_ context.Context, rec ExportRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := r.load()
	if err != nil {
		return err
	}
	f.Exports = append(f.Exports, rec)
	return r.save(f)
}

// Confirmations implements Registry.
func (r *FileRegistry) Confirmations(_ context.Context) ([]Confirmation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := r.load()
	if err != nil {
		return nil, err
	}
	return append([]Confirmation(nil), f.Confirmations...), nil
}

// Confirm implements Registry.
func (r *FileRegistry) Confirm(_ context.Context, c Confirmation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := r.load()
	if err != nil {
		return err
	}
	f.Confirmations = append(f.Confirmations, c)
	return r.save(f)
}

// writeFileSync writes b to path with mode, creating or truncating it, and
// fsyncs before closing so a rename after it publishes bytes that are on
// disk.
func writeFileSync(path string, b []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// fsyncDir makes a directory entry change (a link, a rename, an unlink)
// durable. Same discipline as core/internal/lifecycle/commit.go's fsyncDir.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("keycustody: fsync %s: %w", dir, err)
	}
	return nil
}
