package state

import (
	"context"
	"testing"
)

// Issue #1528's migration. RETIRED is a new artifacts.state value, so the
// CHECK constraint has to be widened the way 0002 and 0006 widened it, and the
// retirement record (who, why, the source check, the successor) needs a table
// of its own. A deployment upgrading from 0.8.0 holds exactly the forge's rows:
// FAILED artifacts with transitions and a COMPLETE one with a placement-free
// history, and every one of them has to come through unchanged.
func TestMigration0023KeepsEveryRowAndAdmitsRetired_1528(t *testing.T) {
	ctx := context.Background()
	db, _ := openLikeProduction(t)
	applyUpTo(t, ctx, db, 22)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO artifacts (id, source, backup_set, artifact_name, remote_path, local_path, state, discovered_at, updated_at, last_error)
		VALUES (1, 'cicd-pipeline', 'forge', 'gitea-dump-20261029T034245Z.tar.gz', 'gitea-dump-20261029T034245Z.tar.gz', '', 'FAILED',
		        '2026-10-29T03:42:45Z', '2026-10-29T03:43:00Z', ''),
		       (2, 'cicd-pipeline', 'forge', 'gitea-dump-20261119T033012Z.tar.gz', 'gitea-dump-20261119T033012Z.tar.gz', '/local/forge/gitea-dump-20261119T033012Z.tar.gz', 'COMPLETE',
		        '2026-11-19T03:30:12Z', '2026-11-19T03:35:00Z', '')`); err != nil {
		t.Fatalf("insert artifacts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO state_transitions (artifact_id, idempotency_key, from_state, to_state, occurred_at, detail)
		VALUES (1, 'k1', '', 'DISCOVERED', '2026-10-29T03:42:45Z', ''),
		       (1, 'k2', 'DISCOVERED', 'FAILED', '2026-10-29T03:43:00Z', 'copy_to_local: not_found: object not found'),
		       (2, 'k3', '', 'DISCOVERED', '2026-11-19T03:30:12Z', '')`); err != nil {
		t.Fatalf("insert transitions: %v", err)
	}

	// Then exactly what an upgrade applies: every migration after 0022.
	known, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range known {
		if m.version > 22 {
			if err := applyMigration(ctx, db, m); err != nil {
				t.Fatalf("apply migration %d over a 0.8.0 journal: %v", m.version, err)
			}
		}
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("artifacts after 0023: n=%d err=%v, want 2", n, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM state_transitions`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("state_transitions after 0023: n=%d err=%v, want 3", n, err)
	}
	var st string
	if err := db.QueryRowContext(ctx, `SELECT state FROM artifacts WHERE id = 1`).Scan(&st); err != nil || st != "FAILED" {
		t.Fatalf("the FAILED row reads %q (err %v) after 0023", st, err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE artifacts SET state = 'RETIRED' WHERE id = 1`); err != nil {
		t.Fatalf("the journal refuses RETIRED after 0023: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE artifacts SET state = 'NOT_A_STATE' WHERE id = 2`); err == nil {
		t.Fatal("the widened CHECK accepts an invented state, so it enforces nothing")
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO artifact_retirements (artifact_id, transition_key, retired_at, actor_kind, actor, source_check, superseded_by, failure_reason, note)
		VALUES (1, 'k4', '2026-11-19T04:00:00Z', 'automatic', 'engine', 'absent', 'gitea-dump-20261119T033012Z.tar.gz',
		        'copy_to_local: not_found: object not found', '')`); err != nil {
		t.Fatalf("artifact_retirements is missing or has the wrong shape: %v", err)
	}
}
