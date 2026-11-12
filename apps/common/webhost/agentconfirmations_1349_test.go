package webhost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// U2.1 (#1349) over HTTP: approve and deny are session-only, approve asks for
// a step-up and replays the record, and the replay meets everything the
// original request would have. Written red first; each test names the
// mutation that has to turn it red.

// fakeConfirmations is an honest in-memory stand-in for the service's
// confirmation methods. It keeps the records a test plants and writes down
// every call it is asked for, so a test can say a method was never reached.
type fakeConfirmations struct {
	mu       sync.Mutex
	created  []service.CreateAgentConfirmationRequest
	createFn func(service.CreateAgentConfirmationRequest) error
	records  map[string]service.AgentConfirmation
	err      error
	claimed  []string
	denied   []string
	finished []service.AgentExecution
	approver string
}

func newFakeConfirmations(records ...service.AgentConfirmation) *fakeConfirmations {
	f := &fakeConfirmations{records: map[string]service.AgentConfirmation{}}
	for _, c := range records {
		f.records[c.ID] = c
	}
	return f
}

func (f *fakeConfirmations) CreateAgentConfirmation(_ context.Context, req service.CreateAgentConfirmationRequest) (service.AgentConfirmation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createFn != nil {
		if err := f.createFn(req); err != nil {
			return service.AgentConfirmation{}, err
		}
	}
	f.created = append(f.created, req)
	c := service.AgentConfirmation{
		ID: "cnf_" + strconv.Itoa(len(f.created)), TokenID: req.Principal.TokenID, TokenName: req.Principal.Name, Tool: req.Tool,
		Route: req.Route, Method: req.Method, Body: req.Body, Command: req.Command, BackupSet: req.BackupSet, State: "pending",
		CreatedAt: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 11, 11, 9, 15, 0, 0, time.UTC),
	}
	f.records[c.ID] = c
	return c, nil
}

// fakeTools is the catalogue's answer for the tools these tests use.
type fakeTools map[string]struct{ method, path, action, scope string }

func (f fakeTools) ResolveAgentTool(tool, method, path, action string) (string, bool) {
	t, ok := f[tool]
	if !ok || t.method != method || t.path != path || t.action != action {
		return "", false
	}
	return t.scope, true
}

var testTools = fakeTools{
	"run_cycle":          {"POST", "/operations", "run_cycle", "operate"},
	"backup_set_enabled": {"POST", "/backup-sets/production/postgres/enabled", "", "configure"},
}

func (f *fakeConfirmations) ListAgentConfirmations(context.Context) ([]service.AgentConfirmation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []service.AgentConfirmation
	for _, c := range f.records {
		out = append(out, c)
	}
	return out, f.err
}

func (f *fakeConfirmations) AgentConfirmationStatus(_ context.Context, tokenID, id string) (service.AgentConfirmation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.records[id]
	if !ok || (tokenID != "" && c.TokenID != tokenID) {
		return service.AgentConfirmation{}, service.ErrAgentConfirmationNotFound
	}
	return c, nil
}

func (f *fakeConfirmations) ApproveAgentConfirmation(_ context.Context, id, approver string) (service.AgentConfirmation, service.AgentPrincipal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimed = append(f.claimed, id)
	f.approver = approver
	c, ok := f.records[id]
	switch {
	case f.err != nil:
		return service.AgentConfirmation{}, service.AgentPrincipal{}, f.err
	case !ok:
		return service.AgentConfirmation{}, service.AgentPrincipal{}, service.ErrAgentConfirmationNotFound
	}
	c.State, c.DecidedBy = "approved", approver
	f.records[id] = c
	return c, service.AgentPrincipal{TokenID: c.TokenID, Name: c.TokenName, Scope: service.AgentScopeOperate}, nil
}

func (f *fakeConfirmations) FinishAgentApproval(_ context.Context, c service.AgentConfirmation, exec service.AgentExecution) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, exec)
	return nil
}

