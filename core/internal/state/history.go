package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// CountArtifactsReadOnly answers one question about a journal file without
// touching it: how many artifacts has it ever recorded? It is what a caller
// asks before it decides a deployment is a fresh install (retnd#1049), so it
// opens the file read-only, runs no migration, creates nothing, and does not
// even create the file when it is absent.
//
// exists is false, with a zero count and no error, when there is no file at
// path. A file that exists but has no artifacts table yet (empty, or created
// and never migrated) counts as zero: it has recorded nothing. A file that
// exists and cannot be read as a journal is an error, and the caller must
// treat that as "do not assume it is empty", never as zero.
func CountArtifactsReadOnly(ctx context.Context, path string) (count int64, exists bool, err error) {
	if _, statErr := os.Stat(path); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("state: looking for the journal at %s: %w", path, statErr)
	}
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath(), RawQuery: "mode=ro"}
	db, err := sql.Open(driverName, u.String())
	if err != nil {
		return 0, true, fmt.Errorf("state: opening the journal at %s read-only: %w", path, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return 0, true, fmt.Errorf("state: reading the journal at %s: %w", path, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts`).Scan(&count); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0, true, nil
		}
		return 0, true, fmt.Errorf("state: reading the journal at %s: %w", path, err)
	}
	return count, true, nil
}

// CountArtifacts is the same count over an open journal.
func (j *Journal) CountArtifacts(ctx context.Context) (int64, error) {
	var n int64
	if err := j.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts`).Scan(&n); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("state: counting artifacts: %w", err)
	}
	return n, nil
}
