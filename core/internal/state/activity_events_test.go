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
