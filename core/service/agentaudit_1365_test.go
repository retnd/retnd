package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/state"
)

// U1.5 (#1365): the audit table keeps its bound under load, a caller with no
// credential never waits on the writer, and a page names the token. Each test
// names the mutation that has to turn it red.

// Falsify: prune hourly, or cap an over-count pass at 20 batches. Three
// tokens at the limit write 10,800 rows an hour (11,000 here), and the count
// has to be at the cap after every pass, with a fake clock for the rows' ages.
func TestTheRowCapHoldsUnderThreeTokensAtTheLimit(t *testing.T) {
	if agentAuditPruneEvery > 5*time.Minute {
		t.Fatalf("the prune runs every %s, want at most 5m", agentAuditPruneEvery)
	}
	r := newDurableRig(t)
	ctx := context.Background()
	clock := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	r.svc.audit.now = func() time.Time { return clock }
	const (
		rowCap = 500
		rate   = 11_000
	)
	r.svc.audit.maxRows = rowCap
	for hour := 0; hour < 2; hour++ {
		rows := make([]state.AgentAudit, rate)
		for i := range rows {
			rows[i] = state.AgentAudit{
				At: clock.Add(time.Duration(i) * time.Second / 4), TokenID: fmt.Sprintf("agt_%d", i%3),
				Tool: "status", Transport: "http", Status: 200,
			}
		}
		if err := r.journal.InsertAgentAudits(ctx, rows); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(time.Hour)
		if _, err := r.svc.PruneAgentAudit(ctx); err != nil {
			t.Fatal(err)
		}
		if n := len(recordedRows(t, r.svc, AgentAuditQuery{Limit: MaxAgentAuditLimit})); n > rowCap {
			t.Fatalf("hour %d: %d rows after the pass, want at most the cap of %d", hour, n, rowCap)
		}
	}
}

// Falsify: make a refusal that names no token wait for room. The queue is
// full and nothing drains it; the call returns at once, and the feed's
// counter hears about the row.
func TestARefusalWithNoTokenNeverWaitsOnAFullQueue(t *testing.T) {
	r := newDurableRig(t)
	w := newAgentAuditWriter(r.journal, r.svc.logger)
	for i := 0; i < agentAuditQueueSize; i++ {
		if stored, err := w.enqueueOrDrop(agentAuditItem{}); !stored || err != nil {
			t.Fatalf("row %d: stored %v, %v", i, stored, err)
		}
	}
	done := make(chan bool, 1)
	go func() {
		stored, err := w.enqueueOrDrop(agentAuditItem{})
		done <- stored || err != nil
	}()
	select {
	case bad := <-done:
		if bad {
			t.Fatal("a full queue took a row, or failed")
		}
	case <-time.After(time.Second):
		t.Fatal("a refusal that names no token waited on a full queue")
	}

	var feed refusalFeed
	at := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	feed.allow(at)
	feed.hold()
	if _, held := feed.allow(at.Add(agentRefusalFeedEvery)); held != 1 {
		t.Errorf("the next feed line counts %d held, want the dropped row", held)
	}
}

// Falsify: leave the name out of the page. FR-91: the principal is
// agent:<name> on every surface that names an actor.
func TestAnAuditPageNamesTheToken(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	minted := mintForTest(t, r.svc, MintAgentTokenRequest{Name: "triage"})
	if err := r.svc.RecordAgentCall(ctx, AgentCall{TokenID: minted.AgentToken.ID, Tool: "status", Transport: "http", Status: 200}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.RecordAgentRefusal(ctx, AgentRefusal{Method: "GET", Path: "/x", Status: 401, Code: "AGENT_TOKEN_INVALID"}); err != nil {
		t.Fatal(err)
	}
	rows := recordedRows(t, r.svc, AgentAuditQuery{})
	if len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
	if rows[0].TokenName != "" || rows[0].Contract().TokenName != nil {
		t.Errorf("a refusal of a token that matched nothing has the name %q, want none (null)", rows[0].TokenName)
	}
	if n := rows[1].Contract().TokenName; n == nil || *n != "triage" {
		t.Errorf("token_name = %v, want triage", n)
	}
}

// Falsify: make a reservation for a token wait without a bound, or fail open.
// The queue is full and nothing drains it. With the writer's wait cut to 50ms
// the reservation returns ErrAgentAuditStalled once that deadline passes (not
// before, not never); the production value is pinned at five seconds.
func TestAnAuditReservationFailsClosedAfterItsDeadline(t *testing.T) {
	if agentAuditEnqueueWait != 5*time.Second {
		t.Fatalf("the wait for a row with a token is %s, want 5s", agentAuditEnqueueWait)
	}
	r := newDurableRig(t)
	w := newAgentAuditWriter(r.journal, r.svc.logger)
	w.wait = 50 * time.Millisecond
	r.svc.audit = w
	for i := 0; i < agentAuditQueueSize; i++ {
		if stored, err := w.enqueueOrDrop(agentAuditItem{}); !stored || err != nil {
			t.Fatalf("row %d: stored %v, %v", i, stored, err)
		}
	}
	start := time.Now()
	err := r.svc.ReserveAgentAudit(context.Background())
	if !errors.Is(err, ErrAgentAuditStalled) {
		t.Fatalf("a full queue gave %v, want ErrAgentAuditStalled", err)
	}
	if took := time.Since(start); took < 50*time.Millisecond || took > 2*time.Second {
		t.Errorf("gave up after %s, want the 50ms deadline", took)
	}
	if err := newDurableRig(t).svc.ReserveAgentAudit(context.Background()); err != nil {
		t.Errorf("an idle writer refused a reservation: %v", err)
	}
}
