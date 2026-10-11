// Migration 0016, the per-copy assurance record (EPIC Q, #978 and #980).
//
// What these pin is the part of the schema other code leans on without
// being able to see it: the zero value of every assurance column is the
// answer that cannot look good, a closed vocabulary stays closed, and an
// upgrade over a populated journal leaves every copy that already existed
// unknown and due rather than fresh.

package state

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// assuranceMigrationVersion is the version this file's subject is. It is
// named once so a renumbering breaks in one place.
const assuranceMigrationVersion = 16

// migrateThrough applies every embedded migration whose version is at most
// upTo, in order, without asserting that the versions are contiguous.
//
// migrateUpTo (placements_test.go) does assert that, and it is right to for
// the versions it is used with. 0014 and 0015 are reserved by two other
// epics that have not landed, so a contiguity check would fail for a reason
// that has nothing to do with this migration.
func migrateThrough(t *testing.T, db *sql.DB, upTo int) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, bootstrapSchemaMigrationsSQL); err != nil {
		t.Fatalf("bootstrap schema_migrations: %v", err)
	}
	known, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range known {
		if m.version > upTo {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			t.Fatalf("applying migration %d: %v", m.version, err)
		}
	}
}

// placementColumnSet reads PRAGMA table_info(placements) into a map of
// column name to (type, notnull, default).
type columnInfo struct {
	typ     string
	notNull bool
	dflt    sql.NullString
}

func placementColumnSet(t *testing.T, db *sql.DB, table string) map[string]columnInfo {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	out := map[string]columnInfo{}
	for rows.Next() {
		var (
			cid    int
			name   string
			typ    string
			nn     int
			dflt   sql.NullString
			pkFlag int
		)
		if err := rows.Scan(&cid, &name, &typ, &nn, &dflt, &pkFlag); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		out[name] = columnInfo{typ: typ, notNull: nn == 1, dflt: dflt}
	}
	return out
}

