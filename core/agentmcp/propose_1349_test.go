package agentmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/agenttools"
)

// U2.1 (#1349): the tools that propose a change. Each test names the mutation
// that has to turn it red.

type recordingCaller struct {
	calls []Call
	err   error
	resp  Response
}

func (c *recordingCaller) Call(_ context.Context, call Call) (Response, error) {
	c.calls = append(c.calls, call)
	return c.resp, c.err
}

var runCycle = agenttools.Row{Tool: "run_cycle", Method: "POST", Route: "/operations", Scope: agenttools.ScopeOperate, Action: "run_cycle", Description: "Propose running one backup cycle."}

var setEnabled = agenttools.Row{Tool: "backup_set_enabled", Method: "POST", Route: "/backup-sets/{source}/{set}/enabled", Scope: agenttools.ScopeConfigure, Description: "Propose enabling or disabling a set."}

func proposalServer(c Caller, tools ...agenttools.Row) *server {
	s := &server{cfg: Config{Caller: c, Tools: tools}, tools: map[string]agenttools.Row{}}
	for _, t := range tools {
		s.tools[t.Tool] = t
	}
	return s
}

// Falsify: report a proposal as a success.
func TestAProposalIsAnErrorResultNamingTheConfirmationNeverASuccess(t *testing.T) {
	caller := &recordingCaller{err: &Refusal{
		Status: 409, Code: "AGENT_CONFIRMATION_REQUIRED", Message: "a person has to confirm this", CorrelationID: "cid_1",
		ConfirmationID: "cnf_1", ConfirmationExpires: "2026-11-11T09:15:00Z",
	}}
	res := proposalServer(caller, runCycle).run(context.Background(), runCycle, nil)
	if res.OK || res.Code != "AGENT_CONFIRMATION_REQUIRED" || res.ConfirmationID != "cnf_1" || res.ExpiresAt != "2026-11-11T09:15:00Z" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.HasPrefix(res.Command, "retnd") || res.CorrelationID != "cid_1" || res.ExitCode != ExitFailure {
		t.Errorf("command %q, correlation %q, exit %d", res.Command, res.CorrelationID, res.ExitCode)
	}
	if got := toToolResult("run_cycle", res); !got.IsError {
		t.Errorf("the tool result is not an error result")
	}
	if len(caller.calls) != 1 || caller.calls[0].Tool != "run_cycle" || caller.calls[0].Method != "POST" || string(caller.calls[0].Body) != `{"action":"run_cycle"}` {
		t.Errorf("call = %+v, want the operations request with its action", caller.calls)
	}
}

func TestAnActionToolRefusesABodyThatNamesAnotherAction(t *testing.T) {
	caller := &recordingCaller{}
	res := proposalServer(caller, runCycle).run(context.Background(), runCycle, json.RawMessage(`{"body":{"action":"restore_snapshot"}}`))
	if res.OK || res.Code != CodeInvalidRequest || res.ExitCode != ExitUsage || len(caller.calls) != 0 {
		t.Errorf("result = %+v, calls %d; the tool must not send another act", res, len(caller.calls))
	}
}

func TestAProposalSendsTheBodyAndThePlaceholdersItWasGiven(t *testing.T) {
	caller := &recordingCaller{err: &Refusal{Status: 409, Code: "AGENT_CONFIRMATION_REQUIRED", ConfirmationID: "cnf_2"}}
	raw := json.RawMessage(`{"source":"production","set":"postgres","body":{"enabled":false}}`)
	res := proposalServer(caller, setEnabled).run(context.Background(), setEnabled, raw)
	if res.ConfirmationID != "cnf_2" {
		t.Fatalf("result = %+v", res)
	}
	got := caller.calls[0]
	if strings.Join(got.PathArgs, "/") != "production/postgres" || string(got.Body) != `{"enabled":false}` {
		t.Errorf("call = %+v", got)
	}
	if !strings.Contains(res.Command, "production/postgres") {
		t.Errorf("command %q does not name the set", res.Command)
	}
}

func TestAProposalWithoutItsPlaceholdersOrWithStrayArgumentsSendsNothing(t *testing.T) {
	for name, raw := range map[string]string{
		"no placeholders":   `{"body":{"enabled":true}}`,
		"an empty one":      `{"source":"","set":"x"}`,
		"a stray argument":  `{"source":"a","set":"b","force":true}`,
		"a body that isn't": `{"source":"a","set":"b","body":"yes"}`,
		"not an object":     `[1]`,
	} {
		caller := &recordingCaller{}
		res := proposalServer(caller, setEnabled).run(context.Background(), setEnabled, json.RawMessage(raw))
		if res.OK || res.Code != CodeInvalidRequest || len(caller.calls) != 0 {
			t.Errorf("%s = %+v, calls %d", name, res, len(caller.calls))
		}
	}
}

// Falsify: let the answer of a proposal that ran count as ok.
func TestAnEngineThatRanAProposalIsReportedAsNotConfirmed(t *testing.T) {
	caller := &recordingCaller{resp: Response{Body: json.RawMessage(`{"id":"op_1"}`), CorrelationID: "cid_9"}}
	res := proposalServer(caller, runCycle).run(context.Background(), runCycle, nil)
	if res.OK || res.Code != CodeUnexpected || res.CorrelationID != "cid_9" {
		t.Errorf("result = %+v, want it refused as unexpected, not ok", res)
	}
}

func TestAProposingToolIsNotMarkedReadOnlyAndAReadToolIs(t *testing.T) {
	if !proposes(runCycle) || !proposes(setEnabled) {
		t.Error("an operate or configure tool does not propose")
	}
	read := agenttools.Row{Tool: "status", Method: "GET", Route: "/system/health", Scope: agenttools.ScopeRead}
	none := agenttools.Row{Tool: "status", Method: "GET", Route: "/system/health"}
	if proposes(read) || proposes(none) {
		t.Error("a read tool proposes")
	}
	if _, ok := inputSchema(runCycle)["properties"].(map[string]any)["body"]; !ok {
		t.Error("a proposing tool's schema has no body")
	}
}
