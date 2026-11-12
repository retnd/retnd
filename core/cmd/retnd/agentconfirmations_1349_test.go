package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/service"
)

// `retnd agent confirmations list|approve|deny` (#1349). Each runs over the
// doors it has: with nothing serving (list and deny, which read and end a
// record) and attached to the fake engine (all three, approve included).

// askToRun records one confirmation on svc the way a tool call would, for a
// token minted here.
func askToRun(t *testing.T, svc *service.BackupService, tool, route, body string) service.AgentConfirmation {
	t.Helper()
	ctx := context.Background()
	minted, err := svc.MintAgentToken(ctx, service.MintAgentTokenRequest{Name: "runner", Scope: service.AgentScopeOperate, Owner: "alice"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	principal, err := svc.AuthenticateAgentToken(ctx, minted.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	c, err := svc.CreateAgentConfirmation(ctx, service.CreateAgentConfirmationRequest{
		Principal: principal, Scope: service.AgentScopeOperate, Tool: tool, Method: "POST", Route: route,
		Body: []byte(body), Command: "retnd run",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return c
}

func openSeedService(t *testing.T, configPath string) (*service.BackupService, func()) {
	t.Helper()
	svc, closeFn, err := service.Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return svc, func() { _ = closeFn() }
}

func listedConfirmations(t *testing.T, configPath string) apicontract.ListAgentConfirmationsResponse {
	t.Helper()
	code, raw, stderr := runCLI(t, "agent", "confirmations", "list", "--json", "--config", configPath)
	if code != exitOK {
		t.Fatalf("list --json exit %d\n%s", code, stderr)
	}
	var got apicontract.ListAgentConfirmationsResponse
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("list --json = %s (%v)", raw, err)
	}
	return got
}

func TestListShowsEachConfirmationWithItsCommandOnBothDoors(t *testing.T) {
	t.Run("direct", func(t *testing.T) {
		configPath := writeTestConfig(t)
		svc, closeSvc := openSeedService(t, configPath)
		c := askToRun(t, svc, "run_cycle", "/operations", `{"action":"run_cycle"}`)
		closeSvc()
		assertListed(t, configPath, c)
	})
	t.Run("through the engine", func(t *testing.T) {
		configPath := writeTestConfig(t)
		e := startFakeEngineFor(t, configPath)
		e.attach(t)
		c := askToRun(t, e.svc, "run_cycle", "/operations", `{"action":"run_cycle"}`)
		assertListed(t, configPath, c)
	})
}

func assertListed(t *testing.T, configPath string, c service.AgentConfirmation) {
	t.Helper()
	code, text, stderr := runCLI(t, "agent", "confirmations", "list", "--config", configPath)
	if code != exitOK {
		t.Fatalf("list exit %d\n%s", code, stderr)
	}
	for _, want := range []string{c.ID, "pending", "runner", "run_cycle", "retnd run"} {
		if !strings.Contains(text, want) {
			t.Errorf("the list lacks %q:\n%s", want, text)
		}
	}
	got := listedConfirmations(t, configPath)
	if len(got.Confirmations) != 1 || got.Confirmations[0].ID != c.ID || got.Confirmations[0].State != "pending" {
		t.Errorf("--json = %+v", got)
	}
	if strings.Contains(text, "action") {
		t.Errorf("the list shows the recorded body:\n%s", text)
	}
}

func TestShowReadsOneConfirmationOnBothDoorsIncludingHowItEnded(t *testing.T) {
	for name, door := range map[string]func(t *testing.T) (string, *service.BackupService, func()){
		"direct": func(t *testing.T) (string, *service.BackupService, func()) {
			configPath := writeTestConfig(t)
			svc, closeSvc := openSeedService(t, configPath)
			return configPath, svc, closeSvc
		},
		"through the engine": func(t *testing.T) (string, *service.BackupService, func()) {
			configPath := writeTestConfig(t)
			e := startFakeEngineFor(t, configPath)
			e.attach(t)
			return configPath, e.svc, func() {}
		},
	} {
		t.Run(name, func(t *testing.T) {
			configPath, svc, done := door(t)
			c := askToRun(t, svc, "run_cycle", "/operations", `{"action":"run_cycle"}`)
			approved, _, err := svc.ApproveAgentConfirmation(context.Background(), c.ID, "alice")
			if err != nil {
				t.Fatal(err)
			}
			exec := service.AgentExecution{Status: 403, Code: "DESTRUCTIVE_OPERATIONS_DISABLED", Message: "refused over HTTP", CorrelationID: "cid_1"}
			if err := svc.FinishAgentApproval(context.Background(), approved, exec); err != nil {
				t.Fatal(err)
			}
			done()

			code, text, stderr := runCLI(t, "agent", "confirmations", "show", c.ID, "--config", configPath)
			if code != exitOK || !strings.Contains(text, "approved") || !strings.Contains(text, "DESTRUCTIVE_OPERATIONS_DISABLED") || !strings.Contains(text, "exit code 50") {
				t.Fatalf("show exit %d\n%s\n%s", code, text, stderr)
			}
			code, raw, _ := runCLI(t, "agent", "confirmations", "show", c.ID, "--json", "--config", configPath)
			var got apicontract.AgentConfirmation
			if err := json.Unmarshal([]byte(raw), &got); code != exitOK || err != nil || got.Execution == nil || got.Execution.ExitCode != 50 {
				t.Errorf("show --json = %s (%v)", raw, err)
			}
		})
	}
}

func TestAnEmptyListSaysSo(t *testing.T) {
	code, stdout, _ := runCLI(t, "agent", "confirmations", "list", "--config", writeTestConfig(t))
	if code != exitOK || !strings.Contains(stdout, "no agent confirmations") {
		t.Errorf("exit %d, %q", code, stdout)
	}
}

// Falsify: bypass the gate on replay (the engine's, so this is the parity
// check: the verb exits with what retnd run exits with).
func TestApprovingARunCycleExitsWithTheShutGatesCode(t *testing.T) {
	configPath := writeTestConfig(t)
	e := startFakeEngineFor(t, configPath)
	e.attach(t)
	c := askToRun(t, e.svc, "run_cycle", "/operations", `{"action":"run_cycle"}`)

	code, stdout, stderr := runCLI(t, "agent", "confirmations", "approve", c.ID, "--config", configPath)
	if code != exitEngineGateShut {
		t.Fatalf("exit %d, want %d, the code retnd run gives a shut gate\n%s\n%s", code, exitEngineGateShut, stdout, stderr)
	}
	if !strings.Contains(stderr, "DESTRUCTIVE_OPERATIONS_DISABLED") || !strings.Contains(stderr, "approval stands") {
		t.Errorf("stderr does not name the refusal and say the approval stands:\n%s", stderr)
	}
	got := listedConfirmations(t, configPath)
	if got.Confirmations[0].State != "approved" || got.Confirmations[0].Execution == nil || got.Confirmations[0].Execution.ExitCode != 50 {
		t.Errorf("after the approval: %+v", got.Confirmations[0])
	}
	var posted bool
	for _, r := range e.requests() {
		posted = posted || r == "POST /agent/confirmations/"+c.ID+"/approve"
	}
	if !posted {
		t.Errorf("approve never reached the engine's route: %v", e.requests())
	}
}

func TestApprovingSomethingThatRunsExitsZeroAndSaysSo(t *testing.T) {
	configPath := writeTestConfig(t)
	e := startFakeEngineFor(t, configPath)
	e.attach(t)
	c := askToRun(t, e.svc, "backup_set_enabled", "/backup-sets/production/postgres/enabled", `{"enabled":false}`)
	code, stdout, stderr := runCLI(t, "agent", "confirmations", "approve", c.ID, "--json", "--config", configPath)
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	var got apicontract.AgentConfirmation
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || got.State != "approved" || got.Execution == nil || got.Execution.Status != 200 || got.Execution.Code != "" {
		t.Errorf("--json = %s (%v)", stdout, err)
	}
}

// Falsify: let approve run with no engine.
func TestApprovingWithNoEngineServingSaysSoAndLeavesItPending(t *testing.T) {
	configPath := writeTestConfig(t)
	svc, closeSvc := openSeedService(t, configPath)
	c := askToRun(t, svc, "run_cycle", "/operations", `{"action":"run_cycle"}`)
	closeSvc()

	code, _, stderr := runCLI(t, "agent", "confirmations", "approve", c.ID, "--config", configPath)
	if code != exitFailure || !strings.Contains(stderr, "no engine is serving") {
		t.Errorf("exit %d, stderr %q; want a refusal that says an engine is needed", code, stderr)
	}
	if got := listedConfirmations(t, configPath); got.Confirmations[0].State != "pending" {
		t.Errorf("a refused approval left it %s", got.Confirmations[0].State)
	}
}

func TestDenyEndsAConfirmationOnBothDoors(t *testing.T) {
	bothSeeded := map[string]func(t *testing.T) (string, string){
		"direct": func(t *testing.T) (string, string) {
			configPath := writeTestConfig(t)
			svc, closeSvc := openSeedService(t, configPath)
			defer closeSvc()
			return configPath, askToRun(t, svc, "run_cycle", "/operations", `{}`).ID
		},
		"through the engine": func(t *testing.T) (string, string) {
			configPath := writeTestConfig(t)
			e := startFakeEngineFor(t, configPath)
			e.attach(t)
			return configPath, askToRun(t, e.svc, "run_cycle", "/operations", `{}`).ID
		},
	}
	for name, seed := range bothSeeded {
		t.Run(name, func(t *testing.T) {
			configPath, id := seed(t)
			code, stdout, stderr := runCLI(t, "agent", "confirmations", "deny", id, "--config", configPath)
			if code != exitOK || !strings.Contains(stdout, "nothing was run") {
				t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
			}
			if got := listedConfirmations(t, configPath); got.Confirmations[0].State != "denied" || got.Confirmations[0].DecidedBy == nil {
				t.Errorf("after the denial: %+v", got.Confirmations[0])
			}
			if code, _, stderr := runCLI(t, "agent", "confirmations", "deny", id, "--config", configPath); code != exitFailure || !strings.Contains(stderr, "already decided") {
				t.Errorf("denying twice = exit %d, %q", code, stderr)
			}
		})
	}
}

func TestTheConfirmationVerbsRefuseTheWayTheServiceDoes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"no subcommand", []string{"agent", "confirmations"}, exitUsage},
		{"unknown subcommand", []string{"agent", "confirmations", "expire"}, exitUsage},
		{"show with no id", []string{"agent", "confirmations", "show"}, exitUsage},
		{"show an unknown id", []string{"agent", "confirmations", "show", "cnf_nope"}, exitFailure},
		{"approve with no id", []string{"agent", "confirmations", "approve"}, exitUsage},
		{"deny with two ids", []string{"agent", "confirmations", "deny", "a", "b"}, exitUsage},
		{"list with an operand", []string{"agent", "confirmations", "list", "x"}, exitUsage},
		{"deny an unknown id", []string{"agent", "confirmations", "deny", "cnf_nope"}, exitFailure},
		{"approve an unknown id", []string{"agent", "confirmations", "approve", "cnf_nope"}, exitFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(tc.args, "--config", writeTestConfig(t))
			if code, stdout, stderr := runCLI(t, args...); code != tc.code {
				t.Errorf("exit %d, want %d\n%s\n%s", code, tc.code, stdout, stderr)
			}
		})
	}
}

