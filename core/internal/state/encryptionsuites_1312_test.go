// Migration 0020 and the suite a key belongs to (EPIC T, T1.2, #1312).
//
// What these pin: 0020 only creates its side table and alters nothing (so
// compat cell 03-migrated-schema stays additive, which is why it is a side
// table at all), a key written before it reads age/v1, a key's suite is
// written in the same transaction as the key, a suite the registry does not
// know is refused for a key and for a per-copy record (ADR 0033 D7.3), and
// the one-active-managed rule still holds whatever the suites.

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
)

const keySuitesMigrationVersion = 20

func TestMigration0020IsAdditiveAndOnlyAddsTheKeySuiteTable(t *testing.T) {
	m := migrationSQL(t, keySuitesMigrationVersion)
	if !strings.HasPrefix(m.filename, "0020_") {
		t.Fatalf("migration 20 is %s", m.filename)
	}
	sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", m.filename))
	if err != nil {
		t.Fatal(err)
	}
	stripped := regexp.MustCompile(`(?m)^\s*--.*$`).ReplaceAllString(string(sql), "")
	statements := 0
	for _, stmt := range strings.Split(stripped, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		statements++
		if !strings.HasPrefix(strings.ToUpper(stmt), "CREATE TABLE ENCRYPTION_KEY_SUITES") {
			t.Errorf("0020 may only create encryption_key_suites; it also does: %.80s", stmt)
		}
	}
	if statements == 0 {
		t.Fatal("0020 parsed into no statements, so this check inspected nothing")
	}
	up := strings.ToUpper(stripped)
	for _, banned := range []string{"ALTER TABLE", "DROP INDEX", "DROP TABLE"} {
		if strings.Contains(up, banned) {
			t.Errorf("0020 contains %s; encryption_keys and its index stay exactly as 0015 left them, or compat cell 03-migrated-schema stops being additive", banned)
		}
	}
}

func TestMigration0020GivesEveryExistingKeyAgeV1(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	// A key as a journal that predates 0020 holds it: a row in
	// encryption_keys and none in encryption_key_suites.
	if _, err := j.db.ExecContext(ctx,
		`INSERT INTO encryption_keys (id, provider, recipient, status, created_at) VALUES ('k-old', 'managed', 'age1old', 'active', ?)`,
		formatTime(at)); err != nil {
		t.Fatal(err)
	}
	got, err := j.EncryptionKey(ctx, "k-old")
	if err != nil {
		t.Fatal(err)
	}
	if got.SuiteID != "age/v1" {
		t.Errorf("a key written before 0020 reads suite %q, want age/v1", got.SuiteID)
	}
	keys, err := j.EncryptionKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].SuiteID != "age/v1" {
		t.Errorf("EncryptionKeys = %+v, %v; want the one key with suite age/v1", keys, err)
	}
}

func TestAddEncryptionKeyRecordsTheSuiteInTheSameWrite(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if err := j.AddEncryptionKey(ctx, EncryptionKey{ID: "k1", Provider: KeyProviderManaged, CreatedAt: at, SuiteID: "age/v1"}, ""); err != nil {
		t.Fatal(err)
	}
	var suite string
	if err := j.db.QueryRowContext(ctx, `SELECT suite_id FROM encryption_key_suites WHERE key_id = 'k1'`).Scan(&suite); err != nil || suite != "age/v1" {
		t.Errorf("encryption_key_suites for k1 = %q, %v; want age/v1 written with the key", suite, err)
	}
	// An empty suite is age/v1, written explicitly rather than left absent.
	if err := j.AddEncryptionKey(ctx, EncryptionKey{ID: "k2", Provider: KeyProviderManaged, CreatedAt: at.Add(time.Hour)}, "k1"); err != nil {
		t.Fatal(err)
	}
	if err := j.db.QueryRowContext(ctx, `SELECT suite_id FROM encryption_key_suites WHERE key_id = 'k2'`).Scan(&suite); err != nil || suite != "age/v1" {
		t.Errorf("encryption_key_suites for k2 = %q, %v; want age/v1", suite, err)
	}
}

func TestAddEncryptionKeyRefusesAnUnregisteredSuite(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for _, bad := range []string{"required", "age", "AGE/V1", "tink-aes256gcm-hkdf-1mb/v1"} {
		err := j.AddEncryptionKey(ctx, EncryptionKey{ID: "k-" + bad, Provider: KeyProviderManaged, CreatedAt: at, SuiteID: bad}, "")
		if !errors.Is(err, ErrUnknownSuite) {
			t.Errorf("a key under suite %q = %v, want ErrUnknownSuite", bad, err)
		}
		if _, err := j.EncryptionKey(ctx, "k-"+bad); !errors.Is(err, ErrEncryptionKeyNotFound) {
			t.Errorf("a key under suite %q was registered although it was refused: %v", bad, err)
		}
	}
}

// The one-active-managed index from 0015 is still there: two active managed
// keys are refused whatever suites they name. Phase 1's second suite (#1313)
// adds the cross-suite case beside this one.
func TestTwoActiveManagedKeysAreRefusedWhateverTheirSuites(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if err := j.AddEncryptionKey(ctx, EncryptionKey{ID: "k1", Provider: KeyProviderManaged, CreatedAt: at, SuiteID: "age/v1"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.AddEncryptionKey(ctx, EncryptionKey{ID: "k2", Provider: KeyProviderManaged, CreatedAt: at.Add(time.Hour), SuiteID: "age/v1"}, ""); err == nil {
		t.Fatal("a second active managed key was registered without superseding the first")
	}
	// And the refused key left no suite row behind: the suite is written in
	// the key's own transaction or not at all.
	var n int
	if err := j.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM encryption_key_suites WHERE key_id = 'k2'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("encryption_key_suites holds %d row(s) for the refused key k2 (err %v)", n, err)
	}
}

func TestRecordPlacementEncryptionRefusesAnUnregisteredSuite(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if err := j.RegisterEncryptionKey(ctx, EncryptionKey{ID: "k1", Provider: KeyProviderManaged, CreatedAt: scrubAt}); err != nil {
		t.Fatal(err)
	}
	ok := PlacementEncryption{FormatID: "age/v1", FormatVersion: 1, KeyID: "k1", StoredHash: "c", StoredHashAlg: "sha256", StoredSize: 5000}

	// The planted violation: a config string where the suite's own id
	// belongs. The engine writes the id from the suite that produced the
	// bytes; this is what writing it from config text would look like.
	for _, planted := range []string{"default", "required", "age"} {
		bad := ok
		bad.FormatID = planted
		if err := j.RecordPlacementEncryption(ctx, a, MediumLocal, bad); !errors.Is(err, ErrUnknownSuite) {
			t.Errorf("a record naming %q = %v, want ErrUnknownSuite", planted, err)
		}
	}
	if _, encrypted, err := j.PlacementEncryption(ctx, a, MediumLocal); err != nil || encrypted {
		t.Fatalf("a refused record was written: encrypted=%v err=%v", encrypted, err)
	}

	wrongVersion := ok
	wrongVersion.FormatVersion = 2
	if err := j.RecordPlacementEncryption(ctx, a, MediumLocal, wrongVersion); err == nil {
		t.Error("a record naming age/v1 at version 2 was accepted; the version is the suite's own")
	}

	// The control: the suite's real id and version are accepted.
	if err := j.RecordPlacementEncryption(ctx, a, MediumLocal, ok); err != nil {
		t.Fatalf("the age/v1 record was refused: %v", err)
	}
}
