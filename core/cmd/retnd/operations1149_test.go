package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
)

// #1149 (EPIC S, audit row P1-4): `retnd operation list` and
// `retnd operation show <id> [--follow]`.
//
// An operation lives in the memory of the process serving the deployment, so
// both verbs are routed and refuse without a route, the way `assurance
// cancel` does. The fake engine here serves operations from a fixture the
// test states, because the thing under test is whether the CLI asks the
// engine and prints what the engine said, not what a real scrub produces.

// listOperations is GET /operations, newest first as the API serves it.
func (e *fakeEngine) listOperations(w http.ResponseWriter, _ *http.Request) {
	e.opsMu.Lock()
	defer e.opsMu.Unlock()
	writeJSON(w, http.StatusOK, apicontract.ListOperationsResponse{Operations: append([]apicontract.Operation{}, e.ops...)})
}

// getOperation is GET /operations/{id}. A script registered with
// scriptOperation answers one reading per request and repeats the last.
func (e *fakeEngine) getOperation(w http.ResponseWriter, _ *http.Request, id string) {
	e.opsMu.Lock()
	defer e.opsMu.Unlock()
	if script := e.opReads[id]; len(script) > 0 {
		got := script[0]
		if len(script) > 1 {
			e.opReads[id] = script[1:]
		}
		writeJSON(w, http.StatusOK, got)
		return
	}
	for _, op := range e.ops {
		if op.OperationID == id {
			writeJSON(w, http.StatusOK, op)
			return
		}
	}
	refuse(w, http.StatusNotFound, apicontract.ErrorCodeOperationNotFound, "operation "+id+" not found")
}

func (e *fakeEngine) holdOperations(ops ...apicontract.Operation) {
	e.opsMu.Lock()
	defer e.opsMu.Unlock()
	e.ops = ops
}

func (e *fakeEngine) scriptOperation(id string, readings ...apicontract.Operation) {
	e.opsMu.Lock()
	defer e.opsMu.Unlock()
	if e.opReads == nil {
		e.opReads = map[string][]apicontract.Operation{}
	}
	e.opReads[id] = readings
}

func i64(v int64) *int64 { return &v }

// fullOperation fills every optional field the contract's Operation has, so
// a CLI that prints a subset of the wire object cannot pass a field-for-field
// diff by luck.
func fullOperation() apicontract.Operation {
	return apicontract.Operation{
		OperationID:    "op-full",
		Action:         "run_cycle",
		Actor:          "operator",
		BackupSetID:    "production/postgres-primary",
		ConfigRevision: "rev-7",
		CreatedAt:      "2026-10-13T09:00:00Z",
		StartedAt:      "2026-10-13T09:00:01Z",
		FinishedAt:     "2026-10-13T09:02:00Z",
		Status:         "completed",
		Result:         "ok: 3 artifacts",
		Error:          "",
		Cycle:          &apicontract.CycleOutcome{BackupSetsProcessed: 2, ArtifactsWalked: 9, ArtifactsThrough: 3, Moves: &apicontract.CycleMoveOutcome{Attempted: 3, Landed: 2}},
		Restore:        &apicontract.OperationRestore{ArtifactID: "production/postgres-primary/a.dump", Medium: "local", WindowDays: 7, Detail: "staged"},
		Progress:       &apicontract.OperationProgress{Artifact: "a.dump", ArtifactsDone: 1, BackupSetID: "production/postgres-primary", BackupSetsDone: 1},
	}
}

func opOf(id, action, set, status string) apicontract.Operation {
	return apicontract.Operation{OperationID: id, Action: action, BackupSetID: set, Status: status, CreatedAt: "2026-10-13T09:00:00Z"}
}

// stdoutJSON decodes whatever the command printed as one JSON document.
func decodeJSON(t *testing.T, raw string) any {
	t.Helper()
	var v any
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\n%q", err, raw)
	}
	return v
}

func wireOf(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func runOp(t *testing.T, cfg string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	full := append(append([]string{}, args...), "--config", cfg)
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() { code = run(full) })
	})
	return
}