// Falsify: map one refusal to a different exit code in the service's table.
func TestAnExecutionsExitCodeIsTheOneEngineRefusalExitGives(t *testing.T) {
	for _, code := range []apicontract.ErrorCode{
		apicontract.ErrorCodeDestructiveOperationsDisabled,
		apicontract.ErrorCodeInsecureTransportAckRequired,
		apicontract.ErrorCodeBackupSetNotFound,
		apicontract.ErrorCodeInvalidRequest,
		apicontract.ErrorCodeInternal,
		apicontract.ErrorCodeAgentRouteDenied,
		// The confirmation refusals are all exit 1 (Q20).
		apicontract.ErrorCodeAgentConfirmationNotFound,
		apicontract.ErrorCodeAgentConfirmationDecided,
		apicontract.ErrorCodeAgentConfirmationTokenEnded,
	} {
		want, _ := engineRefusalExit(&apiclient.Error{Code: code, Status: 403})
		if got := service.AgentExecutionExitCode(403, string(code)); got != want {
			t.Errorf("%s: the service says exit %d, the CLI exits %d", code, got, want)
		}
	}
	for code, status := range map[apicontract.ErrorCode]int{
		apicontract.ErrorCodeAgentConfirmationNotFound:   404,
		apicontract.ErrorCodeAgentConfirmationDecided:    409,
		apicontract.ErrorCodeAgentConfirmationTokenEnded: 409,
	} {
		if exit, _ := engineRefusalExit(&apiclient.Error{Code: code, Status: status}); exit != exitFailure {
			t.Errorf("%s exits %d, want 1", code, exit)
		}
	}
	if service.AgentExecutionExitCode(500, "") != 1 {
		t.Errorf("a bare 500 is not exit 1")
	}
}