func (f *fakeConfirmations) DenyAgentConfirmation(_ context.Context, id, by string) (service.AgentConfirmation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.denied = append(f.denied, id)
	c, ok := f.records[id]
	switch {
	case f.err != nil:
		return service.AgentConfirmation{}, f.err
	case !ok:
		return service.AgentConfirmation{}, service.ErrAgentConfirmationNotFound
	}
	c.State, c.DecidedBy = "denied", by
	return c, nil
}

type confirmationsBackend struct {
	*backupSetFakeBackend
	*fakeAgentAccess
	*fakeConfirmations
}

type confirmationsRig struct {
	router  http.Handler
	backend *confirmationsBackend
	rec     *recordingBackend
	steps   *stubStepUp
}

func newConfirmationsRig(t *testing.T, gate DestructiveGate, records ...service.AgentConfirmation) *confirmationsRig {
	t.Helper()
	b := &confirmationsBackend{
		backupSetFakeBackend: newBackupSetFakeBackend(),
		fakeAgentAccess:      newFakeAgentAccess(),
		fakeConfirmations:    newFakeConfirmations(records...),
	}
	rec := &recordingBackend{}
	steps := &stubStepUp{user: "alice", password: stepUpPassword}
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: b, Gate: gate, StepUp: steps, Recorder: rec,
		AgentTools: testTools, BinaryVersion: "test", Commit: "test",
	})
	return &confirmationsRig{router: router, backend: b, rec: rec, steps: steps}
}

func pendingConfirmation(id, method, route, body string) service.AgentConfirmation {
	return service.AgentConfirmation{
		ID: id, TokenID: "agt_op", TokenName: "runner", Tool: "run_cycle", Route: route, Method: method,
		Body: []byte(body), Command: "retnd run", State: "pending",
		CreatedAt: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 11, 11, 9, 15, 0, 0, time.UTC),
	}
}

// session builds a signed-in browser request: the cookie pair, and the
// step-up password when one is given.
func session(method, path, body, password string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if password != "" {
		req.Header.Set(StepUpHeader, password)
	}
	return csrfPaired(req)
}

// Falsify: accept a bearer on the approve route.
func TestATokenCannotApproveItsOwnAsk(t *testing.T) {
	rig := newConfirmationsRig(t, nil, pendingConfirmation("cnf_1", "POST", "/operations", `{"action":"run_cycle"}`))
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/agent/confirmations/cnf_1/approve"},
		{http.MethodPost, "/api/v1/agent/confirmations/cnf_1/deny"},
		{http.MethodGet, "/api/v1/agent/confirmations"},
	} {
		for name, token := range map[string]string{"operate": operateToken, "read": readToken} {
			rec := serve(rig.router, bearer(tc.method, tc.path, "", token))
			if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_ROUTE_DENIED" {
				t.Errorf("%s token on %s %s = %d %s, want 403 AGENT_ROUTE_DENIED", name, tc.method, tc.path, rec.Code, rec.Body.String())
			}
		}
	}
	if len(rig.backend.claimed) != 0 || len(rig.backend.denied) != 0 {
		t.Errorf("a bearer reached the service: claimed %v, denied %v", rig.backend.claimed, rig.backend.denied)
	}
	if rig.steps.asked() != 0 {
		t.Errorf("a bearer made the step-up check run")
	}
}

func TestTheConfirmationRoutesAreOnTheDeniedPrefixList(t *testing.T) {
	for _, path := range []string{
		"/api/v1/agent/confirmations", "/api/v1/agent/confirmations/x/approve", "/api/v1/agent/confirmations/x/deny",
		"/api/v1/agent/confirmations/../confirmations/x/approve", "/api/v1/agent/%63onfirmations/x/approve",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer rat_whatever_0000000000000000000000000000")
		if !agentRouteDenied(req) {
			t.Errorf("%s is not refused to a bearer", path)
		}
	}
}

