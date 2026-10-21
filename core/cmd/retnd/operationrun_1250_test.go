package main

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
)

// #1250: `retnd operation run [--backup-set S] [--follow] [--json]`.
//
// The Dashboard's "Run all enabled sets" and the set page's "Run this backup
// set" both POST /operations with run_cycle or run_backup_set. A terminal had
// `run` and `fetch --backup-set`, which run the cycle in the CLI's own process,
// so a terminal that only reaches the engine over RETND_API_URL could not
// start a run at all, and a run started locally never showed up in `operation
// list`. These tests drive the twin against the fake engine: the body it
// posts, the key it sends, what it prints and the exit codes it ends on.

// gateShutExit is the code `operation run` documents for the engine's
// destructive gate refusing it. Spelled as a literal here, not read off the
// constant, because it is a promise a script branches on and a renumbering
// has to break this test rather than follow it.
const gateShutExit = 50

// submitted is one POST /operations as the engine received it.
type submitted struct {
	body map[string]any
	key  string
}

// submissions records every POST /operations and answers with whatever
// answer says, so a test states the engine's reply and reads back exactly
// what the CLI sent.
type submissions struct {
	mu   sync.Mutex
	got  []submitted
	errs []string
}

func (s *submissions) all() []submitted {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]submitted(nil), s.got...)
}

func (s *submissions) handler(answer func(w http.ResponseWriter, body map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			s.mu.Lock()
			s.errs = append(s.errs, "the body is not a JSON object: "+string(raw))
			s.mu.Unlock()
			refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, "malformed JSON body")
			return
		}
		key := r.Header.Get("Idempotency-Key")
		s.mu.Lock()
		s.got = append(s.got, submitted{body: body, key: key})
		s.mu.Unlock()
		if key == "" {
			// What apps/common/webhost answers, before anything else.
			refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, "the Idempotency-Key header is required")
			return
		}
		answer(w, body)
	}
}

// accepted answers the way the engine does once the row is durable: 202 and
// the queued Operation.
func accepted(op apicontract.Operation) func(http.ResponseWriter, map[string]any) {
	return func(w http.ResponseWriter, _ map[string]any) { writeJSON(w, http.StatusAccepted, op) }
}

func queuedRun(id, action, set string) apicontract.Operation {
	op := opOf(id, action, set, "queued")
	op.Actor = "operator"
	op.ConfigRevision = "rev-from-engine"
	return op
}

// engineRevision is the config_revision the fake engine's GET /system/version
// answers with, which is the value a run has to carry.
func engineRevision(t *testing.T, e *fakeEngine) string {
	t.Helper()
	rev := e.svc.ConfigRevision()
	if rev == "" {
		t.Fatal("the fake engine reports no config_revision, so a body carrying none would pass")
	}
	return rev
}

func TestOperationRun1250_PostsTheBodyTheRunAllButtonPosts(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	rec := &submissions{}
	e.handle("POST /operations", rec.handler(accepted(queuedRun("op-run-1", "run_cycle", ""))))

	code, out, errOut := runOp(t, cfg, "operation", "run")
	if code != 0 {
		t.Fatalf("operation run exited %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("the engine received %d POST /operations, want exactly 1 (errors: %v)", len(got), rec.errs)
	}
	// Field for field the object useRunControls.ts sends through
	// api.runCycle: the action and the revision, and nothing else. A
	// backup_set_id on a run_cycle is a foreign parameter the engine refuses.
	want := map[string]any{"action": "run_cycle", "config_revision": engineRevision(t, e)}
	if !reflect.DeepEqual(got[0].body, want) {
		t.Errorf("operation run posted\n  %v\nwant\n  %v", got[0].body, want)
	}
	if got[0].key == "" {
		t.Error("operation run sent no Idempotency-Key, which the route requires")
	}

	// The revision came from the engine, asked before the submission.
	seen := e.requests()
	version, post := -1, -1
	for i, r := range seen {
		if r == "GET /system/version" && post < 0 {
			version = i
		}
		if r == "POST /operations" {
			post = i
		}
	}
	if version < 0 || post < 0 || version > post {
		t.Errorf("the engine saw %v; want GET /system/version before POST /operations", seen)
	}
	if !strings.Contains(out, "op-run-1") {
		t.Errorf("stdout does not name the operation the engine started, so there is nothing to hand to operation show:\n%s", out)
	}
}

func TestOperationRun1250_BackupSetMapsToRunBackupSet(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	rec := &submissions{}
	e.handle("POST /operations", rec.handler(accepted(queuedRun("op-set-1", "run_backup_set", "production/postgres-primary"))))

	code, out, errOut := runOp(t, cfg, "operation", "run", "--backup-set", "production/postgres-primary")
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("the engine received %d submissions, want 1 (errors: %v)", len(got), rec.errs)
	}
	// api.runBackupSet's body, field for field.
	want := map[string]any{"action": "run_backup_set", "config_revision": engineRevision(t, e), "backup_set_id": "production/postgres-primary"}
	if !reflect.DeepEqual(got[0].body, want) {
		t.Errorf("operation run --backup-set posted\n  %v\nwant\n  %v", got[0].body, want)
	}
}

