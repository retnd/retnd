package service

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/state"
)

// U2.1 (#1349): the confirmation an agent's operate or configure call becomes
// (FR-95). Each test names the mutation that has to turn it red.

var confirmationClock = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

func runnerToken(t *testing.T, svc *BackupService, scope string) AgentPrincipal {
	t.Helper()
	minted := mintForTest(t, svc, MintAgentTokenRequest{Name: "runner", Scope: scope})
	p, err := svc.AuthenticateAgentToken(context.Background(), minted.Token)
	if err != nil {
		t.Fatalf("authenticate the minted token: %v", err)
	}
	return p
}

func askToRunACycle(p AgentPrincipal) CreateAgentConfirmationRequest {
	return CreateAgentConfirmationRequest{
		Principal: p, Scope: AgentScopeOperate, Tool: "run_cycle", Method: "POST", Route: "/operations",
		Body: []byte(`{"action":"run_cycle"}`), Command: "retnd run",
	}
}

// Falsify: execute on the tool call. The service has no handler to execute
// with, so the test asserts what an execution would have left behind.
func TestAnOperateCallCreatesAConfirmationAndRunsNothing(t *testing.T) {
	pinAgentClock(t, confirmationClock)
	svc := newRecordingService(t, config.Source{Name: "prod", BackupSets: []config.BackupSet{{Name: "db", ID: mustBackupSetID(t, "prod", "db")}}})
	p := runnerToken(t, svc, AgentScopeOperate)

	got, err := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "pending" || got.Tool != "run_cycle" || got.Command != "retnd run" || got.TokenName != "runner" {
		t.Errorf("confirmation = %+v", got)
	}
	if want := confirmationClock.Add(15 * time.Minute); !got.ExpiresAt.Equal(want) {
		t.Errorf("expires %s, want %s: the default is fifteen minutes", got.ExpiresAt, want)
	}
	if ops, err := svc.journal.ListOperations(context.Background(), 10); err != nil || len(ops) != 0 {
		t.Errorf("a pending confirmation started operations: %+v, %v", ops, err)
	}
	if got.Execution != nil {
		t.Errorf("a pending confirmation carries an execution: %+v", got.Execution)
	}

	live, err := svc.LiveActivity(context.Background(), LiveActivityRequest{})
	if err != nil || live.Deployment == nil {
		t.Fatalf("live activity = %+v, %v", live, err)
	}
	lines := live.Deployment.Events
	if len(lines) != 1 || lines[0].Event != "agent_action" {
		t.Fatalf("the feed has %+v; a pending confirmation is one agent_action and nothing else", lines)
	}
	fields := map[string]string{}
	for _, f := range lines[0].Fields {
		fields[f.Key] = f.Value
	}
	if fields["state"] != "pending" || fields["confirmation_id"] != got.ID || fields["tool"] != "run_cycle" || lines[0].Result == "" {
		t.Errorf("agent_action fields = %v, result %q", fields, lines[0].Result)
	}
}