// Falsify: skip VerifyStepUp.
func TestApprovalWithoutAStepUpIsRefused(t *testing.T) {
	rig := newConfirmationsRig(t, nil, pendingConfirmation("cnf_1", "POST", "/operations", `{"action":"run_cycle"}`))
	rec := serve(rig.router, session(http.MethodPost, "/api/v1/agent/confirmations/cnf_1/approve", "", ""))
	if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "STEP_UP_REQUIRED" {
		t.Fatalf("no password = %d %s, want 403 STEP_UP_REQUIRED", rec.Code, rec.Body.String())
	}
	rec = serve(rig.router, session(http.MethodPost, "/api/v1/agent/confirmations/cnf_1/approve", "", "not the password"))
	if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "STEP_UP_FAILED" {
		t.Fatalf("wrong password = %d %s, want 403 STEP_UP_FAILED", rec.Code, rec.Body.String())
	}
	if len(rig.backend.claimed) != 0 {
		t.Errorf("a refused step-up still claimed the confirmation: %v", rig.backend.claimed)
	}
}

func TestApprovalOverTheStepUpLimitIsToldToWait(t *testing.T) {
	b := &confirmationsBackend{
		backupSetFakeBackend: newBackupSetFakeBackend(), fakeAgentAccess: newFakeAgentAccess(),
		fakeConfirmations: newFakeConfirmations(pendingConfirmation("cnf_1", "POST", "/operations", `{}`)),
	}
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: b, StepUp: rateLimitedStepUp{wait: 36500 * time.Millisecond},
		BinaryVersion: "test", Commit: "test",
	})
	rec := serve(router, session(http.MethodPost, "/api/v1/agent/confirmations/cnf_1/approve", "", stepUpPassword))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "37" {
		t.Errorf("over the limit = %d, Retry-After %q; want 429 and 37", rec.Code, rec.Header().Get("Retry-After"))
	}
	if len(b.claimed) != 0 {
		t.Errorf("the limiter did not stop the claim")
	}
}

func TestWithNoStepUpWiredApprovalFailsClosed(t *testing.T) {
	b := &confirmationsBackend{
		backupSetFakeBackend: newBackupSetFakeBackend(), fakeAgentAccess: newFakeAgentAccess(),
		fakeConfirmations: newFakeConfirmations(pendingConfirmation("cnf_1", "POST", "/operations", `{}`)),
	}
	router := NewRouter(RouterConfig{Platform: allowingPlatform("alice"), Backend: b, BinaryVersion: "test", Commit: "test"})
	rec := serve(router, session(http.MethodPost, "/api/v1/agent/confirmations/cnf_1/approve", "", stepUpPassword))
	if rec.Code != http.StatusServiceUnavailable || responseErrorCode(rec.Body.String()) != "STEP_UP_UNAVAILABLE" || len(b.claimed) != 0 {
		t.Errorf("no verifier = %d %s, claimed %v", rec.Code, rec.Body.String(), b.claimed)
	}
}

// Falsify: let approve read a body.
func TestApprovalRefusesASubstitutedBody(t *testing.T) {
	rig := newConfirmationsRig(t, alwaysPassGate{}, pendingConfirmation("cnf_1", "POST", "/operations", `{"action":"run_cycle"}`))
	for name, body := range map[string]string{
		"a different request": `{"action":"restore_snapshot"}`,
		"the same request":    `{"action":"run_cycle"}`,
		"an empty object":     `{}`,
		"a space":             ` `,
	} {
		rec := serve(rig.router, session(http.MethodPost, "/api/v1/agent/confirmations/cnf_1/approve", body, stepUpPassword))
		if rec.Code != http.StatusBadRequest || responseErrorCode(rec.Body.String()) != "AGENT_CONFIRMATION_BODY_SUBSTITUTED" {
			t.Errorf("%s = %d %s, want 400 AGENT_CONFIRMATION_BODY_SUBSTITUTED", name, rec.Code, rec.Body.String())
		}
	}
	if len(rig.backend.claimed) != 0 || len(rig.backend.ops) != 0 {
		t.Errorf("a refused body ran something: claimed %v, operations %d", rig.backend.claimed, len(rig.backend.ops))
	}
}

