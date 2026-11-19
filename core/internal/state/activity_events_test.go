package state

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestMigration0018IsAdditiveAndAddsTheActivityTable(t *testing.T) {
	m := migrationSQL(t, 18)
	if !strings.HasPrefix(m.filename, "0018_") {
		t.Fatalf("migration 18 is %s", m.filename)
	}
	sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", m.filename))
	if err != nil {
		t.Fatal(err)
	}
	// Strip comments, then every statement must create something new.
	stripped := regexp.MustCompile(`(?m)^\s*--.*$`).ReplaceAllString(string(sql), "")
	for _, stmt := range strings.Split(stripped, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		up := strings.ToUpper(stmt)
		if !strings.HasPrefix(up, "CREATE TABLE ACTIVITY_EVENTS") &&
			!strings.HasPrefix(up, "CREATE INDEX IDX_ACTIVITY_EVENTS_") {
			t.Errorf("0018 may only create the activity table and its indexes; it also does: %.80s", stmt)
		}
	}
}

func TestActivityEventsRoundTripPageAndCoalesce(t *testing.T) {
	j := func() *Journal { j, _ := openJournal(t); return j }()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := func(seq int64, event, set, op string) ActivityEventRow {
		scope := "set"
		if set == "" {
			scope = "deployment"
		}
		return ActivityEventRow{Seq: seq, At: now, Level: "info", Event: event, Scope: scope, SetID: set, OpID: op, ActionID: op, Fields: `[{"k":"a","v":"b"}]`}
	}
	rows := []ActivityEventRow{
		row(1, "a", "s/1", ""), row(2, "a", "s/2", ""), row(3, "a", "s/1", ""),
		row(4, "a", "", ""), row(5, "a", "s/1", ""),
	}
	if err := j.AppendActivityEvents(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if max, _ := j.MaxActivityEventSeq(ctx); max != 5 {
		t.Fatalf("max = %d", max)
	}
	got, err := j.RecentActivityEvents(ctx, "s/1", 2)
	if err != nil || len(got) != 2 || got[0].Seq != 3 || got[1].Seq != 5 {
		t.Fatalf("recent = %+v, %v; want sequences 3 and 5 oldest first", got, err)
	}
	if dep, _ := j.RecentActivityEvents(ctx, "", 10); len(dep) != 1 || dep[0].Seq != 4 {
		t.Fatalf("deployment bucket = %+v", dep)
	}
	page, more, _ := j.ActivityEventsBefore(ctx, "s/1", 5, 1)
	if len(page) != 1 || page[0].Seq != 3 || !more {
		t.Fatalf("page = %+v more=%v", page, more)
	}
	if !got[0].At.Equal(now) || got[0].Fields != `[{"k":"a","v":"b"}]` {
		t.Fatalf("round trip lost data: %+v", got[0])
	}

	st := func(seq int64) ActivityEventRow {
		r := row(seq, "recovery_stage", "s/1", "op")
		r.Coalesce = true
		return r
	}
	_ = j.AppendActivityEvents(ctx, []ActivityEventRow{st(10)})
	_ = j.AppendActivityEvents(ctx, []ActivityEventRow{st(11)})
	ops, _ := j.ActivityEventsOfOperation(ctx, "op")
	if len(ops) != 1 || ops[0].Seq != 11 {
		t.Fatalf("coalescing kept %+v", ops)
	}
}

// The planted control for the renumber (#1071): 0018 is the activity feed's
// number, EPIC P keeps 0017, and a second file claiming 0018 must be refused
// by the loader rather than quietly ordered. The first half proves the real
// set loads, so the refusal below is about the planted file and nothing else.
func TestASecond0018MigrationIsRefusedByTheLoader(t *testing.T) {
	real := os.DirFS(filepath.Join("..", "..", "migrations"))
	if _, err := loadMigrationsFrom(real); err != nil {
		t.Fatalf("the shipped migrations do not load, so the control proves nothing: %v", err)
	}
	planted := fstest.MapFS{}
	entries, err := fs.ReadDir(real, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(real, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		planted[e.Name()] = &fstest.MapFile{Data: b}
	}
	planted["0018_another_epic.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE another_epic (id INTEGER);\n")}
	_, err = loadMigrationsFrom(planted)
	if err == nil {
		t.Fatal("two files claiming version 18 loaded without complaint")
	}
	if !strings.Contains(err.Error(), "0018_activity_events.sql") || !strings.Contains(err.Error(), "0018_another_epic.sql") {
		t.Fatalf("the refusal should name both files: %v", err)
	}
}

// The prune is two kinds of single statement (#1247), each of which removes
// at most one batch, so the writer can give every statement a deadline of
// its own and let the connection go between them. A prune that removed
// everything in one statement held the journal's only connection for as
// long as that took.

func appendActivityRows(t *testing.T, j *Journal, n int, at func(seq int) time.Time) {
	t.Helper()
	rows := make([]ActivityEventRow, 0, n)
	for i := 1; i <= n; i++ {
		rows = append(rows, ActivityEventRow{Seq: int64(i), At: at(i), Level: "info", Event: "e", Scope: "set", SetID: "s/1", Fields: "[]"})
	}
	if err := j.AppendActivityEvents(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
}

func activitySeqs(rows []ActivityEventRow) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Seq)
	}
	return out
}

