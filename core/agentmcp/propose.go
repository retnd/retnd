package agentmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/retnd/retnd/core/agenttools"
	"github.com/retnd/retnd/core/cliecho"
)

// The tools that propose a change (EPIC U, U2.1, #1349; FR-95).
//
// A tool under operate or configure never changes anything. Calling it sends
// the request the route takes, as the token, and names the tool; the engine
// answers by recording that request as a pending confirmation and refusing it
// with AGENT_CONFIRMATION_REQUIRED, the confirmation's id and when it lapses.
// This file turns that refusal into the tool result an agent reads: an error
// result (isError) that carries the code, the id, the echoed command and the
// expiry, never a success. confirmation_status, a plain read tool over
// GET /agent/confirmations/{id}, is how the agent finds out what a person did.

// parseProposal reads a proposing tool's arguments: the route's placeholders
// as strings, and an optional body object. A tool that names an action sends
// it in the body, and a body that names a different one is refused.
func parseProposal(t agenttools.Row, raw json.RawMessage) ([]string, json.RawMessage, error) {
	args := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		if err := dec.Decode(&args); err != nil {
			return nil, nil, fmt.Errorf("the arguments are not a JSON object: %v", err)
		}
	}
	params := routeParams(t.Route)
	values := make([]string, 0, len(params))
	for _, p := range params {
		v, ok := args[p].(string)
		if !ok || v == "" {
			return nil, nil, fmt.Errorf("%s: a non-empty string is required", p)
		}
		values = append(values, v)
		delete(args, p)
	}
	body := map[string]any{}
	if b, ok := args["body"]; ok {
		m, isMap := b.(map[string]any)
		if !isMap {
			return nil, nil, fmt.Errorf("body: an object is required")
		}
		body = m
		delete(args, "body")
	}
	if len(args) > 0 {
		names := make([]string, 0, len(args))
		for k := range args {
			names = append(names, k)
		}
		slices.Sort(names)
		return nil, nil, fmt.Errorf("unexpected argument %s", strings.Join(names, ", "))
	}
	if t.Action != "" {
		if got, ok := body["action"]; ok && got != t.Action {
			return nil, nil, fmt.Errorf("body.action: this tool is %s and sends that action", t.Action)
		}
		body["action"] = t.Action
	}
	if len(body) == 0 {
		return values, nil, nil
	}
	// json.Marshal sorts the keys, so the same arguments are the same bytes.
	out, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("body: %v", err)
	}
	return values, out, nil
}

// propose makes the call and reads its answer. The engine's refusal is the
// expected one; an answer that says the change ran, which it never should, is
// reported as what it is rather than as a success.
func (s *server) propose(ctx context.Context, t agenttools.Row, raw json.RawMessage) Result {
	pathArgs, body, err := parseProposal(t, raw)
	if err != nil {
		return invalidArguments(err.Error())
	}
	named := make(map[string]string, len(pathArgs))
	for i, p := range routeParams(t.Route) {
		named[p] = pathArgs[i]
	}
	command := cliecho.Echo(cliecho.Action{Method: t.Method, Route: t.Route, Params: named, Query: url.Values{}, Body: body}).Shell()

	resp, err := s.cfg.Caller.Call(ctx, Call{Tool: t.Tool, Method: t.Method, Route: t.Route, PathArgs: pathArgs, Body: body})
	if err != nil {
		return refusalResult(err, command)
	}
	return Result{
		Code: CodeUnexpected, CorrelationID: resp.CorrelationID, Command: command, ExitCode: ExitFailure,
		Message: "the engine answered a proposed change as if it had run; nothing here asked a person, so treat it as not confirmed and tell an operator",
	}
}