func TestAConfirmationOnASetLandsOnThatSetsStrip(t *testing.T) {
	pinAgentClock(t, confirmationClock)
	svc := newRecordingService(t, config.Source{Name: "prod", BackupSets: []config.BackupSet{{Name: "db", ID: mustBackupSetID(t, "prod", "db")}}})
	p := runnerToken(t, svc, AgentScopeOperate)
	req := askToRunACycle(p)
	req.Tool, req.Route, req.BackupSet = "run_backup_set", "/operations", "prod/db"
	if _, err := svc.CreateAgentConfirmation(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	live, err := svc.LiveActivity(context.Background(), LiveActivityRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if strip := setNamed(t, live, "prod/db"); len(strip.Events) != 1 || strip.Events[0].Event != "agent_action" {
		t.Errorf("the set's strip = %+v, want the confirmation on it", strip.Events)
	}
	if len(live.Deployment.Events) != 0 {
		t.Errorf("the deployment's feed has it too, so it would be shown twice")
	}
}

func TestAReadTokenCreatesNoConfirmationAndAConfigureToolNeedsConfigure(t *testing.T) {
	pinAgentClock(t, confirmationClock)
	svc := newTestService(t)
	read := runnerToken(t, svc, AgentScopeRead)
	if _, err := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(read)); !errors.Is(err, ErrAgentScopeInsufficient) {
		t.Errorf("a read token = %v, want ErrAgentScopeInsufficient", err)
	}
	if rows, _ := svc.journal.ListAgentConfirmations(context.Background(), 10); len(rows) != 0 {
		t.Errorf("a refused call left %d rows", len(rows))
	}

	if err := svc.journal.InsertAgentToken(context.Background(), agentTokenRowForTest("agt_op2", "operator2", AgentScopeOperate)); err != nil {
		t.Fatal(err)
	}
	op := AgentPrincipal{TokenID: "agt_op2", Name: "operator2", Scope: AgentScopeOperate}
	req := askToRunACycle(op)
	req.Scope, req.Tool = AgentScopeConfigure, "backup_set_enabled"
	if _, err := svc.CreateAgentConfirmation(context.Background(), req); !errors.Is(err, ErrAgentScopeInsufficient) {
		t.Errorf("an operate token on a configure tool = %v, want ErrAgentScopeInsufficient", err)
	}
}

// Falsify: drop the cap.
func TestTheSixthPendingConfirmationIsRefused(t *testing.T) {
	pinAgentClock(t, confirmationClock)
	svc := newTestService(t)
	p := runnerToken(t, svc, AgentScopeOperate)
	for i := 0; i < 5; i++ {
		if _, err := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p)); err != nil {
			t.Fatalf("confirmation %d: %v", i+1, err)
		}
	}
	if _, err := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p)); !errors.Is(err, ErrAgentConfirmationLimit) {
		t.Fatalf("the sixth = %v, want ErrAgentConfirmationLimit", err)
	}
}

func TestTheLimitsAreTheConfiguredOnes(t *testing.T) {
	pinAgentClock(t, confirmationClock)
	cfg := testConfig()
	cfg.Agents = config.Agents{ConfirmationTTL: time.Minute, MaxPendingConfirmations: 1}
	svc := New(cfg, openTestJournal(t), nil, nil)
	p := runnerToken(t, svc, AgentScopeOperate)
	got, err := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p))
	if err != nil || !got.ExpiresAt.Equal(confirmationClock.Add(time.Minute)) {
		t.Fatalf("first = %+v, %v; want a one minute expiry", got, err)
	}
	if _, err := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p)); !errors.Is(err, ErrAgentConfirmationLimit) {
		t.Errorf("a second with a cap of one = %v", err)
	}
}

func TestAnEmptyOrOversizedRequestIsRefusedAtCreation(t *testing.T) {
	pinAgentClock(t, confirmationClock)
	svc := newTestService(t)
	p := runnerToken(t, svc, AgentScopeOperate)
	for name, mutate := range map[string]func(*CreateAgentConfirmationRequest){
		"a read method": func(r *CreateAgentConfirmationRequest) { r.Method = "GET" },
		"no route":      func(r *CreateAgentConfirmationRequest) { r.Route = "" },
		"no tool":       func(r *CreateAgentConfirmationRequest) { r.Tool = "" },
		"no command":    func(r *CreateAgentConfirmationRequest) { r.Command = "" },
		"a huge body":   func(r *CreateAgentConfirmationRequest) { r.Body = []byte(strings.Repeat("x", 70<<10)) },
	} {
		req := askToRunACycle(p)
		mutate(&req)
		if _, err := svc.CreateAgentConfirmation(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s = %v, want ErrInvalidRequest", name, err)
		}
	}
}

