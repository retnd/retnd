package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

// U2.1 (#1349): the rows behind the confirmations an agent's operate and
// configure calls become. Written red first; each test names the mutation
// that has to turn it red.

var confirmationEpoch = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

func confirmationForTest(id, tokenID string) AgentConfirmation {
	return AgentConfirmation{
		ID: id, TokenID: tokenID, Tool: "run_cycle", Route: "/operations", Method: "POST",
		Body: []byte(`{"action":"run_cycle"}`), Command: "retnd run", BackupSet: "",
		State: AgentConfirmationPending, CreatedAt: confirmationEpoch, ExpiresAt: confirmationEpoch.Add(15 * time.Minute),
	}
}

func pendingRecord(c AgentConfirmation) AgentAudit {
	return AgentAudit{At: c.CreatedAt, TokenID: c.TokenID, Tool: c.Tool, Transport: "confirmation", Arguments: "sha256:abc", Status: 409, Code: "AGENT_CONFIRMATION_REQUIRED", ConfirmationID: c.ID}
}

func journalWithToken(t *testing.T) *Journal {
	t.Helper()
	j, _ := openJournal(t)
	if err := j.InsertAgentToken(context.Background(), agentTokenForTest("agt_1", "runner")); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestAConfirmationKeepsItsBodyByteForByte(t *testing.T) {
	ctx := context.Background()
	j := journalWithToken(t)
	c := confirmationForTest("cnf_1", "agt_1")
	// Whitespace, key order and a trailing newline are all part of the record.
	c.Body = []byte("{ \"action\" :\"run_cycle\",\n \"z\":1 }\n")
	if err := j.InsertAgentConfirmation(ctx, c, 5, pendingRecord(c)); err != nil {
		t.Fatal(err)
	}
	got, err := j.GetAgentConfirmation(ctx, "cnf_1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Body) != string(c.Body) {
		t.Errorf("body = %q, want %q", got.Body, c.Body)
	}
	if got.State != AgentConfirmationPending || got.DecidedAt != nil || got.DecidedBy != "" || !got.ExpiresAt.Equal(c.ExpiresAt) {
		t.Errorf("round trip = %+v", got)
	}
	rows, err := j.AgentAuditForConfirmation(ctx, "cnf_1")
	if err != nil || len(rows) != 1 || rows[0].Transport != "confirmation" {
		t.Errorf("the pending record was written with the confirmation: %+v, %v", rows, err)
	}
}

// Falsify: drop the cap.
func TestTheSixthPendingConfirmationIsRefused(t *testing.T) {
	ctx := context.Background()
	j := journalWithToken(t)
	for i := 0; i < 5; i++ {
		c := confirmationForTest("cnf_"+string(rune('a'+i)), "agt_1")
		if err := j.InsertAgentConfirmation(ctx, c, 5, pendingRecord(c)); err != nil {
			t.Fatalf("confirmation %d: %v", i+1, err)
		}
	}
	sixth := confirmationForTest("cnf_f", "agt_1")
	if err := j.InsertAgentConfirmation(ctx, sixth, 5, pendingRecord(sixth)); !errors.Is(err, ErrAgentConfirmationLimit) {
		t.Fatalf("the sixth = %v, want ErrAgentConfirmationLimit", err)
	}
	if _, err := j.GetAgentConfirmation(ctx, "cnf_f"); !errors.Is(err, ErrAgentConfirmationNotFound) {
		t.Errorf("a refused confirmation left a row: %v", err)
	}
	if rows, _ := j.AgentAuditForConfirmation(ctx, "cnf_f"); len(rows) != 0 {
		t.Errorf("a refused confirmation left an audit row: %+v", rows)
	}
}

func TestTheCapCountsOnlyOpenConfirmationsOfThatToken(t *testing.T) {
	ctx := context.Background()
	j := journalWithToken(t)
	if err := j.InsertAgentToken(ctx, agentTokenForTest("agt_2", "other")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		c := confirmationForTest("cnf_"+string(rune('a'+i)), "agt_1")
		if err := j.InsertAgentConfirmation(ctx, c, 5, pendingRecord(c)); err != nil {
			t.Fatal(err)
		}
	}
	// Another token has its own five.
	other := confirmationForTest("cnf_other", "agt_2")
	if err := j.InsertAgentConfirmation(ctx, other, 5, pendingRecord(other)); err != nil {
		t.Errorf("another token's first confirmation: %v", err)
	}
	// One denied frees a place.
	if ok, err := j.DecideAgentConfirmation(ctx, "cnf_a", AgentConfirmationDenied, "alice", confirmationEpoch.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("deny = %v, %v", ok, err)
	}
	again := confirmationForTest("cnf_again", "agt_1")
	if err := j.InsertAgentConfirmation(ctx, again, 5, pendingRecord(again)); err != nil {
		t.Errorf("after a denial: %v", err)
	}
	// One whose time has passed frees a place without anyone sweeping it.
	late := confirmationForTest("cnf_late", "agt_1")
	late.CreatedAt = confirmationEpoch.Add(16 * time.Minute)
	late.ExpiresAt = late.CreatedAt.Add(15 * time.Minute)
	if err := j.InsertAgentConfirmation(ctx, late, 5, pendingRecord(late)); err != nil {
		t.Errorf("past the first batch's expiry the old ones no longer count: %v", err)
	}
}

func TestADecisionIsMadeOnceAndOnlyOnAPendingConfirmation(t *testing.T) {
	ctx := context.Background()
	j := journalWithToken(t)
	c := confirmationForTest("cnf_1", "agt_1")
	if err := j.InsertAgentConfirmation(ctx, c, 5, pendingRecord(c)); err != nil {
		t.Fatal(err)
	}
	at := confirmationEpoch.Add(time.Minute)
	if ok, err := j.DecideAgentConfirmation(ctx, "cnf_1", AgentConfirmationApproved, "alice", at); err != nil || !ok {
		t.Fatalf("approve = %v, %v", ok, err)
	}
	if ok, err := j.DecideAgentConfirmation(ctx, "cnf_1", AgentConfirmationDenied, "bob", at.Add(time.Minute)); err != nil || ok {
		t.Errorf("a second decision = %v, %v; want it refused", ok, err)
	}
	got, _ := j.GetAgentConfirmation(ctx, "cnf_1")
	if got.State != AgentConfirmationApproved || got.DecidedBy != "alice" || got.DecidedAt == nil || !got.DecidedAt.Equal(at) {
		t.Errorf("after two decisions = %+v, want the first to stand", got)
	}
}

func TestExpiringMarksOnlyThePendingOnesPastTheirTime(t *testing.T) {
	ctx := context.Background()
	j := journalWithToken(t)
	for _, id := range []string{"cnf_1", "cnf_2"} {
		c := confirmationForTest(id, "agt_1")
		if err := j.InsertAgentConfirmation(ctx, c, 5, pendingRecord(c)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.DecideAgentConfirmation(ctx, "cnf_2", AgentConfirmationDenied, "alice", confirmationEpoch); err != nil {
		t.Fatal(err)
	}
	if got, err := j.ExpireAgentConfirmations(ctx, confirmationEpoch.Add(14*time.Minute)); err != nil || len(got) != 0 {
		t.Fatalf("inside the window = %+v, %v; want none", got, err)
	}
	got, err := j.ExpireAgentConfirmations(ctx, confirmationEpoch.Add(15*time.Minute))
	if err != nil || len(got) != 1 || got[0].ID != "cnf_1" || got[0].State != AgentConfirmationExpired {
		t.Fatalf("at the deadline = %+v, %v; want cnf_1 expired", got, err)
	}
	if again, _ := j.ExpireAgentConfirmations(ctx, confirmationEpoch.Add(time.Hour)); len(again) != 0 {
		t.Errorf("expiring twice reported %+v", again)
	}
	denied, _ := j.GetAgentConfirmation(ctx, "cnf_2")
	if denied.State != AgentConfirmationDenied {
		t.Errorf("a denied confirmation became %s", denied.State)
	}
}

func TestListingIsNewestFirst(t *testing.T) {
	ctx := context.Background()
	j := journalWithToken(t)
	for i, id := range []string{"cnf_1", "cnf_2", "cnf_3"} {
		c := confirmationForTest(id, "agt_1")
		c.CreatedAt = confirmationEpoch.Add(time.Duration(i) * time.Second)
		c.ExpiresAt = c.CreatedAt.Add(15 * time.Minute)
		if err := j.InsertAgentConfirmation(ctx, c, 5, pendingRecord(c)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := j.ListAgentConfirmations(ctx, 2)
	if err != nil || len(got) != 2 || got[0].ID != "cnf_3" || got[1].ID != "cnf_2" {
		t.Errorf("list = %+v, %v", got, err)
	}
}

func TestAnAgentTokenCanBeReadByID(t *testing.T) {
	ctx := context.Background()
	j := journalWithToken(t)
	got, err := j.GetAgentToken(ctx, "agt_1")
	if err != nil || got.Name != "runner" {
		t.Errorf("get = %+v, %v", got, err)
	}
	if _, err := j.GetAgentToken(ctx, "agt_nope"); !errors.Is(err, ErrAgentTokenNotFound) {
		t.Errorf("unknown id = %v, want ErrAgentTokenNotFound", err)
	}
}
