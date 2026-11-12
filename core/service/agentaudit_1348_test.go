package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/obs"
)

// U1.5 (#1348): the audit trail behind FR-97. Each test names the mutation
// that has to turn it red.

func recordedRows(t *testing.T, svc *BackupService, q AgentAuditQuery) []AgentAuditEntry {
	t.Helper()
	rows, _, err := svc.ListAgentAudit(context.Background(), q)
	if err != nil {
		t.Fatalf("ListAgentAudit(%+v): %v", q, err)
	}
	return rows
}

// feedEvents is what an operator reading the deployment's strip and one set's
// strip would see, by event name.
func feedEvents(t *testing.T, svc *BackupService) (deployment, set []LiveActivityEvent) {
	t.Helper()
	live, err := svc.LiveActivity(context.Background(), LiveActivityRequest{Limit: liveActivityMaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if live.Deployment != nil {
		deployment = live.Deployment.Events
	}
	for _, s := range live.Sets {
		if s.BackupSetID == "alpha/nightly" {
			set = s.Events
		}
	}
	return deployment, set
}

func agentActions(events []LiveActivityEvent) []LiveActivityEvent {
	var out []LiveActivityEvent
	for _, e := range events {
		if e.Event == obs.EventAgentAction {
			out = append(out, e)
		}
	}
	return out
}

// Falsify: skip reads. Every call, reads included, is one row.
func TestAReadIsAnAuditRow(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	if err := r.svc.RecordAgentCall(ctx, AgentCall{
		TokenID: "agt_1", TokenName: "triage", Tool: "status", Transport: "stdio",
		Arguments: map[string]any{"verbose": true}, Status: 200, CorrelationID: "cid_read",
	}); err != nil {
		t.Fatal(err)
	}
	rows := recordedRows(t, r.svc, AgentAuditQuery{})
	if len(rows) != 1 {
		t.Fatalf("a read left %d rows, want exactly one", len(rows))
	}
	got := rows[0]
	if got.Tool != "status" || got.Transport != "stdio" || got.TokenID != "agt_1" || got.CorrelationID != "cid_read" || got.Status != 200 {
		t.Errorf("row = %+v", got)
	}
	if got.Arguments != `{"verbose":true}` {
		t.Errorf("arguments = %q, want the call's arguments as JSON", got.Arguments)
	}
}

// Falsify: write two rows for one call, or one row for two calls.
func TestEachCallIsExactlyOneRow(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		tool := "status"
		if i%2 == 1 {
			tool = "run_cycle"
		}
		if err := r.svc.RecordAgentCall(ctx, AgentCall{
			TokenID: "agt_1", TokenName: "triage", Tool: tool, Transport: "http",
			Mutating: tool == "run_cycle", Status: 200, CorrelationID: fmt.Sprintf("cid_%d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(recordedRows(t, r.svc, AgentAuditQuery{})); n != 7 {
		t.Fatalf("seven calls left %d rows", n)
	}
}

// Falsify: skip refusals before the handler. A refusal the authenticator made
// is a row with its code, and it reaches the feed.
func TestARefusalBeforeTheHandlerIsStillAudited(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	minted := mintForTest(t, r.svc, MintAgentTokenRequest{Name: "triage"})
	if err := r.svc.RecordAgentRefusal(ctx, AgentRefusal{
		TokenID: minted.AgentToken.ID, TokenName: "triage", Method: "POST", Path: "/api/v1/backup-sets",
		Status: 403, Code: "AGENT_SCOPE_INSUFFICIENT", CorrelationID: "cid_refused",
	}); err != nil {
		t.Fatal(err)
	}
	rows := recordedRows(t, r.svc, AgentAuditQuery{TokenID: minted.AgentToken.ID})
	if len(rows) != 1 || rows[0].Code != "AGENT_SCOPE_INSUFFICIENT" || rows[0].CorrelationID != "cid_refused" {
		t.Fatalf("rows = %+v", rows)
	}
	deployment, _ := feedEvents(t, r.svc)
	acts := agentActions(deployment)
	if len(acts) != 1 || acts[0].Result != "warn" {
		t.Fatalf("the refusal reached the feed as %+v, want one agent_action with result warn", acts)
	}
}

// Falsify: emit it without a Result, or for a read.
func TestAMutationIsOnTheFeedWithAResultAndAReadIsNot(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	calls := []AgentCall{
		{TokenID: "agt_1", TokenName: "triage", Tool: "status", Transport: "stdio", Status: 200},
		{TokenID: "agt_1", TokenName: "triage", Tool: "backup_sets_list", Transport: "stdio", Status: 200},
		{TokenID: "agt_1", TokenName: "triage", Tool: "run_cycle", Transport: "stdio", Mutating: true, Status: 202},
		{TokenID: "agt_1", TokenName: "triage", Tool: "retry_backup", Transport: "stdio", Mutating: true, Status: 500},
	}
	for _, c := range calls {
		if err := r.svc.RecordAgentCall(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	deployment, _ := feedEvents(t, r.svc)
	acts := agentActions(deployment)
	if len(acts) != 2 {
		t.Fatalf("%d agent_action lines for two mutations and two reads", len(acts))
	}
	if acts[0].Result != "success" || acts[1].Result != "error" {
		t.Errorf("results = %q and %q, want success and error", acts[0].Result, acts[1].Result)
	}
	for _, a := range acts {
		got := map[string]string{}
		for _, f := range a.Fields {
			got[f.Key] = f.Value
		}
		if got["actor"] != "agent:triage" || got["token_id"] != "agt_1" || got["tool"] == "" {
			t.Errorf("fields = %v, want the actor, the token and the tool named", got)
		}
	}
	// The reads are still rows.
	if n := len(recordedRows(t, r.svc, AgentAuditQuery{})); n != 4 {
		t.Errorf("%d rows, want all four calls", n)
	}
}

// Falsify: blank BackupSetID, or put it on a set the configuration lacks.
func TestAnAgentActionAboutASetLandsOnThatSetsStrip(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	if err := r.svc.RecordAgentCall(ctx, AgentCall{
		TokenID: "agt_1", TokenName: "triage", Tool: "run_backup_set", Transport: "http",
		Mutating: true, Status: 202, BackupSetID: "alpha/nightly",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.RecordAgentCall(ctx, AgentCall{
		TokenID: "agt_1", TokenName: "triage", Tool: "run_backup_set", Transport: "http",
		Mutating: true, Status: 404, BackupSetID: "ghost/none",
	}); err != nil {
		t.Fatal(err)
	}
	deployment, set := feedEvents(t, r.svc)
	if n := len(agentActions(set)); n != 1 {
		t.Errorf("the set's strip has %d agent_action lines, want the one about it", n)
	}
	if n := len(agentActions(deployment)); n != 1 {
		t.Errorf("the deployment strip has %d, want the one about a set nobody configured", n)
	}
}

// Falsify: emit nothing for a confirmation state change, or emit it with no Result.
func TestAConfirmationChangingStateIsOnTheFeed(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	want := map[string]string{"pending": "info", "approved": "success", "denied": "warn", "expired": "warn"}
	for state := range want {
		if err := r.svc.RecordAgentCall(ctx, AgentCall{
			TokenID: "agt_1", TokenName: "triage", Tool: "run_cycle", Transport: "http",
			ConfirmationID: "cnf_1", ConfirmationState: state, Status: 409,
		}); err != nil {
			t.Fatal(err)
		}
	}
	deployment, _ := feedEvents(t, r.svc)
	got := map[string]string{}
	for _, a := range agentActions(deployment) {
		for _, f := range a.Fields {
			if f.Key == "state" {
				got[f.Value] = a.Result
			}
		}
	}
	for state, result := range want {
		if got[state] != result {
			t.Errorf("confirmation %s reached the feed with result %q, want %q", state, got[state], result)
		}
	}
}

// Falsify: let an unattributed refusal through every time.
func TestRefusalsThatNameNoTokenCannotFillTheFeed(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		if err := r.svc.RecordAgentRefusal(ctx, AgentRefusal{Method: "GET", Path: "/api/v1/x", Status: 401, Code: "AGENT_TOKEN_INVALID"}); err != nil {
			t.Fatal(err)
		}
	}
	deployment, _ := feedEvents(t, r.svc)
	if n := len(agentActions(deployment)); n != 1 {
		t.Errorf("200 refusals of a token that matched nothing put %d lines on the feed, want 1", n)
	}
	if n := len(recordedRows(t, r.svc, AgentAuditQuery{Limit: MaxAgentAuditLimit})); n != 200 {
		t.Errorf("the table kept %d of 200 refusals; the table keeps every one", n)
	}
}

// Falsify: store the arguments whole.
func TestStoredArgumentsAreBounded(t *testing.T) {
	r := newDurableRig(t)
	if err := r.svc.RecordAgentCall(context.Background(), AgentCall{
		TokenID: "agt_1", Tool: "status", Transport: "stdio", Arguments: map[string]string{"q": strings.Repeat("a", 64<<10)}, Status: 200,
	}); err != nil {
		t.Fatal(err)
	}
	rows := recordedRows(t, r.svc, AgentAuditQuery{})
	if len(rows) != 1 || len(rows[0].Arguments) > maxAgentAuditArguments+8 {
		t.Fatalf("the row keeps %d bytes of arguments", len(rows[0].Arguments))
	}
}

// Falsify: drop rows when the queue is full. A writer that cannot keep up
// slows the caller and loses nothing.
func TestAFullQueueSlowsTheCallerAndLosesNothing(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	const calls = agentAuditQueueSize*3 + 17
	for i := 0; i < calls; i++ {
		if err := r.svc.RecordAgentCall(ctx, AgentCall{TokenID: "agt_1", Tool: "status", Transport: "stdio", Status: 200}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	var total int
	cursor := ""
	for {
		rows, next, err := r.svc.ListAgentAudit(ctx, AgentAuditQuery{Limit: MaxAgentAuditLimit, Before: cursor})
		if err != nil {
			t.Fatal(err)
		}
		total += len(rows)
		if next == "" {
			break
		}
		cursor = next
	}
	if total != calls {
		t.Fatalf("%d rows for %d calls", total, calls)
	}
}

// Falsify: a context that has ended must not be written through; a closed
// writer reports it rather than dropping silently.
func TestARowAfterShutdownIsReportedNotDropped(t *testing.T) {
	r := newDurableRig(t)
	r.svc.cancel()
	<-r.svc.auditDone
	err := r.svc.RecordAgentCall(context.Background(), AgentCall{TokenID: "agt_1", Tool: "status", Transport: "stdio", Status: 200})
	if !errors.Is(err, ErrAgentAuditClosed) {
		t.Fatalf("err = %v, want ErrAgentAuditClosed", err)
	}
}

// Falsify: ignore the cursor, the token or the since.
func TestThePageFiltersByTokenAndTimeAndWalksBackByCursor(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	clock := pinAgentClock(t, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	for i := 0; i < 6; i++ {
		tok := "agt_a"
		if i%3 == 2 {
			tok = "agt_b"
		}
		*clock = clock.Add(24 * time.Hour)
		if err := r.svc.RecordAgentCall(ctx, AgentCall{TokenID: tok, Tool: fmt.Sprintf("t%d", i), Transport: "stdio", Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	if rows := recordedRows(t, r.svc, AgentAuditQuery{TokenID: "agt_b"}); len(rows) != 2 {
		t.Errorf("token filter: %d rows, want 2", len(rows))
	}
	// now is Nov 7 09:00; two and a half days back keeps Nov 5 and Nov 6 and Nov 7.
	if rows := recordedRows(t, r.svc, AgentAuditQuery{Since: "60h"}); len(rows) != 3 {
		t.Errorf("since 60h: %d rows, want 3", len(rows))
	}
	page1, next, err := r.svc.ListAgentAudit(ctx, AgentAuditQuery{Limit: 4})
	if err != nil || len(page1) != 4 || next == "" {
		t.Fatalf("page 1 = %d rows, cursor %q, %v", len(page1), next, err)
	}
	page2, next2, err := r.svc.ListAgentAudit(ctx, AgentAuditQuery{Limit: 4, Before: next})
	if err != nil || len(page2) != 2 || next2 != "" {
		t.Fatalf("page 2 = %d rows, cursor %q, %v", len(page2), next2, err)
	}
	if page1[0].Tool != "t5" || page2[1].Tool != "t0" {
		t.Errorf("order: %s ... %s, want newest first", page1[0].Tool, page2[1].Tool)
	}
	if _, _, err := r.svc.ListAgentAudit(ctx, AgentAuditQuery{Since: "banana"}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a since that is no time is %v, want ErrInvalidRequest", err)
	}
}

// Falsify: leave the prune out. Retention 0 empties the table, and a row
// inside the retention stays.
func TestRowsPastTheRetentionArePruned(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	clock := pinAgentClock(t, time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC))
	record := func() {
		t.Helper()
		if err := r.svc.RecordAgentCall(ctx, AgentCall{TokenID: "agt_1", Tool: "status", Transport: "stdio", Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	record()
	record()
	*clock = clock.Add(100 * 24 * time.Hour)
	record()
	r.svc.audit.now = func() time.Time { return *clock }
	n, err := r.svc.PruneAgentAudit(ctx)
	if err != nil || n != 2 {
		t.Fatalf("pruned %d (%v), want the two rows older than 90 days", n, err)
	}
	if rows := recordedRows(t, r.svc, AgentAuditQuery{}); len(rows) != 1 {
		t.Fatalf("%d rows left, want the young one", len(rows))
	}
	r.svc.SetAgentAuditRetentionDays(0)
	*clock = clock.Add(time.Minute)
	if _, err := r.svc.PruneAgentAudit(ctx); err != nil {
		t.Fatal(err)
	}
	if rows := recordedRows(t, r.svc, AgentAuditQuery{}); len(rows) != 0 {
		t.Fatalf("retention 0 left %d rows", len(rows))
	}
}

// Falsify: prune by age only. A refusal flood inside a day must still be bounded.
func TestTheTableIsBoundedByRowsAsWellAsByAge(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	r.svc.audit.maxRows = 50
	for i := 0; i < 80; i++ {
		if err := r.svc.RecordAgentRefusal(ctx, AgentRefusal{Method: "GET", Path: "/x", Status: 401, Code: "AGENT_TOKEN_INVALID"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.svc.PruneAgentAudit(ctx); err != nil {
		t.Fatal(err)
	}
	if rows := recordedRows(t, r.svc, AgentAuditQuery{Limit: MaxAgentAuditLimit}); len(rows) != 50 {
		t.Fatalf("%d rows after the prune, want the newest 50", len(rows))
	}
}

// Falsify: put the plaintext token, or anything of it, in a row.
func TestNoRowCarriesTheToken(t *testing.T) {
	r := newDurableRig(t)
	ctx := context.Background()
	minted := mintForTest(t, r.svc, MintAgentTokenRequest{Name: "triage"})
	if err := r.svc.RecordAgentCall(ctx, AgentCall{
		TokenID: minted.AgentToken.ID, TokenName: "triage", Tool: "status", Transport: "stdio", Status: 200,
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(recordedRows(t, r.svc, AgentAuditQuery{}))
	if strings.Contains(string(raw), minted.Token) {
		t.Error("an audit row carries the plaintext token")
	}
}