func TestApprovalHandsBackTheRecordByteForByte(t *testing.T) {
	pinAgentClock(t, confirmationClock)
	svc := newTestService(t)
	p := runnerToken(t, svc, AgentScopeOperate)
	req := askToRunACycle(p)
	req.Body = []byte("{ \"action\":\"run_cycle\" ,\n\"x\": 1}\n")
	made, err := svc.CreateAgentConfirmation(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	got, who, err := svc.ApproveAgentConfirmation(context.Background(), made.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Body) != string(req.Body) || got.State != "approved" || got.DecidedBy != "alice" {
		t.Errorf("approved = %+v", got)
	}
	if who.Name != "runner" || who.Scope != AgentScopeOperate || who.TokenID != p.TokenID {
		t.Errorf("replay principal = %+v, want the token that asked", who)
	}
	if _, _, err := svc.ApproveAgentConfirmation(context.Background(), made.ID, "bob"); !errors.Is(err, ErrAgentConfirmationNotPending) {
		t.Errorf("a second approval = %v, want ErrAgentConfirmationNotPending", err)
	}
}

// Falsify: skip the digest check.
func TestAStoredBodyThatChangedAfterCreationIsRefusedAtApproval(t *testing.T) {
	pinAgentClock(t, confirmationClock)
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	svc := New(testConfig(), j, nil, nil)
	p := runnerToken(t, svc, AgentScopeOperate)
	made, err := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p))
	if err != nil {
		t.Fatal(err)
	}
	// Somebody with the journal file changes the record behind the service's back.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.ExecContext(context.Background(),
		`UPDATE agent_confirmations SET body = ? WHERE id = ?`, []byte(`{"action":"run_backup_set"}`), made.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ApproveAgentConfirmation(context.Background(), made.ID, "alice"); !errors.Is(err, ErrAgentConfirmationBodySubstituted) {
		t.Fatalf("a tampered record = %v, want ErrAgentConfirmationBodySubstituted", err)
	}
	got, _ := svc.journal.GetAgentConfirmation(context.Background(), made.ID)
	if got.State != "pending" {
		t.Errorf("the refused approval left the confirmation %s; it stays pending so a person can deny it", got.State)
	}
}

// Falsify: approve one past expires_at.
func TestAnExpiredConfirmationIsRefusedOnApproveAndReadsExpired(t *testing.T) {
	clock := pinAgentClock(t, confirmationClock)
	svc := newRecordingService(t)
	p := runnerToken(t, svc, AgentScopeOperate)
	made, err := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p))
	if err != nil {
		t.Fatal(err)
	}
	*clock = confirmationClock.Add(15*time.Minute + time.Second)
	if _, _, err := svc.ApproveAgentConfirmation(context.Background(), made.ID, "alice"); !errors.Is(err, ErrAgentConfirmationExpired) {
		t.Fatalf("approving past the time = %v, want ErrAgentConfirmationExpired", err)
	}
	status, err := svc.AgentConfirmationStatus(context.Background(), p.TokenID, made.ID)
	if err != nil || status.State != "expired" {
		t.Errorf("status = %+v, %v; want expired", status, err)
	}
	if _, err := svc.DenyAgentConfirmation(context.Background(), made.ID, "alice"); !errors.Is(err, ErrAgentConfirmationExpired) {
		t.Errorf("denying an expired one = %v, want ErrAgentConfirmationExpired", err)
	}

	live, _ := svc.LiveActivity(context.Background(), LiveActivityRequest{})
	var states []string
	for _, e := range live.Deployment.Events {
		for _, f := range e.Fields {
			if f.Key == "state" {
				states = append(states, f.Value)
			}
		}
	}
	if strings.Join(states, ",") != "pending,expired" {
		t.Errorf("states on the feed = %v, want pending then expired exactly once each", states)
	}
}