// Falsify: bypass the gate on replay.
func TestRunCycleApprovedMeetsTheShutGateAsAPersonWould(t *testing.T) {
	rig := newConfirmationsRig(t, nil, pendingConfirmation("cnf_1", "POST", "/operations", `{"action":"run_cycle"}`))
	rec := serve(rig.router, session(http.MethodPost, "/api/v1/agent/confirmations/cnf_1/approve", "", stepUpPassword))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d %s; the approval stands even when the replay is refused", rec.Code, rec.Body.String())
	}
	var got apicontract.AgentConfirmation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "approved" || got.Execution == nil || got.Execution.Code != "DESTRUCTIVE_OPERATIONS_DISABLED" || got.Execution.Status != 403 || got.Execution.ExitCode != 50 {
		t.Errorf("approved = %+v execution %+v, want the shut gate's refusal with exit code 50", got, got.Execution)
	}
	if got.Execution != nil && got.Execution.CorrelationID == "" {
		t.Errorf("the execution carries no correlation id")
	}
	if len(rig.backend.ops) != 0 {
		t.Errorf("a refused replay started %d operations", len(rig.backend.ops))
	}
	if len(rig.backend.finished) != 1 || rig.backend.finished[0].Code != "DESTRUCTIVE_OPERATIONS_DISABLED" {
		t.Errorf("recorded executions = %+v", rig.backend.finished)
	}
	var replayed bool
	for _, a := range rig.rec.recorded() {
		if a.Route == "/operations" && a.Actor == "agent:runner" && a.ErrorCode == "DESTRUCTIVE_OPERATIONS_DISABLED" {
			replayed = true
		}
	}
	if !replayed {
		t.Errorf("the action log has no refused /operations by agent:runner: %+v", rig.rec.recorded())
	}
}

func TestApprovalReplaysTheRecordWithTheTokenAsTheActorAndTheApproverRecorded(t *testing.T) {
	rig := newConfirmationsRig(t, alwaysPassGate{}, pendingConfirmation("cnf_1", "POST", "/backup-sets/production/postgres/enabled", `{ "enabled" : false }`))
	rec := serve(rig.router, session(http.MethodPost, "/api/v1/agent/confirmations/cnf_1/approve", "", stepUpPassword))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body.String())
	}
	if got := rig.backend.lastSetEnabled; got.id != "production/postgres" || got.enabled {
		t.Errorf("the replay called SetBackupSetEnabled(%q, %v), want the recorded request", got.id, got.enabled)
	}
	if rig.backend.approver != "alice" {
		t.Errorf("decided_by = %q, want the signed-in approver", rig.backend.approver)
	}
	var got apicontract.AgentConfirmation
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Execution == nil || got.Execution.Status != 200 || got.Execution.Code != "" || got.Execution.ExitCode != 0 {
		t.Errorf("execution = %+v, want a plain success", got.Execution)
	}

	var actors []string
	for _, a := range rig.rec.recorded() {
		actors = append(actors, a.Actor+" "+a.Method+" "+a.Route)
	}
	want := []string{"agent:runner POST /backup-sets/{source}/{set}/enabled", "alice POST /agent/confirmations/{id}/approve"}
	if strings.Join(actors, "|") != strings.Join(want, "|") {
		t.Errorf("action log = %v, want the replay by the token then the approval by the person", actors)
	}
}