func TestOperationList_RoutedAndJSONIsTheWireObject(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	ops := []apicontract.Operation{fullOperation(), opOf("op-2", "scrub", "a/b", "running")}
	e.holdOperations(ops...)

	code, out, errOut := runOp(t, cfg, "operation", "list", "--json")
	if code != 0 {
		t.Fatalf("operation list --json exited %d\nstderr: %s", code, errOut)
	}
	if !containsRequest(e.requests(), "GET /operations") {
		t.Fatalf("the engine never saw GET /operations; it saw %v", e.requests())
	}
	want := wireOf(t, apicontract.ListOperationsResponse{Operations: ops})
	if got := decodeJSON(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("--json is not the object the API served, field for field\n got: %v\nwant: %v", got, want)
	}
}

func TestOperationList_FiltersByBackupSetAndAction(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	e.holdOperations(
		opOf("op-a1", "scrub", "a/one", "running"),
		opOf("op-a2", "recovery_test", "a/one", "completed"),
		opOf("op-b1", "scrub", "b/two", "completed"),
	)
	ids := func(out string) []string {
		var resp apicontract.ListOperationsResponse
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("not a ListOperationsResponse: %v\n%s", err, out)
		}
		var got []string
		for _, o := range resp.Operations {
			got = append(got, o.OperationID)
		}
		return got
	}
	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{"--backup-set", "a/one"}, []string{"op-a1", "op-a2"}},
		{[]string{"--action", "scrub"}, []string{"op-a1", "op-b1"}},
		{[]string{"--backup-set", "a/one", "--action", "scrub"}, []string{"op-a1"}},
		{[]string{"--backup-set", "nobody/home"}, nil},
	} {
		code, out, errOut := runOp(t, cfg, append([]string{"operation", "list", "--json"}, c.args...)...)
		if code != 0 {
			t.Fatalf("%v exited %d: %s", c.args, code, errOut)
		}
		if got := ids(out); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v listed %v, want %v", c.args, got, c.want)
		}
	}
	// An empty answer is still the object with an array, never null.
	_, out, _ := runOp(t, cfg, "operation", "list", "--json", "--backup-set", "nobody/home")
	if !strings.Contains(out, `"operations": []`) {
		t.Errorf("an empty list must print an empty array, got %q", out)
	}
}

func TestOperationList_HumanOutputNamesIDActionStatusAndSet(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	e.holdOperations(opOf("op-visible-1", "scrub", "a/one", "running"))
	code, out, errOut := runOp(t, cfg, "operation", "list")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"op-visible-1", "scrub", "running", "a/one"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing %q does not show %q", out, want)
		}
	}
}

func TestOperationVerbs_RefuseWithoutARoute(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	for _, args := range [][]string{{"operation", "list"}, {"operation", "show", "op-1"}} {
		code, _, errOut := runOp(t, cfg, args...)
		if code == 0 || code == exitUsage {
			t.Errorf("%v with no engine exited %d, want a refusal that is not a usage error\n%s", args, code, errOut)
		}
		if !strings.Contains(errOut, "RETND_API_URL") {
			t.Errorf("%v: stderr %q does not say how to reach the engine", args, errOut)
		}
	}
}

func TestOperationShow_JSONIsTheWireObject(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	op := fullOperation()
	e.holdOperations(op)

	code, out, errOut := runOp(t, cfg, "operation", "show", "op-full", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !containsRequest(e.requests(), "GET /operations/op-full") {
		t.Fatalf("the engine saw %v, want GET /operations/op-full", e.requests())
	}
	if got, want := decodeJSON(t, out), wireOf(t, op); !reflect.DeepEqual(got, want) {
		t.Errorf("--json is not the object the API served, field for field\n got: %v\nwant: %v", got, want)
	}
}

func TestOperationShow_HumanOutputAndFailureDetail(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	op := opOf("op-bad", "scrub", "a/one", "failed")
	op.Error = "read-back mismatch on a.dump"
	e.holdOperations(op)
	code, out, errOut := runOp(t, cfg, "operation", "show", "op-bad")
	if code != 0 {
		t.Fatalf("show of a failed operation is a successful read, exit %d: %s", code, errOut)
	}
	for _, want := range []string{"op-bad", "scrub", "failed", "a/one", "read-back mismatch on a.dump"} {
		if !strings.Contains(out, want) {
			t.Errorf("show %q does not carry %q", out, want)
		}
	}
}

func TestOperationShow_UnknownIDNamesItAndExitsOne(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	code, _, errOut := runOp(t, cfg, "operation", "show", "no-such-op")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "no-such-op") || !strings.Contains(strings.ToLower(errOut), "not found") {
		t.Errorf("stderr %q does not name the operation the engine could not find", errOut)
	}
}

