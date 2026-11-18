package state

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// U1.5 (#1348): the batch write, the page and the two prunes on agent_audit.

func auditRows(n int, at func(i int) time.Time) []AgentAudit {
	rows := make([]AgentAudit, 0, n)
	for i := 0; i < n; i++ {
		tok := "agt_a"
		if i%2 == 1 {
			tok = "agt_b"
		}
		rows = append(rows, AgentAudit{At: at(i), TokenID: tok, Tool: fmt.Sprintf("t%d", i), Transport: "stdio", Status: 200})
	}
	return rows
}

// Falsify: commit row by row, or drop the tail of a batch.
func TestABatchIsStoredWhole(t *testing.T) {
	j, _ := openJournal(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := j.InsertAgentAudits(ctx, auditRows(25, func(int) time.Time { return now })); err != nil {
		t.Fatal(err)
	}
	rows, err := j.AgentAuditPage(ctx, AgentAuditFilter{Limit: 100})
	if err != nil || len(rows) != 25 {
		t.Fatalf("%d rows (%v), want 25", len(rows), err)
	}
	if rows[0].Tool != "t24" {
		t.Errorf("newest row is %s, want t24", rows[0].Tool)
	}
}

// Falsify: ignore a filter.
func TestThePageFiltersAndCursors(t *testing.T) {
	j, _ := openJournal(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := j.InsertAgentAudits(ctx, auditRows(10, func(i int) time.Time { return base.Add(time.Duration(i) * time.Hour) })); err != nil {
		t.Fatal(err)
	}
	if rows, _ := j.AgentAuditPage(ctx, AgentAuditFilter{TokenID: "agt_b", Limit: 100}); len(rows) != 5 {
		t.Errorf("token filter: %d rows", len(rows))
	}
	if rows, _ := j.AgentAuditPage(ctx, AgentAuditFilter{Since: base.Add(7 * time.Hour), Limit: 100}); len(rows) != 3 {
		t.Errorf("since filter: %d rows", len(rows))
	}
	first, _ := j.AgentAuditPage(ctx, AgentAuditFilter{Limit: 4})
	next, _ := j.AgentAuditPage(ctx, AgentAuditFilter{Limit: 4, Before: first[len(first)-1].Seq})
	if len(first) != 4 || len(next) != 4 || next[0].Seq >= first[3].Seq {
		t.Errorf("cursor: pages of %d and %d, second starts at %d after %d", len(first), len(next), next[0].Seq, first[3].Seq)
	}
}

// Falsify: delete the young rows, or take the batch from the wrong end.
func TestPruningByAgeTakesOnlyOldRowsInBatches(t *testing.T) {
	j, _ := openJournal(t)
	ctx := context.Background()
	now := time.Now().UTC()
	rows := auditRows(30, func(i int) time.Time {
		if i < 20 {
			return now.Add(-100 * 24 * time.Hour)
		}
		return now
	})
	if err := j.InsertAgentAudits(ctx, rows); err != nil {
		t.Fatal(err)
	}
	cutoff := now.Add(-90 * 24 * time.Hour)
	var got []int64
	for i := 0; i < 5; i++ {
		n, err := j.PruneAgentAuditByAge(ctx, cutoff, 8)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
		if n < 8 {
			break
		}
	}
	if len(got) != 3 || got[0] != 8 || got[1] != 8 || got[2] != 4 {
		t.Fatalf("batches of 8 removed %v, want 8, 8 and 4 for 20 old rows", got)
	}
	left, _ := j.AgentAuditPage(ctx, AgentAuditFilter{Limit: 100})
	if len(left) != 10 {
		t.Fatalf("%d rows left, want the 10 young ones", len(left))
	}
	if n, err := j.PruneAgentAuditByAge(ctx, cutoff, 8); n != 0 || err != nil {
		t.Fatalf("a table with nothing old lost %d rows (%v)", n, err)
	}
}

// Falsify: count from the front, or ignore the keep.
func TestPruningOverTheRowCapKeepsTheNewest(t *testing.T) {
	j, _ := openJournal(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := j.InsertAgentAudits(ctx, auditRows(40, func(int) time.Time { return now })); err != nil {
		t.Fatal(err)
	}
	var total int64
	for i := 0; i < 5; i++ {
		n, err := j.PruneAgentAuditOverCount(ctx, 25, 10)
		if err != nil {
			t.Fatal(err)
		}
		total += n
		if n < 10 {
			break
		}
	}
	left, _ := j.AgentAuditPage(ctx, AgentAuditFilter{Limit: 100})
	if total != 15 || len(left) != 25 || left[0].Tool != "t39" || left[24].Tool != "t15" {
		t.Fatalf("removed %d, %d left (%s..%s), want the newest 25", total, len(left), left[0].Tool, left[len(left)-1].Tool)
	}
}