func TestTheReplayRequestIsTheRecordAndNothingTheApproveRequestCarried(t *testing.T) {
	var seen *http.Request
	var seenBody []byte
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		seenBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	})
	record := "{ \"action\" :\"run_cycle\",\n\"z\":1 }\n"
	c := pendingConfirmation("cnf_1", "POST", "/operations", record)
	exec := replayAgentRequest(context.Background(), capture, c, service.AgentPrincipal{TokenID: "agt_op", Name: "runner", Scope: "operate"})
	if seen == nil {
		t.Fatal("nothing was replayed")
	}
	if string(seenBody) != record {
		t.Errorf("replayed body = %q, want %q byte for byte", seenBody, record)
	}
	if seen.Method != http.MethodPost || seen.URL.Path != "/api/v1/operations" {
		t.Errorf("replayed %s %s", seen.Method, seen.URL.Path)
	}
	for _, h := range []string{StepUpHeader, "Authorization", "Cookie"} {
		if seen.Header.Get(h) != "" {
			t.Errorf("the replay carries %s", h)
		}
	}
	if exec.Status != http.StatusAccepted || exec.Code != "" {
		t.Errorf("execution = %+v", exec)
	}
}

func TestAReplayNeverReachesARouteABearerCannot(t *testing.T) {
	for _, route := range []string{"/agent/confirmations/cnf_2/approve", "/agent/tokens", "/auth/logout", "/recoveries", "/system/first-run"} {
		rig := newConfirmationsRig(t, alwaysPassGate{}, pendingConfirmation("cnf_1", "POST", route, `{"name":"x"}`))
		rec := serve(rig.router, session(http.MethodPost, "/api/v1/agent/confirmations/cnf_1/approve", "", stepUpPassword))
		var got apicontract.AgentConfirmation
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != http.StatusOK || got.Execution == nil || got.Execution.Code != "AGENT_ROUTE_DENIED" || got.Execution.Status != http.StatusForbidden {
			t.Errorf("replaying %s = %d %s, want an execution of AGENT_ROUTE_DENIED", route, rec.Code, rec.Body.String())
		}
	}
}

// Falsify: let a raw bearer request create a confirmation, or run.
func TestARawBearerRequestToAMutatingRouteCreatesNoConfirmationAndRunsNothing(t *testing.T) {
	rig := newConfirmationsRig(t, alwaysPassGate{})
	rec := serve(rig.router, bearer(http.MethodPost, "/api/v1/operations", `{"action":"run_cycle"}`, operateToken))
	if rec.Code != http.StatusConflict || responseErrorCode(rec.Body.String()) != "AGENT_CONFIRMATION_REQUIRED" {
		t.Fatalf("an operate token = %d %s, want 409 AGENT_CONFIRMATION_REQUIRED", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "cnf_") {
		t.Errorf("the refusal names a confirmation id, so one was created: %s", rec.Body.String())
	}
	if len(rig.backend.ops) != 0 {
		t.Errorf("a raw bearer request ran an operation")
	}
	if got, _ := rig.backend.ListAgentConfirmations(context.Background()); len(got) != 0 {
		t.Errorf("a raw bearer request left %d confirmations", len(got))
	}
}

func TestDenyIsTheSignedInPersonsAndEndsTheConfirmation(t *testing.T) {
	rig := newConfirmationsRig(t, nil, pendingConfirmation("cnf_1", "POST", "/operations", `{}`))
	rec := serve(rig.router, session(http.MethodPost, "/api/v1/agent/confirmations/cnf_1/deny", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("deny = %d %s", rec.Code, rec.Body.String())
	}
	var got apicontract.AgentConfirmation
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.State != "denied" || got.DecidedBy == nil || *got.DecidedBy != "alice" {
		t.Errorf("denied = %+v", got)
	}
	if rec := serve(rig.router, httptest.NewRequest(http.MethodPost, "/api/v1/agent/confirmations/cnf_1/deny", nil)); rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "CSRF_TOKEN_MISSING" {
		t.Errorf("deny without the CSRF pair = %d %s", rec.Code, rec.Body.String())
	}
}

func TestTheServicesRefusalsKeepTheirCodes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{service.ErrAgentConfirmationNotFound, 404, "AGENT_CONFIRMATION_NOT_FOUND"},
		{service.ErrAgentConfirmationExpired, 409, "AGENT_CONFIRMATION_EXPIRED"},
		{service.ErrAgentConfirmationNotPending, 409, "AGENT_CONFIRMATION_DECIDED"},
		{service.ErrAgentConfirmationBodySubstituted, 409, "AGENT_CONFIRMATION_BODY_SUBSTITUTED"},
		{service.ErrAgentConfirmationTokenEnded, 409, "AGENT_CONFIRMATION_TOKEN_ENDED"},
	} {
		for _, verb := range []string{"approve", "deny"} {
			if verb == "deny" && tc.err == service.ErrAgentConfirmationBodySubstituted {
				continue
			}
			rig := newConfirmationsRig(t, nil)
			rig.backend.err = tc.err
			password := ""
			if verb == "approve" {
				password = stepUpPassword
			}
			rec := serve(rig.router, session(http.MethodPost, "/api/v1/agent/confirmations/cnf_1/"+verb, "", password))
			if rec.Code != tc.status || responseErrorCode(rec.Body.String()) != tc.code {
				t.Errorf("%s with %v = %d %s, want %d %s", verb, tc.err, rec.Code, rec.Body.String(), tc.status, tc.code)
			}
		}
	}
}

