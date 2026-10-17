package state

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/retnd/retnd/core/internal/model"
)

// LastRecoveryTest is the newest recovery test recorded under one validator
// id, with the artifact it was recorded on.
type LastRecoveryTest struct {
	State    string
	At       time.Time
	Artifact model.ArtifactID
}

// NameScan is what the custom validators' list, preview and pattern reads need
// from a backup set (#1219): the artifact names, and which validators have run.
type NameScan struct {
	// Names are the set's artifact names, newest discovered first. Two with
	// the same discovery time keep the order they were first recorded in.
	Names []string
	// LastTests maps a recovery_validator_id to the newest test recorded
	// under it, over every copy of every artifact in the set.
	LastTests map[string]LastRecoveryTest
}

// NamesAndLastRecoveryTests reads a backup set the way ListByBackupSet does
// and keeps only what the validator scans use. ListByBackupSet decodes every
// column of every artifact and then loads every placement of every artifact,
// to hand over a name and, for the copies that were ever tested, a validator
// id, a state and a time. This reads the names and the tested copies and
// nothing else, so its cost follows the number of names and not the width of a
// record.
func (j *Journal) NamesAndLastRecoveryTests(ctx context.Context, set model.BackupSetID) (NameScan, error) {
	out := NameScan{LastTests: map[string]LastRecoveryTest{}}

	rows, err := j.db.QueryContext(ctx,
		`SELECT artifact_name, discovered_at FROM artifacts WHERE source = ? AND backup_set = ? ORDER BY id`,
		set.Source, set.Set)
	if err != nil {
		return NameScan{}, fmt.Errorf("state: list artifact names: %w", err)
	}
	type named struct {
		name string
		at   time.Time
	}
	var all []named
	for rows.Next() {
		var name, discovered string
		if err := rows.Scan(&name, &discovered); err != nil {
			_ = rows.Close()
			return NameScan{}, fmt.Errorf("state: scan artifact name: %w", err)
		}
		// The text sorts lexically only where the timestamps are the same
		// width, so the order is decided on the parsed time, as it is for
		// ListByBackupSet's callers.
		at, err := parseTime(discovered)
		if err != nil {
			_ = rows.Close()
			return NameScan{}, fmt.Errorf("state: stored discovered_at %q is invalid: %w", discovered, err)
		}
		all = append(all, named{name, at})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return NameScan{}, fmt.Errorf("state: list artifact names: %w", err)
	}
	if err := rows.Close(); err != nil {
		return NameScan{}, fmt.Errorf("state: list artifact names: %w", err)
	}
	sort.SliceStable(all, func(i, k int) bool { return all[i].at.After(all[k].at) })
	out.Names = make([]string, len(all))
	for i, n := range all {
		out.Names[i] = n.name
	}

	tested, err := j.db.QueryContext(ctx, `
		SELECT a.artifact_name, pa.recovery_validator_id, pa.recovery_state, pa.recovery_tested_at
		FROM artifacts a
		JOIN placements p ON p.artifact_id = a.id
		JOIN placement_assurance pa ON pa.placement_id = p.id
		WHERE a.source = ? AND a.backup_set = ? AND pa.recovery_validator_id <> '' AND pa.recovery_tested_at IS NOT NULL
		ORDER BY a.id, p.id`, set.Source, set.Set)
	if err != nil {
		return NameScan{}, fmt.Errorf("state: list recovery tests: %w", err)
	}
	defer tested.Close()
	for tested.Next() {
		var name, validator, testedAt string
		var st sql.NullString
		if err := tested.Scan(&name, &validator, &st, &testedAt); err != nil {
			return NameScan{}, fmt.Errorf("state: scan recovery test: %w", err)
		}
		at, err := parseTime(testedAt)
		if err != nil {
			return NameScan{}, fmt.Errorf("state: stored placement recovery_tested_at is invalid: %w", err)
		}
		if cur, ok := out.LastTests[validator]; ok && !at.After(cur.At) {
			continue
		}
		artifact, err := model.NewArtifactID(set, name)
		if err != nil {
			return NameScan{}, fmt.Errorf("state: stored artifact id %q is invalid: %w", name, err)
		}
		out.LastTests[validator] = LastRecoveryTest{State: orDefault(st, RecoveryUnknown), At: at.UTC(), Artifact: artifact}
	}
	if err := tested.Err(); err != nil {
		return NameScan{}, fmt.Errorf("state: list recovery tests: %w", err)
	}
	return out, nil
}
