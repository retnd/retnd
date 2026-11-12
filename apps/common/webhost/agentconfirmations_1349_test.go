package webhost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func (f *fakeConfirmations) ListAgentConfirmations(context.Context) ([]service.AgentConfirmation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []service.AgentConfirmation
	for _, c := range f.records {
		out = append(out, c)
	}
	return out, f.err
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

func (f *fakeConfirmations) FinishAgentApproval(_ context.Context, c service.AgentConfirmation, _ string, exec service.AgentExecution) error {
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
		BinaryVersion: "test", Commit: "test",
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