func TestTheListIsWhatTheServiceHasAndNeverTheBody(t *testing.T) {
	rig := newConfirmationsRig(t, nil, pendingConfirmation("cnf_1", "POST", "/operations", `{"secret":"x"}`))
	rec := serve(rig.router, session(http.MethodGet, "/api/v1/agent/confirmations", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("the list carries the recorded body: %s", rec.Body.String())
	}
	var got apicontract.ListAgentConfirmationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Confirmations) != 1 || got.Confirmations[0].Command != "retnd run" {
		t.Errorf("list = %+v, %v", got, err)
	}
}

// toolCall is a bearer request that says which tool it is for, the way both
// transports make one.
func toolCall(method, path, body, token, tool string) *http.Request {
	req := bearer(method, path, body, token)
	req.Header.Set(service.AgentToolHeader, tool)
	req.Header.Set(service.AgentTransportHeader, "stdio")
	return req
}

// Falsify: execute on the tool call.
func TestAnOperateToolCallCreatesAConfirmationRecordingTheRequestAndRunsNothing(t *testing.T) {
	rig := newConfirmationsRig(t, alwaysPassGate{})
	body := "{ \"action\":\"run_cycle\" }"
	rec := serve(rig.router, toolCall(http.MethodPost, "/api/v1/operations", body, operateToken, "run_cycle"))
	if rec.Code != http.StatusConflict || responseErrorCode(rec.Body.String()) != "AGENT_CONFIRMATION_REQUIRED" {
		t.Fatalf("tool call = %d %s, want 409 AGENT_CONFIRMATION_REQUIRED", rec.Code, rec.Body.String())
	}
	if len(rig.backend.created) != 1 {
		t.Fatalf("%d confirmations created, want exactly one", len(rig.backend.created))
	}
	got := rig.backend.created[0]
	if string(got.Body) != body || got.Method != "POST" || got.Route != "/operations" || got.Tool != "run_cycle" || got.Scope != "operate" {
		t.Errorf("recorded %+v, want the request as sent", got)
	}
	if got.Principal.Name != "runner" || !strings.HasPrefix(got.Command, "retnd") {
		t.Errorf("principal %+v, command %q", got.Principal, got.Command)
	}
	if id := rec.Header().Get(ConfirmationIDHeader); id != "cnf_1" || rec.Header().Get(ConfirmationExpiresHeader) == "" {
		t.Errorf("headers %q, %q: the answer has to name the confirmation and its expiry", id, rec.Header().Get(ConfirmationExpiresHeader))
	}
	if !strings.Contains(rec.Body.String(), "cnf_1") {
		t.Errorf("the message does not name the confirmation: %s", rec.Body.String())
	}
	if len(rig.backend.ops) != 0 {
		t.Errorf("a tool call ran %d operations", len(rig.backend.ops))
	}
}