func TestDenyEndsAConfirmationAndEmitsIt(t *testing.T) {
	pinAgentClock(t, confirmationClock)
	svc := newRecordingService(t)
	p := runnerToken(t, svc, AgentScopeOperate)
	made, err := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p))
	if err != nil {
		t.Fatal(err)
	}
	denied, err := svc.DenyAgentConfirmation(context.Background(), made.ID, "alice")
	if err != nil || denied.State != "denied" || denied.DecidedBy != "alice" {
		t.Fatalf("deny = %+v, %v", denied, err)
	}
	if _, _, err := svc.ApproveAgentConfirmation(context.Background(), made.ID, "alice"); !errors.Is(err, ErrAgentConfirmationNotPending) {
		t.Errorf("approving a denied one = %v, want ErrAgentConfirmationNotPending", err)
	}
	if _, err := svc.DenyAgentConfirmation(context.Background(), "cnf_nope", "alice"); !errors.Is(err, ErrAgentConfirmationNotFound) {
		t.Errorf("denying an unknown id = %v", err)
	}
	live, _ := svc.LiveActivity(context.Background(), LiveActivityRequest{})
	var last LiveActivityEvent
	for _, e := range live.Deployment.Events {
		last = e
	}
	fields := map[string]string{}
	for _, f := range last.Fields {
		fields[f.Key] = f.Value
	}
	if fields["state"] != "denied" || fields["confirmation_id"] != made.ID || last.Result != "warn" {
		t.Errorf("the denial on the feed = %v, result %q", fields, last.Result)
	}
}

func TestApprovalOfAConfirmationWhoseTokenWasRevokedIsRefused(t *testing.T) {
	pinAgentClock(t, confirmationClock)
	svc := newTestService(t)
	p := runnerToken(t, svc, AgentScopeOperate)
	made, err := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RevokeAgentToken(context.Background(), p.TokenID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ApproveAgentConfirmation(context.Background(), made.ID, "alice"); !errors.Is(err, ErrAgentConfirmationTokenEnded) {
		t.Fatalf("a revoked token's confirmation = %v, want ErrAgentConfirmationTokenEnded", err)
	}
}

func TestAnApprovalRecordsItsExecutionAndTheStatusToolReadsItBack(t *testing.T) {
	pinAgentClock(t, confirmationClock)
	svc := newRecordingService(t)
	p := runnerToken(t, svc, AgentScopeOperate)
	made, err := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p))
	if err != nil {
		t.Fatal(err)
	}
	approved, _, err := svc.ApproveAgentConfirmation(context.Background(), made.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	exec := AgentExecution{Status: 403, Code: "DESTRUCTIVE_OPERATIONS_DISABLED", Message: "refused over HTTP", CorrelationID: "cid_abc"}
	if err := svc.FinishAgentApproval(context.Background(), approved, exec); err != nil {
		t.Fatal(err)
	}

	got, err := svc.AgentConfirmationStatus(context.Background(), p.TokenID, made.ID)
	if err != nil || got.State != "approved" || got.Execution == nil {
		t.Fatalf("status = %+v, %v", got, err)
	}
	want := AgentExecution{Status: 403, Code: "DESTRUCTIVE_OPERATIONS_DISABLED", Message: "refused over HTTP", CorrelationID: "cid_abc", ExitCode: 50}
	if *got.Execution != want {
		t.Errorf("execution = %+v, want %+v: the shut gate is exit 50", *got.Execution, want)
	}
}

func TestAnExecutionExitCodeIsTheCLIs(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		want   int
	}{
		{202, "", 0},
		{200, "", 0},
		{403, "DESTRUCTIVE_OPERATIONS_DISABLED", 50},
		{403, "INSECURE_TRANSPORT_ACK_REQUIRED", 52},
		{404, "BACKUP_SET_NOT_FOUND", 1},
		{500, "INTERNAL", 1},
	} {
		if got := AgentExecutionExitCode(tc.status, tc.code); got != tc.want {
			t.Errorf("AgentExecutionExitCode(%d, %q) = %d, want %d", tc.status, tc.code, got, tc.want)
		}
	}
}

func TestATokenReadsOnlyItsOwnConfirmations(t *testing.T) {
	pinAgentClock(t, confirmationClock)
	svc := newTestService(t)
	p := runnerToken(t, svc, AgentScopeOperate)
	made, err := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.journal.InsertAgentToken(context.Background(), agentTokenRowForTest("agt_other", "other", AgentScopeOperate)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AgentConfirmationStatus(context.Background(), "agt_other", made.ID); !errors.Is(err, ErrAgentConfirmationNotFound) {
		t.Errorf("another token reading it = %v, want not found, so ids cannot be probed", err)
	}
}

func TestTheListIsNewestFirstAndSweepsExpiredOnes(t *testing.T) {
	clock := pinAgentClock(t, confirmationClock)
	svc := newTestService(t)
	p := runnerToken(t, svc, AgentScopeOperate)
	first, _ := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p))
	*clock = confirmationClock.Add(20 * time.Minute)
	second, _ := svc.CreateAgentConfirmation(context.Background(), askToRunACycle(p))
	list, err := svc.ListAgentConfirmations(context.Background())
	if err != nil || len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list[0].State != "pending" || list[1].State != "expired" {
		t.Errorf("states = %s, %s; the old one expired when it was read", list[0].State, list[1].State)
	}
}

