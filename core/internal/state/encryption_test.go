// Migration 0015 and the encryption records it adds (EPIC N, N1.1, #989).
//
// What these pin: the migration is additive and only creates its own three
// tables, a journal that predates it reads every copy as unencrypted with
// placements.hash untouched, the key registry's vocabularies agree with the
// schema, a stored-bytes digest that equals the artifact's plaintext digest
// is refused (FR-61's planted violation), and the bundle confirmation is a
// durable fact that survives a reopen.

package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
)

const encryptionMigrationVersion = 15

func TestMigration0015IsAdditiveAndAddsOnlyTheEncryptionTables(t *testing.T) {
	m := migrationSQL(t, encryptionMigrationVersion)
	if !strings.HasPrefix(m.filename, "0015_") {
		t.Fatalf("migration 15 is %s", m.filename)
	}
	sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", m.filename))
	if err != nil {
		t.Fatal(err)
	}
	stripped := regexp.MustCompile(`(?m)^\s*--.*$`).ReplaceAllString(string(sql), "")
	for _, stmt := range strings.Split(stripped, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		up := strings.ToUpper(stmt)
		if !strings.HasPrefix(up, "CREATE TABLE ENCRYPTION_KEYS") &&
			!strings.HasPrefix(up, "CREATE TABLE PLACEMENT_ENCRYPTION") &&
			!strings.HasPrefix(up, "CREATE TABLE RECOVERY_BUNDLE_CONFIRMATIONS") &&
			!strings.HasPrefix(up, "CREATE INDEX IDX_PLACEMENT_ENCRYPTION_") {
			t.Errorf("0015 may only create the key registry, the per-copy record, the bundle confirmation and their indexes; it also does: %.80s", stmt)
		}
	}
	// 0016 moved the FR-35 argument for a side table, and this file leans
	// on it: placements is exactly as 0013 left it, so compat cell 03 stays
	// additive with no waiver.
	if strings.Contains(strings.ToUpper(stripped), "ALTER TABLE") {
		t.Errorf("0015 alters an existing table; the per-copy record is a side table for the reason 0016's header gives")
	}
}

