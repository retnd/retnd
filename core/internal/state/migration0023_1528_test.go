package state

import (
	"context"
	"testing"
	"time"
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

	// Then exactly what an upgrade applies: every migration after 0022, with
	// foreign keys suspended the way migrate suspends them.
	restore, err := suspendForeignKeys(ctx, db)
	if err != nil {
		t.Fatalf("suspendForeignKeys: %v", err)
	}
	defer restore()
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

// The transition into RETIRED and its retirement record are one write
// (issue #1528). The failure is planted between the two: a retirement row
// already holds this transition's key, so the record's INSERT fails after the
// artifacts row and the transition log entry have been written inside the
// same transaction. Everything has to roll back.
func TestARetirementAndItsRecordCommitOrRollBackTogether_1528(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	artifact := testArtifact(t)
	at := time.Date(2026, 11, 19, 4, 0, 0, 0, time.UTC)

	if _, err := j.Discover(ctx, artifact, "d1", "/incoming/backup.dump", RemoteIdentity{}, at); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if _, err := j.RecordTransition(ctx, Transition{Artifact: artifact, Key: "f1", From: "DISCOVERED", To: "FAILED", OccurredAt: at, Detail: "copy_to_local: not_found: object not found"}); err != nil {
		t.Fatalf("FAILED: %v", err)
	}
	retire := Transition{
		Artifact: artifact, Key: "retire-1", From: "FAILED", To: RetiredState, OccurredAt: at.Add(time.Hour),
		Retirement: &RetirementUpdate{ActorKind: RetiredAutomatically, Actor: "engine", SourceCheck: SourceAbsent, SupersededBy: "newer.dump",
			FailureReason: "copy_to_local: not_found: object not found"},
	}

	// The plant: a stray record already carrying the key.
	if _, err := j.db.ExecContext(ctx, `INSERT INTO artifact_retirements (artifact_id, transition_key, retired_at, actor_kind, source_check)
		VALUES ((SELECT id FROM artifacts), 'retire-1', '2026-01-01T00:00:00Z', 'automatic', 'plant')`); err != nil {
		t.Fatalf("plant: %v", err)
	}
	if _, err := j.RecordTransition(ctx, retire); err == nil {
		t.Fatal("the retirement went through although its record could not be written")
	}
	rec, err := j.Get(ctx, artifact)
	if err != nil || rec.State != "FAILED" {
		t.Fatalf("after the failed retirement the row reads %q (err %v), want FAILED: the state committed without its record", rec.State, err)
	}
	var n int
	if err := j.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM state_transitions WHERE idempotency_key = 'retire-1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the transition log kept %d entries for the rolled-back retirement (err %v)", n, err)
	}

	// Negative control: with the plant gone the same call commits both.
	if _, err := j.db.ExecContext(ctx, `DELETE FROM artifact_retirements WHERE source_check = 'plant'`); err != nil {
		t.Fatalf("unplant: %v", err)
	}
	if _, err := j.RecordTransition(ctx, retire); err != nil {
		t.Fatalf("the clean retirement failed: %v", err)
	}
	got, found, err := j.LatestRetirement(ctx, artifact)
	if err != nil || !found {
		t.Fatalf("LatestRetirement: found=%v err=%v", found, err)
	}
	if got.SupersededBy != "newer.dump" || got.SourceCheck != SourceAbsent || got.ActorKind != RetiredAutomatically {
		t.Errorf("the record reads %+v", got)
	}

	// A record on any other transition is refused before anything is opened.
	if _, err := j.RecordTransition(ctx, Transition{Artifact: artifact, Key: "x", From: RetiredState, To: "DISCOVERED", OccurredAt: at,
		Retirement: &RetirementUpdate{ActorKind: RetiredByOperator, SourceCheck: SourceAbsent}}); err == nil {
		t.Error("a retirement record was accepted on a transition out of RETIRED")
	}
}