func TestAConfigureToolCallRecordsTheSetItConcerns(t *testing.T) {
	rig := newConfirmationsRig(t, nil)
	rig.backend.known[operateToken] = service.AgentPrincipal{TokenID: "agt_op", Name: "runner", Scope: "configure"}
	rec := serve(rig.router, toolCall(http.MethodPost, "/api/v1/backup-sets/production/postgres/enabled", `{"enabled":false}`, operateToken, "backup_set_enabled"))
	if rec.Code != http.StatusConflict || len(rig.backend.created) != 1 {
		t.Fatalf("tool call = %d %s, created %d", rec.Code, rec.Body.String(), len(rig.backend.created))
	}
	if got := rig.backend.created[0]; got.BackupSet != "production/postgres" || got.Scope != "configure" || got.Route != "/backup-sets/production/postgres/enabled" {
		t.Errorf("recorded %+v", got)
	}
}

// Falsify: create a confirmation from a request that names no tool.
func TestARequestThatNamesNoToolOrTheWrongOneCreatesNothing(t *testing.T) {
	for name, req := range map[string]*http.Request{
		"no tool header":          bearer(http.MethodPost, "/api/v1/operations", `{"action":"run_cycle"}`, operateToken),
		"an unknown tool":         toolCall(http.MethodPost, "/api/v1/operations", `{"action":"run_cycle"}`, operateToken, "format_disk"),
		"a tool for other work":   toolCall(http.MethodPost, "/api/v1/operations", `{"action":"restore_snapshot"}`, operateToken, "run_cycle"),
		"a tool on another route": toolCall(http.MethodPost, "/api/v1/backup-sets/production/postgres/read-only", `{"read_only":true}`, operateToken, "run_cycle"),
		"a malformed tool name":   toolCall(http.MethodPost, "/api/v1/operations", `{"action":"run_cycle"}`, operateToken, "Run Cycle;"),
	} {
		rig := newConfirmationsRig(t, alwaysPassGate{})
		rec := serve(rig.router, req)
		if rec.Code != http.StatusConflict || responseErrorCode(rec.Body.String()) != "AGENT_CONFIRMATION_REQUIRED" {
			t.Errorf("%s = %d %s, want the plain refusal", name, rec.Code, rec.Body.String())
		}
		if len(rig.backend.created) != 0 || rec.Header().Get(ConfirmationIDHeader) != "" || len(rig.backend.ops) != 0 {
			t.Errorf("%s created %d confirmations or ran something", name, len(rig.backend.created))
		}
	}
}

func TestAReadTokenCreatesNoConfirmationEvenForANamedTool(t *testing.T) {
	rig := newConfirmationsRig(t, alwaysPassGate{})
	rec := serve(rig.router, toolCall(http.MethodPost, "/api/v1/operations", `{"action":"run_cycle"}`, readToken, "run_cycle"))
	if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_SCOPE_INSUFFICIENT" || len(rig.backend.created) != 0 {
		t.Errorf("read token = %d %s, created %d", rec.Code, rec.Body.String(), len(rig.backend.created))
	}
}

func TestTheServicesRefusalsAtCreationKeepTheirCodes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{service.ErrAgentConfirmationLimit, 429, "AGENT_CONFIRMATION_LIMIT"},
		{service.ErrAgentScopeInsufficient, 403, "AGENT_SCOPE_INSUFFICIENT"},
		{service.ErrAgentTokenInvalid, 401, "AGENT_TOKEN_INVALID"},
		{service.ErrInvalidRequest, 400, "INVALID_REQUEST"},
	} {
		rig := newConfirmationsRig(t, nil)
		rig.backend.createFn = func(service.CreateAgentConfirmationRequest) error { return tc.err }
		rec := serve(rig.router, toolCall(http.MethodPost, "/api/v1/operations", `{"action":"run_cycle"}`, operateToken, "run_cycle"))
		if rec.Code != tc.status || responseErrorCode(rec.Body.String()) != tc.code {
			t.Errorf("%v = %d %s, want %d %s", tc.err, rec.Code, rec.Body.String(), tc.status, tc.code)
		}
	}
}