// A journal written before 0015 upgrades with every copy unencrypted and the
// hash it recorded exactly where it was. Nothing is backfilled: absence is
// the honest answer for a copy nobody encrypted.
func TestMigration0015_APreMigrationJournalReadsEveryCopyUnencrypted(t *testing.T) {
	ctx := context.Background()
	db, path := openLikeProduction(t)
	migrateThrough(t, db, 13)
	at := formatTime(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if _, err := db.ExecContext(ctx, `
		INSERT INTO artifacts (source, backup_set, artifact_name, state, remote_path, local_path, local_hash, local_hash_alg, discovered_at, updated_at)
		VALUES ('production', 'postgres-primary', 'old.dump', 'COMPLETE', 'incoming/old.dump', '/backups/old.dump', 'plaintext-digest', 'sha256', ?, ?)`, at, at); err != nil {
		t.Fatalf("seeding an artifact: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO placements (artifact_id, medium, location, size_bytes, hash, hash_alg, verification_class, status, created_at, updated_at)
		SELECT id, 'local', '/backups/old.dump', 4096, 'plaintext-digest', 'sha256', 'content', 'ACTIVE', ?, ? FROM artifacts`, at, at); err != nil {
		t.Fatalf("seeding a placement: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	j, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open over the pre-0015 journal: %v", err)
	}
	defer j.Close()

	set, _ := model.NewBackupSetID("production", "postgres-primary")
	artifact, _ := model.NewArtifactID(set, "old.dump")
	rec, err := j.Get(ctx, artifact)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	p, ok := rec.LocalPlacement()
	if !ok {
		t.Fatal("the seeded placement did not survive the upgrade")
	}
	if p.Hash != "plaintext-digest" || p.HashAlg != "sha256" {
		t.Errorf("placements.hash moved across the upgrade: %q/%q", p.Hash, p.HashAlg)
	}
	if _, encrypted, err := j.PlacementEncryption(ctx, artifact, MediumLocal); err != nil || encrypted {
		t.Errorf("an upgraded copy reads as encrypted (%v, %v); absence of a record is the only honest answer", encrypted, err)
	}
	keys, err := j.EncryptionKeys(ctx)
	if err != nil || len(keys) != 0 {
		t.Errorf("an upgraded journal has keys it never generated: %v %v", keys, err)
	}
}

func TestEncryptionKeys_RegisterListAndTheVocabulariesAgreeWithTheSchema(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	created := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

	key := EncryptionKey{ID: "k-2026-10-24-a", Provider: KeyProviderManaged, Recipient: "age1exampleRecipientNotASecret", CreatedAt: created}
	if err := j.RegisterEncryptionKey(ctx, key); err != nil {
		t.Fatalf("RegisterEncryptionKey: %v", err)
	}
	if err := j.RegisterEncryptionKey(ctx, key); !errors.Is(err, ErrEncryptionKeyExists) {
		t.Errorf("a duplicate id = %v, want ErrEncryptionKeyExists", err)
	}
	ext := EncryptionKey{ID: "k-ext", Provider: KeyProviderExternal, CreatedAt: created.Add(time.Minute)}
	if err := j.RegisterEncryptionKey(ctx, ext); err != nil {
		t.Fatalf("RegisterEncryptionKey(external): %v", err)
	}
	for _, bad := range []EncryptionKey{
		{Provider: KeyProviderManaged, CreatedAt: created},
		{ID: "k-x", Provider: "kms", CreatedAt: created},
		{ID: "k-y", Provider: KeyProviderManaged},
		{ID: "k-z", Provider: KeyProviderManaged, CreatedAt: created, Status: "frozen"},
	} {
		if err := j.RegisterEncryptionKey(ctx, bad); err == nil {
			t.Errorf("RegisterEncryptionKey(%+v) accepted", bad)
		}
	}

	keys, err := j.EncryptionKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].ID != "k-2026-10-24-a" || keys[1].ID != "k-ext" {
		t.Fatalf("EncryptionKeys = %+v", keys)
	}
	got := keys[0]
	if got.Status != EncryptionKeyActive || got.RetiredAt != nil || !got.CreatedAt.Equal(created) ||
		got.Recipient != key.Recipient || got.Provider != KeyProviderManaged {
		t.Errorf("round trip lost a field: %+v", got)
	}
	one, err := j.EncryptionKey(ctx, "k-ext")
	if err != nil || one.Provider != KeyProviderExternal {
		t.Errorf("EncryptionKey(k-ext) = %+v, %v", one, err)
	}
	if _, err := j.EncryptionKey(ctx, "nope"); !errors.Is(err, ErrEncryptionKeyNotFound) {
		t.Errorf("EncryptionKey(nope) = %v, want ErrEncryptionKeyNotFound", err)
	}

	for _, v := range EncryptionKeyStatuses {
		retired := any(nil)
		if v == EncryptionKeyRetired {
			retired = formatTime(created)
		}
		if _, err := j.db.ExecContext(ctx, `UPDATE encryption_keys SET status = ?, retired_at = ? WHERE id = 'k-ext'`, v, retired); err != nil {
			t.Errorf("schema refuses key status %q the code writes: %v", v, err)
		}
	}
	for _, v := range KeyProviders {
		if _, err := j.db.ExecContext(ctx, `UPDATE encryption_keys SET provider = ? WHERE id = 'k-ext'`, v); err != nil {
			t.Errorf("schema refuses provider %q the code writes: %v", v, err)
		}
	}
	if _, err := j.db.ExecContext(ctx, `UPDATE encryption_keys SET status = 'frozen' WHERE id = 'k-ext'`); err == nil {
		t.Error("schema stores a key status the code never writes")
	}
	if _, err := j.db.ExecContext(ctx, `UPDATE encryption_keys SET status = 'retired', retired_at = NULL WHERE id = 'k-ext'`); err == nil {
		t.Error("schema stores a retired key with no retirement time")
	}
	if len(EncryptionKeyStatuses) != 2 || len(KeyProviders) != 2 {
		t.Errorf("vocabulary sizes = %d, %d; a new value needs a migration and a deliberate edit here", len(EncryptionKeyStatuses), len(KeyProviders))
	}
}

// FR-61's planted violation. The stored-bytes digest is the digest of the
// ciphertext, and a write that hands over the artifact's plaintext digest
// is the #662 defect with a new cause: two numbers that disagree about the
// same object are not a measurement.
func TestRecordPlacementEncryption_RefusesThePlaintextDigest(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if err := j.RegisterEncryptionKey(ctx, EncryptionKey{ID: "k1", Provider: KeyProviderManaged, CreatedAt: scrubAt}); err != nil {
		t.Fatal(err)
	}
	rec, err := j.Get(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if rec.LocalHash == "" {
		t.Fatal("the fixture artifact has no plaintext digest, so this test could not plant one")
	}

	planted := PlacementEncryption{FormatID: "age/v1", FormatVersion: 1, KeyID: "k1", StoredHash: rec.LocalHash, StoredHashAlg: rec.LocalHashAlg, StoredSize: 4096 + 16 + 16}
	if err := j.RecordPlacementEncryption(ctx, a, MediumLocal, planted); !errors.Is(err, ErrStoredDigestIsPlaintext) {
		t.Fatalf("the plaintext digest was written as the stored-bytes digest: %v", err)
	}
	if _, encrypted, _ := j.PlacementEncryption(ctx, a, MediumLocal); encrypted {
		t.Fatal("a refused write left a record behind")
	}

	real := planted
	real.StoredHash = "ciphertext-digest-differs"
	if err := j.RecordPlacementEncryption(ctx, a, MediumLocal, real); err != nil {
		t.Fatalf("RecordPlacementEncryption: %v", err)
	}
	got, encrypted, err := j.PlacementEncryption(ctx, a, MediumLocal)
	if err != nil || !encrypted {
		t.Fatalf("PlacementEncryption = %v, %v", encrypted, err)
	}
	if got != real {
		t.Errorf("round trip = %+v, want %+v", got, real)
	}
	// The plaintext digest stays where it was and is not overwritten.
	after, _ := j.Get(ctx, a)
	if after.LocalHash != rec.LocalHash {
		t.Errorf("artifacts.local_hash moved from %q to %q", rec.LocalHash, after.LocalHash)
	}
}

func TestRecordPlacementEncryption_RefusesWhatItCannotDescribe(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if err := j.RegisterEncryptionKey(ctx, EncryptionKey{ID: "k1", Provider: KeyProviderManaged, CreatedAt: scrubAt}); err != nil {
		t.Fatal(err)
	}
	ok := PlacementEncryption{FormatID: "age/v1", FormatVersion: 1, KeyID: "k1", StoredHash: "c", StoredHashAlg: "sha256", StoredSize: 5000}

	if err := j.RecordPlacementEncryption(ctx, a, "offsite", ok); !errors.Is(err, ErrPlacementNotFound) {
		t.Errorf("a copy the journal does not know = %v, want ErrPlacementNotFound", err)
	}
	unknownKey := ok
	unknownKey.KeyID = "never-registered"
	if err := j.RecordPlacementEncryption(ctx, a, MediumLocal, unknownKey); !errors.Is(err, ErrEncryptionKeyNotFound) {
		t.Errorf("an unregistered key = %v, want ErrEncryptionKeyNotFound", err)
	}
	for name, mutate := range map[string]func(*PlacementEncryption){
		"no format":         func(e *PlacementEncryption) { e.FormatID = "" },
		"version zero":      func(e *PlacementEncryption) { e.FormatVersion = 0 },
		"no key":            func(e *PlacementEncryption) { e.KeyID = "" },
		"no digest":         func(e *PlacementEncryption) { e.StoredHash = "" },
		"no digest alg":     func(e *PlacementEncryption) { e.StoredHashAlg = "" },
		"negative size":     func(e *PlacementEncryption) { e.StoredSize = -1 },
		"size is plaintext": func(e *PlacementEncryption) { e.StoredSize = 4096 },
	} {
		e := ok
		mutate(&e)
		if err := j.RecordPlacementEncryption(ctx, a, MediumLocal, e); err == nil {
			t.Errorf("%s: accepted %+v", name, e)
		}
	}
	if _, encrypted, _ := j.PlacementEncryption(ctx, a, MediumLocal); encrypted {
		t.Error("a refused write left a record behind")
	}
}

// The record is about the bytes a copy held. When the copy leaves the
// journal, the record goes with it rather than describing an object that
// is gone.
func TestPlacementEncryption_DiesWithItsPlacement(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if err := j.RegisterEncryptionKey(ctx, EncryptionKey{ID: "k1", Provider: KeyProviderManaged, CreatedAt: scrubAt}); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordPlacementEncryption(ctx, a, MediumLocal, PlacementEncryption{FormatID: "age/v1", FormatVersion: 1, KeyID: "k1", StoredHash: "c", StoredHashAlg: "sha256", StoredSize: 5000}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.db.ExecContext(ctx, `DELETE FROM placements`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := j.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM placement_encryption`).Scan(&n); err != nil || n != 0 {
		t.Errorf("placement_encryption rows after the copy went: %d (%v)", n, err)
	}
	// And the key it named is still registered: a key outlives the copies
	// made under it, which is what lets a retirement be refused by name.
	if _, err := j.EncryptionKey(ctx, "k1"); err != nil {
		t.Errorf("the key went with the copy: %v", err)
	}
}

// FR-66's durable half: a confirmed bundle is a fact in the journal, not a
// computed state, so it is still there after the process that recorded it
// has restarted.
func TestRecoveryBundleConfirmation_SurvivesAReopen(t *testing.T) {
	ctx := context.Background()
	j, path := openJournal(t)
	at := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	if err := j.RegisterEncryptionKey(ctx, EncryptionKey{ID: "k1", Provider: KeyProviderManaged, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, confirmed, err := j.RecoveryBundleConfirmation(ctx, "k1"); err != nil || confirmed {
		t.Fatalf("a fresh key reads as confirmed (%v, %v)", confirmed, err)
	}
	if err := j.ConfirmRecoveryBundle(ctx, RecoveryBundleConfirmation{KeyID: "nope", ConfirmedAt: at}); !errors.Is(err, ErrEncryptionKeyNotFound) {
		t.Errorf("confirming a bundle for an unknown key = %v", err)
	}
	if err := j.ConfirmRecoveryBundle(ctx, RecoveryBundleConfirmation{KeyID: "k1"}); err == nil {
		t.Error("a confirmation with no time was accepted")
	}
	want := RecoveryBundleConfirmation{KeyID: "k1", ConfirmedAt: at, ConfirmedBy: "operator", BundleDigest: "sha256:bundle"}
	if err := j.ConfirmRecoveryBundle(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, confirmed, err := reopened.RecoveryBundleConfirmation(ctx, "k1")
	if err != nil || !confirmed {
		t.Fatalf("after a reopen: confirmed=%v err=%v", confirmed, err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}