func agentTokenRowForTest(id, name, scope string) state.AgentToken {
	return state.AgentToken{
		ID: id, Name: name, Prefix: "rat_abcd", Hash: strings.Repeat("b", 64), Scope: scope, Owner: "alice",
		CreatedAt: confirmationClock, ExpiresAt: confirmationClock.Add(90 * 24 * time.Hour),
	}
}

// Falsify: leave an approved confirmation with no result silent after a
// restart, replay it, or touch one that has a result or was decided after the
// process started (Q21).
func TestAnApprovalThatNeverFinishedIsMarkedInterruptedAtStartAndNeverRerun(t *testing.T) {
	clock := pinAgentClock(t, confirmationClock)
	svc := newRecordingService(t)
	ctx := context.Background()
	p := runnerToken(t, svc, AgentScopeOperate)

	approve := func() AgentConfirmation {
		made, err := svc.CreateAgentConfirmation(ctx, askToRunACycle(p))
		if err != nil {
			t.Fatal(err)
		}
		got, _, err := svc.ApproveAgentConfirmation(ctx, made.ID, "alice")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	lost := approve()
	finished := approve()
	if err := svc.FinishAgentApproval(ctx, finished, AgentExecution{Status: 202, Message: "started"}); err != nil {
		t.Fatal(err)
	}

	*clock = confirmationClock.Add(time.Hour)
	started := *clock
	*clock = started.Add(time.Minute)
	late := approve()

	n, err := svc.ReconcileAgentConfirmations(ctx, started)
	if err != nil || n != 1 {
		t.Fatalf("reconcile = %d, %v; want exactly the one approval with no result", n, err)
	}
	got, err := svc.AgentConfirmationStatus(ctx, p.TokenID, lost.ID)
	if err != nil || got.Execution == nil {
		t.Fatalf("status = %+v, %v", got, err)
	}
	if e := got.Execution; e.Outcome != AgentOutcomeInterrupted || e.ExitCode != 1 ||
		e.Message != "the engine stopped before the replay finished; check the activity feed for this command before running it again" {
		t.Errorf("execution = %+v", *e)
	}
	if c := got.Contract().Execution; c == nil || c.Outcome != "interrupted" {
		t.Errorf("the wire shows %+v, want outcome interrupted", c)
	}
	if got, _ := svc.AgentConfirmationStatus(ctx, p.TokenID, finished.ID); got.Execution == nil || got.Execution.Outcome == AgentOutcomeInterrupted {
		t.Errorf("a replay that finished was marked interrupted: %+v", got.Execution)
	}
	if got, _ := svc.AgentConfirmationStatus(ctx, p.TokenID, late.ID); got.Execution != nil {
		t.Errorf("an approval decided after the start was marked: %+v", got.Execution)
	}

	live, _ := svc.LiveActivity(ctx, LiveActivityRequest{})
	var lines int
	for _, e := range agentActions(live.Deployment.Events) {
		for _, f := range e.Fields {
			if f.Key == "state" && f.Value == "interrupted" {
				lines++
				if e.Result != "warn" {
					t.Errorf("the interrupted line has result %q", e.Result)
				}
			}
		}
	}
	if lines != 1 {
		t.Errorf("%d interrupted lines on the feed, want one", lines)
	}
	if n, err := svc.ReconcileAgentConfirmations(ctx, started); err != nil || n != 0 {
		t.Errorf("a second reconcile = %d, %v; the row is written once", n, err)
	}
}
