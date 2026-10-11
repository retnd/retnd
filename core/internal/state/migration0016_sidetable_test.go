package state

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"
)

// Panel finding C12. Migration 0016 used to widen placements, a table
// placement_moves references and that the compat corpus (FR-35, cell 03)
// promises never to change. These pin the replacement: a side table keyed by
// the placement, with placements itself left exactly as 0013 made it.

func columnNames(t *testing.T, cols map[string]columnInfo) []string {
	t.Helper()
	out := make([]string, 0, len(cols))
	for k := range cols {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestMigration0016_LeavesThePlacementsTableAsItFoundIt_C12(t *testing.T) {
	before, _ := openLikeProduction(t)
	migrateThrough(t, before, 13)
	want := columnNames(t, placementColumnSet(t, before, "placements"))

	after, _ := openLikeProduction(t)
	if err := migrate(context.Background(), after); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got := columnNames(t, placementColumnSet(t, after, "placements"))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("0016 changed placements, which placement_moves references and FR-35 cell 03 forbids changing:\n before: %v\n  after: %v", want, got)
	}
}

func TestMigration0016_PlacementAssuranceIsOneRowPerPlacementAndDiesWithIt_C12(t *testing.T) {
	ctx := context.Background()
	db, _ := openLikeProduction(t)
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cols := placementColumnSet(t, db, "placement_assurance")
	for _, want := range []string{
		"placement_id", "assurance_state", "next_due_at", "last_checked_at", "last_checked_class",
		"content_verified_at", "last_failure_at", "last_failure_reason", "consecutive_failures",
		"last_bytes_read", "last_duration_ms", "scrub_lease_until", "scrub_lease_holder",
		"recovery_state", "recovery_tested_at", "recovery_next_due_at", "recovery_failure_reason",
		"recovery_validator_id", "recovery_validator_hash", "recovery_digest_matched",
		"recovery_termination", "recovery_output_truncated", "recovery_cleanup_state",
		"recovery_bytes_read", "recovery_duration_ms",
	} {
		if _, ok := cols[want]; !ok {
			t.Errorf("placement_assurance has no %q column; got %v", want, columnNames(t, cols))
		}
	}

	rows, err := db.Query(`PRAGMA foreign_key_list(placement_assurance)`)
	if err != nil {
		t.Fatalf("foreign_key_list: %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var (
			id, seq                         int
			table, from, to, onUp, onDel, m string
		)
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUp, &onDel, &m); err != nil {
			t.Fatal(err)
		}
		if table == "placements" && from == "placement_id" && onDel == "CASCADE" {
			found = true
		}
	}
	if !found {
		t.Error("placement_assurance.placement_id must reference placements(id) ON DELETE CASCADE, so a claim cannot outlive its copy")
	}
}

// Absence means unknown and due. A copy nobody has scrubbed has no row, and
// reads exactly as the zero value that cannot look good.
func TestACopyNobodyHasScrubbedHasNoAssuranceRowAndReadsUnknownAndDue_C12(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	var n int
	if err := j.db.QueryRowContext(ctx, `SELECT count(*) FROM placement_assurance`).Scan(&n); err != nil {
		t.Fatalf("placement_assurance missing: %v", err)
	}
	if n != 0 {
		t.Errorf("%d assurance rows exist before anything was scrubbed; rows are created lazily", n)
	}
	p := localCopy(t, j, a)
	if p.AssuranceState != AssuranceUnknown || p.NextDueAt != nil || !p.DueAt(time.Now()) || p.RecoveryState != RecoveryUnknown {
		t.Errorf("an unscrubbed copy reads %+v", p.Assurance)
	}

	recordPass(t, j, a, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	if err := j.db.QueryRowContext(ctx, `SELECT count(*) FROM placement_assurance`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("after one verdict: %d rows, %v", n, err)
	}
	if _, err := j.db.ExecContext(ctx, `DELETE FROM placements`); err != nil {
		t.Fatalf("deleting the placement: %v", err)
	}
	if err := j.db.QueryRowContext(ctx, `SELECT count(*) FROM placement_assurance`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the claim outlived its placement: %d rows, %v", n, err)
	}
}