func TestOperationRun1250_SkipWorkflowScriptsTravelsOnAPerSetRun(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	rec := &submissions{}
	e.handle("POST /operations", rec.handler(accepted(queuedRun("op-set-2", "run_backup_set", "a/one"))))

	code, _, errOut := runOp(t, cfg, "operation", "run", "--backup-set", "a/one", "--skip-workflow-scripts")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	got := rec.all()
	if len(got) != 1 || got[0].body["skip_workflow_scripts"] != true {
		t.Errorf("--skip-workflow-scripts did not reach the engine as skip_workflow_scripts: %v", got)
	}
}

func TestOperationRun1250_EveryInvocationIsANewSubmission(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	rec := &submissions{}
	e.handle("POST /operations", rec.handler(accepted(queuedRun("op-k", "run_cycle", ""))))

	for range 2 {
		if code, _, errOut := runOp(t, cfg, "operation", "run"); code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
	}
	got := rec.all()
	if len(got) != 2 {
		t.Fatalf("%d submissions, want 2", len(got))
	}
	// Two runs typed at a keyboard are two requests. A reused key would hand
	// the second person the first run's operation instead of starting theirs.
	if got[0].key == got[1].key {
		t.Errorf("both invocations sent Idempotency-Key %q", got[0].key)
	}
}

func TestOperationRun1250_JSONIsTheWireObject(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	op := queuedRun("op-json", "run_backup_set", "a/one")
	rec := &submissions{}
	e.handle("POST /operations", rec.handler(accepted(op)))

	code, out, errOut := runOp(t, cfg, "operation", "run", "--backup-set", "a/one", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got, want := decodeJSON(t, out), wireOf(t, op); !reflect.DeepEqual(got, want) {
		t.Errorf("--json is not the Operation the engine answered with, field for field\n got: %v\nwant: %v", got, want)
	}
}

func TestOperationRun1250_FollowEndsOnTheOutcomesExitCode(t *testing.T) {
	fastPolls(t)
	for _, c := range []struct {
		final    string
		wantCode int
	}{{"completed", 0}, {"failed", 1}, {"cancelled", 1}} {
		t.Run(c.final, func(t *testing.T) {
			cfg := writeTestConfig(t)
			e := startFakeEngine(t, cfg)
			e.attach(t)
			rec := &submissions{}
			e.handle("POST /operations", rec.handler(accepted(queuedRun("op-follow", "run_cycle", ""))))
			e.scriptOperation("op-follow",
				opOf("op-follow", "run_cycle", "", "running"),
				opOf("op-follow", "run_cycle", "", c.final),
			)

			code, out, errOut := runOp(t, cfg, "operation", "run", "--follow")
			if code != c.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, c.wantCode, out, errOut)
			}
			if !containsRequest(e.requests(), "GET /operations/op-follow") {
				t.Errorf("--follow never asked the engine about the operation it started; it saw %v", e.requests())
			}
			if !strings.Contains(errOut, "running") {
				t.Errorf("the change of status was not shown on stderr: %q", errOut)
			}
			if !strings.Contains(out, c.final) {
				t.Errorf("stdout does not show how it ended (%s): %q", c.final, out)
			}
		})
	}
}

func TestOperationRun1250_FollowJSONPrintsOnlyTheFinalObject(t *testing.T) {
	fastPolls(t)
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	rec := &submissions{}
	e.handle("POST /operations", rec.handler(accepted(queuedRun("op-fj", "run_cycle", ""))))
	final := fullOperation()
	final.OperationID = "op-fj"
	e.scriptOperation("op-fj", opOf("op-fj", "run_cycle", "", "running"), final)

	code, out, errOut := runOp(t, cfg, "operation", "run", "--follow", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var got any
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%q", err, out)
	}
	if want := wireOf(t, final); !reflect.DeepEqual(got, want) {
		t.Errorf("--follow --json is not the final Operation\n got: %v\nwant: %v", got, want)
	}
	if dec.More() {
		t.Errorf("stdout carries more than the final object: %q", out)
	}
}

func TestOperationRun1250_AGateRefusalHasItsOwnExitCode(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	rec := &submissions{}
	e.handle("POST /operations", rec.handler(func(w http.ResponseWriter, _ map[string]any) {
		refuse(w, http.StatusForbidden, apicontract.ErrorCodeDestructiveOperationsDisabled,
			"destructive operations are refused over HTTP in this release; run the equivalent `retnd` command on the host")
	}))

	code, out, errOut := runOp(t, cfg, "operation", "run")
	if code != gateShutExit {
		t.Fatalf("a shut gate exited %d, want %d\nstdout: %s\nstderr: %s", code, gateShutExit, out, errOut)
	}
	if !strings.Contains(errOut, string(apicontract.ErrorCodeDestructiveOperationsDisabled)) {
		t.Errorf("stderr does not carry the engine's code: %q", errOut)
	}
	// The local twin is what still works with the gate shut, so the refusal
	// names it for this shape of run.
	if !strings.Contains(errOut, cliecho.Binary+" run") {
		t.Errorf("stderr does not name the verb that runs the cycle on the host: %q", errOut)
	}

	_, _, perSet := runOp(t, cfg, "operation", "run", "--backup-set", "a/one")
	if !strings.Contains(perSet, cliecho.Binary+" fetch --backup-set a/one") {
		t.Errorf("a per-set refusal does not name `fetch --backup-set a/one`: %q", perSet)
	}
}

