// Issue #1584: a migration that succeeds leaves the journal as it was before
// it on disk, in a sibling directory, so an operator who upgraded by pulling
// an image (no installer, so no archive) still has a way back.
//
// These drive OpenConfigAndJournal, the real start, against real SQLite files.
// What they pin is the safety envelope as much as the happy path: the copy is
// private, it never costs a migration its success, it is capped and pruned
// oldest first without ever touching the newest copy or one the engine did not
// make, and a start that migrates nothing leaves nothing behind.
package service

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/retnd/retnd/core/internal/state"
)

var retainedCopyName = regexp.MustCompile(`^pre-migration-\d{8}T\d{6}Z-schema(\d+)(-\d+)?$`)

// seedJournalBeforeMigration23 builds a journal at the schema the last release
// shipped: a fully migrated one with migration 0023 un-applied, which is the
// shape of a real upgrade (0023 rebuilds artifacts and is safe to run again
// over a table that already admits RETIRED). It returns the db path.
func seedJournalBeforeMigration23(t *testing.T, dir string) string {
	t.Helper()
	dbPath := filepath.Join(dir, "state.db")
	journal, err := state.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("state.Open (seed): %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("close seed journal: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer db.Close()
	for _, stmt := range []string{
		"DROP TABLE artifact_retirements",
		"DELETE FROM schema_migrations WHERE version = 23",
		"PRAGMA wal_checkpoint(TRUNCATE)",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("rewind to before 0023 (%s): %v", stmt, err)
		}
	}
	return dbPath
}

func startAndClose(t *testing.T, dir, dbPath string) error {
	t.Helper()
	_, journal, release, err := OpenConfigAndJournal(context.Background(), writeConfigFileFor(t, dir, dbPath))
	if err != nil {
		return err
	}
	_ = journal.Close()
	_ = release()
	return nil
}

func retainedCopies(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && (strings.HasPrefix(e.Name(), "pre-migration-") || strings.HasPrefix(e.Name(), ".pre-migration-")) {
			out = append(out, e.Name())
		}
	}
	return out
}

// seedRetainedCopy plants a copy the way the engine writes one: a directory
// named for when and from which schema, holding a MANIFEST and the journal.
func seedRetainedCopy(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatalf("Mkdir %s: %v", p, err)
	}
	for _, f := range []string{"MANIFEST", "state.db"} {
		if err := os.WriteFile(filepath.Join(p, f), []byte("old "+name), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestIssue1584_SuccessfulMigrationKeepsAPrivateCopyOfTheOldJournal(t *testing.T) {
	dir := t.TempDir()
	dbPath := seedJournalBeforeMigration23(t, dir)
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	oldUmask := syscall.Umask(0)
	defer syscall.Umask(oldUmask)
	logs := captureLog(t)

	if err := startAndClose(t, dir, dbPath); err != nil {
		t.Fatalf("start that migrates: %v", err)
	}

	copies := retainedCopies(t, dir)
	if len(copies) != 1 || !retainedCopyName.MatchString(copies[0]) {
		t.Fatalf("retained copies = %v, want exactly one named pre-migration-<UTC time>-schema<N>", copies)
	}
	if m := retainedCopyName.FindStringSubmatch(copies[0]); m[1] != "22" {
		t.Errorf("copy is named for schema %s, want 22 (the schema it holds)", m[1])
	}
	cdir := filepath.Join(dir, copies[0])
	if fi, err := os.Stat(cdir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("copy directory mode = %v (err %v), want 0700", fi.Mode().Perm(), err)
	}
	got, err := os.ReadFile(filepath.Join(cdir, "state.db"))
	if err != nil {
		t.Fatalf("copy has no state.db: %v", err)
	}
	if !bytes.Equal(got, before) {
		t.Error("the copied state.db differs from the journal as it was before the migration")
	}
	entries, _ := os.ReadDir(cdir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		if fi, _ := e.Info(); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", e.Name(), fi.Mode().Perm())
		}
		if e.Name() == "state.db-shm" {
			t.Error("the copy carries -shm, which is derived and which restore never writes back")
		}
	}
	if !containsString(names, "MANIFEST") {
		t.Errorf("copy holds %v, want a MANIFEST saying what it is and how to put it back", names)
	}
	if !strings.Contains(logs.String(), copies[0]) {
		t.Errorf("the log never says where the copy went (%s):\n%s", copies[0], logs.String())
	}

	// A human restores this by hand, so it has to be a real journal at the old schema.
	scratch := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(scratch, got, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", scratch)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&v); err != nil || v != 22 {
		t.Errorf("copied journal max schema = %d (err %v), want 22", v, err)
	}
}

func TestIssue1584_NothingMigratedMeansNothingKept(t *testing.T) {
	t.Run("fresh deployment", func(t *testing.T) {
		dir := t.TempDir()
		if err := startAndClose(t, dir, filepath.Join(dir, "state.db")); err != nil {
			t.Fatal(err)
		}
		if c := retainedCopies(t, dir); len(c) != 0 {
			t.Errorf("a first-ever start kept %v: there was no journal to keep", c)
		}
	})
	t.Run("ordinary restart", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "state.db")
		if err := startAndClose(t, dir, dbPath); err != nil {
			t.Fatal(err)
		}
		if err := startAndClose(t, dir, dbPath); err != nil {
			t.Fatal(err)
		}
		if c := retainedCopies(t, dir); len(c) != 0 {
			t.Errorf("a start with nothing pending kept %v", c)
		}
	})
	t.Run("refusal", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := seedJournalBeforeMigration23(t, dir)
		seedFutureSchemaVersion(t, dbPath)
		if err := startAndClose(t, dir, dbPath); err == nil {
			t.Fatal("a journal from a newer build started")
		}
		if c := retainedCopies(t, dir); len(c) != 0 {
			t.Errorf("a refused start kept %v: nothing was migrated", c)
		}
	})
}