// The whole path: a tool call records it, a person approves it, and the gate
// meets the replay.
func TestAToolCallThenAnApprovalMeetsTheShutGate(t *testing.T) {
	rig := newConfirmationsRig(t, nil)
	rec := serve(rig.router, toolCall(http.MethodPost, "/api/v1/operations", `{"action":"run_cycle"}`, operateToken, "run_cycle"))
	id := rec.Header().Get(ConfirmationIDHeader)
	if id == "" {
		t.Fatalf("no confirmation: %d %s", rec.Code, rec.Body.String())
	}
	rec = serve(rig.router, session(http.MethodPost, "/api/v1/agent/confirmations/"+id+"/approve", "", stepUpPassword))
	var got apicontract.AgentConfirmation
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.Execution == nil || got.Execution.Code != "DESTRUCTIVE_OPERATIONS_DISABLED" || got.Execution.ExitCode != 50 {
		t.Errorf("approve = %d %s", rec.Code, rec.Body.String())
	}
}

func TestATokenReadsTheStateOfItsOwnConfirmationAndNobodyElses(t *testing.T) {
	own := pendingConfirmation("cnf_1", "POST", "/operations", `{"action":"run_cycle"}`)
	other := pendingConfirmation("cnf_2", "POST", "/operations", `{}`)
	other.TokenID = "agt_other"
	rig := newConfirmationsRig(t, nil, own, other)

	rec := serve(rig.router, bearer(http.MethodGet, "/api/v1/agent/confirmations/cnf_1", "", operateToken))
	var got apicontract.AgentConfirmation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || got.State != "pending" || got.Command != "retnd run" {
		t.Fatalf("own = %d %s", rec.Code, rec.Body.String())
	}
	rec = serve(rig.router, bearer(http.MethodGet, "/api/v1/agent/confirmations/cnf_2", "", operateToken))
	if rec.Code != http.StatusNotFound || responseErrorCode(rec.Body.String()) != "AGENT_CONFIRMATION_NOT_FOUND" {
		t.Errorf("another token's = %d %s, want 404", rec.Code, rec.Body.String())
	}
	// A person reads any.
	rec = serve(rig.router, session(http.MethodGet, "/api/v1/agent/confirmations/cnf_2", "", ""))
	if rec.Code != http.StatusOK {
		t.Errorf("a person reading = %d %s", rec.Code, rec.Body.String())
	}
}

func TestOnlyTheStatusReadIsOpenToATokenUnderTheConfirmationsPrefix(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		denied       bool
	}{
		{"GET", "/api/v1/agent/confirmations/cnf_1", false},
		{"GET", "/api/v1/agent/confirmations", true},
		{"GET", "/api/v1/agent/confirmations/", true},
		{"GET", "/api/v1/agent/confirmations/cnf_1/approve", true},
		{"POST", "/api/v1/agent/confirmations/cnf_1", true},
		{"POST", "/api/v1/agent/confirmations/cnf_1/approve", true},
		{"POST", "/api/v1/agent/confirmations/cnf_1/deny", true},
		{"DELETE", "/api/v1/agent/confirmations/cnf_1", true},
		{"GET", "/api/v1/agent/confirmations/../confirmations", true},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if got := agentRouteDenied(req); got != tc.denied {
			t.Errorf("%s %s denied = %v, want %v", tc.method, tc.path, got, tc.denied)
		}
	}
}