// The partner the code above needs: a refusal that is not the gate must not
// be the gate's code, or 50 would only mean "refused".
func TestOperationRun1250_OtherRefusalsAreOrdinaryFailures(t *testing.T) {
	for _, c := range []struct {
		status int
		code   apicontract.ErrorCode
	}{
		{http.StatusConflict, apicontract.ErrorCodeOperationAlreadyRunning},
		{http.StatusConflict, apicontract.ErrorCodeConfigRevisionStale},
		{http.StatusNotFound, apicontract.ErrorCodeBackupSetNotFound},
	} {
		t.Run(string(c.code), func(t *testing.T) {
			cfg := writeTestConfig(t)
			e := startFakeEngine(t, cfg)
			e.attach(t)
			rec := &submissions{}
			e.handle("POST /operations", rec.handler(func(w http.ResponseWriter, _ map[string]any) {
				refuse(w, c.status, c.code, "refused for the test")
			}))
			code, _, errOut := runOp(t, cfg, "operation", "run")
			if len(rec.all()) != 1 {
				t.Fatalf("the engine received %d submissions, so this row tested nothing", len(rec.all()))
			}
			if code != exitFailure {
				t.Errorf("%s exited %d, want %d\n%s", c.code, code, exitFailure, errOut)
			}
		})
	}
}

func TestOperationRun1250_RefusesWithoutARoute(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	code, _, errOut := runOp(t, cfg, "operation", "run")
	if code == 0 || code == exitUsage {
		t.Errorf("operation run with no engine exited %d, want a refusal that is not a usage error\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "RETND_API_URL") {
		t.Errorf("stderr %q does not say how to reach the engine", errOut)
	}
}

func TestOperationRun1250_UsageErrors(t *testing.T) {
	cfg := writeTestConfig(t)
	// The control: a well-formed call must not be a usage error, or the rows
	// below could be the verb being unknown.
	if code, _, _ := runOp(t, cfg, "operation", "run"); code == exitUsage {
		t.Fatal("operation run is a usage error, so the rows below prove nothing")
	}
	for _, args := range [][]string{
		{"operation", "run", "extra"},
		{"operation", "run", "--backup-set", "no-slash"},
		// The engine refuses skip_workflow_scripts on a run_cycle as a
		// foreign parameter, so it is wrong on the command line already.
		{"operation", "run", "--skip-workflow-scripts"},
		{"operation", "run", "--action", "scrub"},
	} {
		if code, _, _ := runOp(t, cfg, args...); code != exitUsage {
			t.Errorf("%v exited %d, want %d", args, code, exitUsage)
		}
	}
}

func TestOperationRun1250_IsInUsage(t *testing.T) {
	text := captureStderr(t, usage)
	block := usageBlock(t, text, "  operation run", "  operation list", "  operation show", "  catalog ")
	for _, want := range []string{"--backup-set", "--follow", "--json", "--skip-workflow-scripts", "POST /api/v1/operations", "50"} {
		if !strings.Contains(block, want) {
			t.Errorf("usage()'s operation run entry does not mention %q:\n%s", want, block)
		}
	}
	if !strings.Contains(text, "\n  50  ") {
		t.Errorf("the exit code table has no row for %d", gateShutExit)
	}
}

// The two run buttons echo `operation run` now, and the line they echo is one
// this binary accepts.
func TestOperationRun1250_CliechoEchoesALineThatParses(t *testing.T) {
	configPath := unparseableConfig(t)
	for _, c := range []struct {
		body string
		want string
	}{
		{`{"action":"run_cycle","config_revision":"r1"}`, cliecho.Binary + " operation run"},
		{`{"action":"run_backup_set","config_revision":"r1","backup_set_id":"api-server/var-backups"}`, cliecho.Binary + " operation run --backup-set api-server/var-backups"},
		{`{"action":"run_backup_set","config_revision":"r1","backup_set_id":"api-server/var-backups","skip_workflow_scripts":true}`, cliecho.Binary + " operation run --backup-set api-server/var-backups --skip-workflow-scripts"},
	} {
		line := cliecho.Echo(cliecho.Action{Method: "POST", Route: "/operations", Body: []byte(c.body)})
		if got := line.Shell(); got != c.want {
			t.Errorf("%s echoed %q, want %q", c.body, got, c.want)
			continue
		}
		if code := dispatch(t, line.Command, configPath); code == exitUsage {
			t.Errorf("`%s` is refused as a bad command line", line.Shell())
		}
	}
}