func fastPolls(t *testing.T) {
	t.Helper()
	old := operationPollEvery
	operationPollEvery = 5 * time.Millisecond
	t.Cleanup(func() { operationPollEvery = old })
}

func TestOperationShow_FollowPollsUntilTerminalAndTakesTheOperationsExit(t *testing.T) {
	fastPolls(t)
	for _, c := range []struct {
		name     string
		final    string
		wantCode int
	}{{"completed", "completed", 0}, {"failed", "failed", 1}} {
		t.Run(c.name, func(t *testing.T) {
			cfg := writeTestConfig(t)
			e := startFakeEngine(t, cfg)
			e.attach(t)
			e.scriptOperation("op-f",
				opOf("op-f", "scrub", "a/one", "queued"),
				opOf("op-f", "scrub", "a/one", "running"),
				opOf("op-f", "scrub", "a/one", c.final),
			)
			code, out, errOut := runOp(t, cfg, "operation", "show", "op-f", "--follow")
			if code != c.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, c.wantCode, out, errOut)
			}
			polls := 0
			for _, r := range e.requests() {
				if r == "GET /operations/op-f" {
					polls++
				}
			}
			if polls < 3 {
				t.Errorf("polled %d times, want at least 3 (queued, running, %s)", polls, c.final)
			}
			if !strings.Contains(out+errOut, "running") || !strings.Contains(out, c.final) {
				t.Errorf("the transitions were not shown\nstdout: %s\nstderr: %s", out, errOut)
			}
		})
	}
}

func TestOperationShow_FollowJSONPrintsOnlyTheFinalObject(t *testing.T) {
	fastPolls(t)
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	e.scriptOperation("op-j", opOf("op-j", "scrub", "a/one", "running"), opOf("op-j", "scrub", "a/one", "completed"))
	code, out, errOut := runOp(t, cfg, "operation", "show", "op-j", "--follow", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var got apicontract.Operation
	dec := json.NewDecoder(bytes.NewReader([]byte(out)))
	if err := dec.Decode(&got); err != nil || got.Status != "completed" {
		t.Fatalf("stdout is not the final operation: %v %q", err, out)
	}
	if dec.More() {
		t.Errorf("stdout carries more than the final object: %q", out)
	}
}

func TestOperationVerbs_UsageErrors(t *testing.T) {
	cfg := writeTestConfig(t)
	// The control: a well-formed call must not be a usage error, or the
	// exits below could be the verb being unknown.
	okCode, _, _ := runOp(t, cfg, "operation", "show", "op-1")
	if okCode == exitUsage {
		t.Fatal("operation show <id> is a usage error, so the rows below prove nothing")
	}
	for _, args := range [][]string{
		{"operation"},
		{"operation", "bogus"},
		{"operation", "list", "extra"},
		{"operation", "show"},
		{"operation", "show", "a", "b"},
		{"operation", "list", "--follow"},
	} {
		if code, _, _ := runOp(t, cfg, args...); code != exitUsage {
			t.Errorf("%v exited %d, want %d", args, code, exitUsage)
		}
	}
}

func TestOperationVerbs_AreInUsageAndHaveCliechoEntries(t *testing.T) {
	all := captureStderr(t, usage)
	for _, want := range []string{"operation list", "operation show", "--follow", "GET /api/v1/operations"} {
		if !strings.Contains(all, want) {
			t.Errorf("usage() does not mention %q", want)
		}
	}
	for _, c := range []struct {
		route string
		act   cliecho.Action
		want  string
	}{
		{"/operations", cliecho.Action{Method: "GET", Route: "/operations"}, "operation list"},
		{"/operations/{id}", cliecho.Action{Method: "GET", Route: "/operations/{id}", Params: map[string]string{"id": "op-9"}}, "operation show op-9"},
	} {
		line := cliecho.Echo(c.act)
		if got := strings.Join(line.Command, " "); !strings.Contains(got, c.want) {
			t.Errorf("cliecho for GET %s echoed %q, want it to contain %q", c.route, got, c.want)
		}
	}
}