func TestPruningByAgeRemovesAtMostOneBatchOldestFirst(t *testing.T) {
	j, _ := openJournal(t)
	ctx := context.Background()
	now := time.Now().UTC()
	appendActivityRows(t, j, 130, func(seq int) time.Time {
		if seq <= 120 {
			return now.Add(-time.Duration(200-seq) * time.Hour)
		}
		return now
	})
	var got []int64
	for i := 0; i < 5; i++ {
		n, err := j.PruneActivityEventsByAge(ctx, now.Add(-time.Minute), 50)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
		if n < 50 {
			break
		}
	}
	if len(got) != 3 || got[0] != 50 || got[1] != 50 || got[2] != 20 {
		t.Fatalf("batches of 50 removed %v; want 50, 50 and 20 for 120 old rows", got)
	}
	left, _ := j.RecentActivityEvents(ctx, "s/1", 1000)
	if len(left) != 10 || left[0].Seq != 121 {
		t.Fatalf("kept %d rows (%v); want the 10 recent ones", len(left), activitySeqs(left))
	}
}

func TestPruningByAgeTakesTheOldestRowsInTheFirstBatch(t *testing.T) {
	j, _ := openJournal(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// The sequence runs against the clock here, so a batch taken in
	// sequence order would remove the youngest of the old rows first.
	appendActivityRows(t, j, 10, func(seq int) time.Time { return now.Add(-time.Duration(seq) * time.Hour) })
	if _, err := j.PruneActivityEventsByAge(ctx, now, 3); err != nil {
		t.Fatal(err)
	}
	left, _ := j.RecentActivityEvents(ctx, "s/1", 1000)
	if len(left) != 7 || left[len(left)-1].Seq != 7 {
		t.Fatalf("a batch of 3 left %v; want the 3 oldest (seq 8 to 10) gone", activitySeqs(left))
	}
}

func TestPruningOverTheRowBoundRemovesTheOldestInBatches(t *testing.T) {
	j, _ := openJournal(t)
	ctx := context.Background()
	now := time.Now().UTC()
	appendActivityRows(t, j, 120, func(int) time.Time { return now })
	var got []int64
	for i := 0; i < 6; i++ {
		n, err := j.PruneActivityEventsOverCount(ctx, 50, 30)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
		if n < 30 {
			break
		}
	}
	if len(got) != 3 || got[0] != 30 || got[1] != 30 || got[2] != 10 {
		t.Fatalf("batches of 30 removed %v; want 30, 30 and 10 for 70 rows over a bound of 50", got)
	}
	left, _ := j.RecentActivityEvents(ctx, "s/1", 1000)
	if len(left) != 50 || left[0].Seq != 71 {
		t.Fatalf("kept %d rows (%v); want the newest 50", len(left), activitySeqs(left))
	}
	if n, err := j.PruneActivityEventsOverCount(ctx, 50, 30); n != 0 || err != nil {
		t.Fatalf("a table at its bound lost %d rows (%v)", n, err)
	}
}

func TestPruningUnderTheRowBoundRemovesNothing(t *testing.T) {
	j, _ := openJournal(t)
	appendActivityRows(t, j, 20, func(int) time.Time { return time.Now().UTC() })
	if n, err := j.PruneActivityEventsOverCount(context.Background(), 50, 30); n != 0 || err != nil {
		t.Fatalf("a table under its bound lost %d rows (%v)", n, err)
	}
}

// Coalescing a recovery stage runs inside the writer's transaction, which
// holds the journal's only connection and its write lock. A delete that
// walks the whole table there costs every other caller the walk, and on a
// host whose page cache a scrub has just evicted that walk is disk reads.
func TestCoalescingAStageReadsTheOperationIndexNotTheTable(t *testing.T) {
	j, _ := openJournal(t)
	q, args := coalesceStatement(ActivityEventRow{Event: "recovery_stage", OpID: "op", ActionID: "op", Coalesce: true})
	rows, err := j.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "; ")
	if !strings.Contains(joined, "idx_activity_events_op") {
		t.Fatalf("the coalescing delete's plan is %q; it walks the table instead of the index 0018 made for it", joined)
	}
}