// TestMigration0016_AddsTheAssuranceRecordBesideTheClass names every column
// the record needs, because a column that is missing is a feature that
// silently writes nowhere.
func TestMigration0016_AddsTheAssuranceRecordBesideTheClass(t *testing.T) {
	db, _ := openLikeProduction(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cols := placementColumnSet(t, db, "placements")

	for _, want := range []string{
		// The scrub claim (#978).
		"assurance_state", "next_due_at", "last_checked_at", "last_checked_class",
		"content_verified_at", "last_failure_at", "last_failure_reason",
		"consecutive_failures", "last_bytes_read", "last_duration_ms",
		"scrub_lease_until", "scrub_lease_holder",
		// The recovery-test claim (#980), a separate home on purpose.
		"recovery_state", "recovery_tested_at", "recovery_next_due_at",
		"recovery_failure_reason", "recovery_validator_id", "recovery_validator_hash",
		"recovery_digest_matched", "recovery_termination", "recovery_output_truncated",
		"recovery_cleanup_state", "recovery_bytes_read", "recovery_duration_ms",
		// The two it lands beside.
		"verification_class", "verified_at",
	} {
		if _, ok := cols[want]; !ok {
			t.Errorf("placements has no %q column after migrating; got %v", want, keysOf(cols))
		}
	}
}

func keysOf(m map[string]columnInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestMigration0016_TheZeroValueCannotLookGood is the invariant the whole
// epic hangs on: a copy nobody has looked at must read as unknown, owe a
// check now, and carry no age that any surface could render as freshness.
func TestMigration0016_TheZeroValueCannotLookGood(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	set := testArtifact(t).Set
	driveOneArtifact(t, j, set, "zero-value.dump", time.Now().UTC())

	var (
		assurance, recovery, lastClass string
		failures                       int
		next, checked, contentAt       sql.NullString
		lastFailure, recTested         sql.NullString
	)
	err := j.db.QueryRowContext(ctx, `
		SELECT assurance_state, recovery_state, last_checked_class, consecutive_failures,
		       next_due_at, last_checked_at, content_verified_at, last_failure_at, recovery_tested_at
		  FROM placements`).Scan(&assurance, &recovery, &lastClass, &failures,
		&next, &checked, &contentAt, &lastFailure, &recTested)
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	if assurance != "unknown" {
		t.Errorf("assurance_state = %q, want %q: the zero value must be the one that cannot look good", assurance, "unknown")
	}
	if recovery != "unknown" {
		t.Errorf("recovery_state = %q, want %q", recovery, "unknown")
	}
	if lastClass != "" {
		t.Errorf("last_checked_class = %q, want empty: nothing has checked this copy", lastClass)
	}
	if failures != 0 {
		t.Errorf("consecutive_failures = %d, want 0", failures)
	}
	for name, v := range map[string]sql.NullString{
		"next_due_at": next, "last_checked_at": checked, "content_verified_at": contentAt,
		"last_failure_at": lastFailure, "recovery_tested_at": recTested,
	} {
		if v.Valid {
			t.Errorf("%s = %q, want NULL: a default that names a time is a claim", name, v.String)
		}
	}
}

// TestMigration0016_TheVocabulariesAreClosed pins the CHECK constraints from
// both sides: every value the code writes is admitted, and a value it does
// not write is refused by the schema rather than stored and rendered.
func TestMigration0016_TheVocabulariesAreClosed(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	driveOneArtifact(t, j, testArtifact(t).Set, "closed.dump", time.Now().UTC())

	set := func(column, value string) error {
		_, err := j.db.ExecContext(ctx, `UPDATE placements SET `+column+` = ?`, value)
		return err
	}
	for _, v := range []string{"unknown", "passed", "failed", "requires_restore", "could_not_complete"} {
		if err := set("assurance_state", v); err != nil {
			t.Errorf("assurance_state %q refused: %v", v, err)
		}
	}
	for _, v := range []string{"unknown", "not_configured", "never_run", "passed", "overdue", "failed", "refused_for_cost", "could_not_complete"} {
		if err := set("recovery_state", v); err != nil {
			t.Errorf("recovery_state %q refused: %v", v, err)
		}
	}
	for _, c := range []struct{ column, value string }{
		{"assurance_state", "verified"},
		{"assurance_state", ""},
		{"assurance_state", "PASSED"},
		{"recovery_state", "green"},
		{"recovery_state", ""},
		{"last_checked_class", "full"},
		{"recovery_termination", "vanished"},
		{"recovery_cleanup_state", "maybe"},
	} {
		if err := set(c.column, c.value); err == nil {
			t.Errorf("%s = %q was accepted: a closed vocabulary that admits a stranger is not closed", c.column, c.value)
		} else if !strings.Contains(strings.ToLower(err.Error()), "constraint") {
			t.Errorf("%s = %q failed for the wrong reason: %v", c.column, c.value, err)
		}
	}
	if _, err := j.db.ExecContext(ctx, `UPDATE placements SET consecutive_failures = -1`); err == nil {
		t.Error("consecutive_failures = -1 was accepted")
	}
}

// TestMigration0016_AnUpgradedCopyIsUnknownAndDueNotFresh is the upgrade
// half of the same invariant. A copy 0007 left with verification_class =
// 'content' and a fresh-looking verified_at is a claim nobody has re-read
// since, and the new record has to read it as exactly that.
func TestMigration0016_AnUpgradedCopyIsUnknownAndDueNotFresh(t *testing.T) {
	ctx := context.Background()
	db, _ := openLikeProduction(t)
	migrateThrough(t, db, 13)

	populateLikeADeployment(t, ctx, db)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO placements (artifact_id, medium, location, hash, hash_alg,
		                        verification_class, verified_at, status, created_at, updated_at)
		SELECT id, 'local', '/local/daily/db.dump', 'abc123', 'sha256', 'content', ?, 'ACTIVE', ?, ?
		  FROM artifacts`, now, now, now); err != nil {
		t.Fatalf("seeding a verified placement: %v", err)
	}

	if err := migrate(ctx, db); err != nil {
		t.Fatalf("migrating a populated v13 database to latest: %v", err)
	}

	var (
		assurance string
		next      sql.NullString
		contentAt sql.NullString
		class     string
	)
	if err := db.QueryRowContext(ctx, `
		SELECT assurance_state, next_due_at, content_verified_at, verification_class FROM placements`).
		Scan(&assurance, &next, &contentAt, &class); err != nil {
		t.Fatalf("reading the upgraded copy: %v", err)
	}
	if assurance != "unknown" || next.Valid || contentAt.Valid {
		t.Errorf("upgraded copy reads assurance=%q next_due_at=%v content_verified_at=%v; want unknown, NULL, NULL",
			assurance, next, contentAt)
	}
	if class != "content" {
		t.Errorf("verification_class = %q: the migration must not rewrite what 0007 recorded", class)
	}
}

// TestMigration0016_ScrubPassesIsAnAppendOnlyCountsTable pins the durable
// per-pass record: counts, never a row per copy.
func TestMigration0016_ScrubPassesIsAnAppendOnlyCountsTable(t *testing.T) {
	db, _ := openLikeProduction(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cols := placementColumnSet(t, db, "scrub_passes")
	for _, want := range []string{
		"id", "triggered_by", "scope", "started_at", "finished_at", "selected", "checked",
		"passed", "failed", "could_not_complete", "requires_restore",
		"skipped_held", "skipped_leased", "bytes_read",
	} {
		if _, ok := cols[want]; !ok {
			t.Errorf("scrub_passes has no %q column", want)
		}
	}
	for name := range cols {
		if strings.Contains(name, "artifact") || strings.Contains(name, "placement") {
			t.Errorf("scrub_passes carries %q: a per-pass row that names a copy is a per-copy row by another name", name)
		}
	}
}

// TestMigration0016_TheDueScanHasAnIndex keeps "which copies are due" from
// being a table scan on a catalog of a million copies.
func TestMigration0016_TheDueScanHasAnIndex(t *testing.T) {
	db, _ := openLikeProduction(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_placements_next_due'`).Scan(&n); err != nil {
		t.Fatalf("index lookup: %v", err)
	}
	if n != 1 {
		t.Error("no idx_placements_next_due index")
	}
}

// TestMigration0016IsTheLatestVersionHere keeps the number honest: 0014 and
// 0015 belong to other epics, and 0016 is this one's.
func TestMigration0016IsTheLatestVersionHere(t *testing.T) {
	m := migrationSQL(t, assuranceMigrationVersion)
	if !strings.HasPrefix(m.filename, "0016_") {
		t.Errorf("migration %d is %s", assuranceMigrationVersion, m.filename)
	}
}