func TestIssue1584_ADiskTooSmallForTheCopyNeverFailsTheMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := seedJournalBeforeMigration23(t, dir)
	defer func(prev func(string) (uint64, bool)) { premigrationFreeBytes = prev }(premigrationFreeBytes)
	premigrationFreeBytes = func(string) (uint64, bool) { return 1, true }
	logs := captureLog(t)

	if err := startAndClose(t, dir, dbPath); err != nil {
		t.Fatalf("a full disk failed the migration: %v", err)
	}
	if c := retainedCopies(t, dir); len(c) != 0 {
		t.Errorf("left %v behind on a disk that had no room (a half copy is worse than none)", c)
	}
	if !strings.Contains(logs.String(), "pre-migration") || !strings.Contains(strings.ToLower(logs.String()), "warn") {
		t.Errorf("no warning that the copy was skipped:\n%s", logs.String())
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&v); err != nil || v < 23 {
		t.Errorf("journal max schema = %d (err %v), want the migration applied", v, err)
	}
}

func TestIssue1584_TheCapPrunesOldestFirstAndOnlyWhatItMade(t *testing.T) {
	dir := t.TempDir()
	dbPath := seedJournalBeforeMigration23(t, dir)
	old := []string{
		"pre-migration-20250101T000000Z-schema10",
		"pre-migration-20250102T000000Z-schema11",
		"pre-migration-20250103T000000Z-schema12",
		"pre-migration-20250104T000000Z-schema13",
		"pre-migration-20250105T000000Z-schema14",
		"pre-migration-20250106T000000Z-schema15",
	}
	for _, n := range old {
		seedRetainedCopy(t, dir, n)
	}
	// Copies a newer build made (schema above anything this binary knows), a
	// directory with the right name and no MANIFEST, and unrelated neighbours.
	newer := "pre-migration-20240101T000000Z-schema9999"
	seedRetainedCopy(t, dir, newer)
	noManifest := "pre-migration-20230101T000000Z-schema5"
	if err := os.Mkdir(filepath.Join(dir, noManifest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "pre-migration-notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)

	if err := startAndClose(t, dir, dbPath); err != nil {
		t.Fatal(err)
	}

	mine := 0
	for _, n := range retainedCopies(t, dir) {
		if n == newer || n == noManifest || n == "pre-migration-notes" {
			continue
		}
		mine++
	}
	if mine != 5 {
		t.Errorf("engine-made copies kept = %d, want the cap of 5 (the new one plus the four newest older ones): %v", mine, retainedCopies(t, dir))
	}
	for _, gone := range old[:2] {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s survived: oldest copies go first", gone)
		}
	}
	for _, kept := range append(old[2:], newer, noManifest, "pre-migration-notes") {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s was removed (%v): only the oldest copies past the cap may go", kept, err)
		}
	}
	for _, gone := range old[:2] {
		if !strings.Contains(logs.String(), gone) {
			t.Errorf("pruning %s was not logged:\n%s", gone, logs.String())
		}
	}
}

func TestIssue1584_TheNewestCopyIsNeverThePrunedOne(t *testing.T) {
	dir := t.TempDir()
	dbPath := seedJournalBeforeMigration23(t, dir)
	// Five existing copies that all sort AFTER the new one (a clock that went
	// backwards, or a restored directory): the cap is still met by removing
	// the oldest of these, never by removing what was just written.
	for i := 1; i <= 5; i++ {
		seedRetainedCopy(t, dir, "pre-migration-2099010"+string(rune('0'+i))+"T000000Z-schema22")
	}
	if err := startAndClose(t, dir, dbPath); err != nil {
		t.Fatal(err)
	}
	var fresh []string
	for _, n := range retainedCopies(t, dir) {
		if !strings.HasPrefix(n, "pre-migration-2099") {
			fresh = append(fresh, n)
		}
	}
	if len(fresh) != 1 {
		t.Fatalf("the copy just written was pruned: %v", retainedCopies(t, dir))
	}
}
