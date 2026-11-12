package webhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// U1.5 (#1348) over HTTP: a bearer request that was served is an audit row,
// reads included, and the trail is paged by a signed-in person only. Each test
// names the mutation that has to turn it red.

// auditSpy records every call it is handed and answers the audit route from a
// fixed page, noting the query it was asked.
type auditSpy struct {
	mu      sync.Mutex
	calls   []service.AgentCall
	asked   []service.AgentAuditQuery
	entries []service.AgentAuditEntry
	next    string
}

func (a *auditSpy) RecordAgentCall(_ context.Context, c service.AgentCall) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, c)
	return nil
}

func (a *auditSpy) ListAgentAudit(_ context.Context, q service.AgentAuditQuery) ([]service.AgentAuditEntry, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, q)
	if q.Since == "banana" {
		return nil, "", service.ErrInvalidRequest
	}
	return a.entries, a.next, nil
}

type auditFakeBackend struct {
	*syncFakeBackend
	*fakeAgentAccess
	*auditSpy
}

func auditRouter() (http.Handler, *auditSpy) {
	spy := &auditSpy{}
	b := auditFakeBackend{syncFakeBackend: newSyncFakeBackend(), fakeAgentAccess: newFakeAgentAccess(), auditSpy: spy}
	return NewRouter(RouterConfig{Platform: allowingPlatform("alice"), Backend: b, BinaryVersion: "test", Commit: "test"}), spy
}

// Falsify: skip reads in the middleware, or record before the handler answers.
func TestABearerReadIsAnAuditRowWithItsAnswer(t *testing.T) {
	router, spy := auditRouter()
	rec := serve(router, bearer(http.MethodGet, "/api/v1/system/health", "", readToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("a read left %d audit calls, want exactly one", len(spy.calls))
	}
	c := spy.calls[0]
	if c.TokenID != "agt_read" || c.TokenName != "triage" || c.Transport != "api" || c.Status != http.StatusOK || c.Mutating || c.Refused {
		t.Errorf("call = %+v", c)
	}
	if c.CorrelationID == "" || c.CorrelationID != rec.Header().Get(CorrelationHeader) {
		t.Errorf("correlation id = %q, the response carried %q", c.CorrelationID, rec.Header().Get(CorrelationHeader))
	}
}

// Falsify: audit session requests as agent calls.
func TestASessionRequestIsNotAnAgentCall(t *testing.T) {
	router, spy := auditRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/version", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	if rec := serve(router, req); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(spy.calls) != 0 {
		t.Errorf("a person's request was recorded as %+v", spy.calls)
	}
}

// Falsify: drop the self-audited prefix. The MCP endpoint writes one row per
// tool call itself; a second per HTTP request is two rows for one call.
func TestTheMCPEndpointIsNotAuditedTwice(t *testing.T) {
	spy := &auditSpy{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := auditAgentCalls(spy)(next)
	for path, want := range map[string]int{"/api/v1/agent/mcp": 0, "/api/v1/status": 1} {
		spy.calls = nil
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(withAgentPrincipal(req.Context(), service.AgentPrincipal{TokenID: "agt_1", Name: "triage", Scope: "read"}))
		serve(h, req)
		if len(spy.calls) != want {
			t.Errorf("%s: %d audit calls, want %d", path, len(spy.calls), want)
		}
	}
}

// Falsify: mark a served mutation as a refusal, or a refusal as served.
func TestARefusedAndAMutatingCallAreMarkedAsSuch(t *testing.T) {
	spy := &auditSpy{}
	status := http.StatusOK
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status >= 400 {
			writeError(w, status, "CSRF_TOKEN_MISSING", "no")
			return
		}
		w.WriteHeader(status)
	})
	h := auditAgentCalls(spy)(next)
	for _, tc := range []struct {
		method   string
		status   int
		mutating bool
		refused  bool
		code     string
	}{
		{http.MethodGet, http.StatusOK, false, false, ""},
		{http.MethodPost, http.StatusAccepted, true, false, ""},
		{http.MethodPost, http.StatusForbidden, true, true, "CSRF_TOKEN_MISSING"},
		{http.MethodGet, http.StatusNotFound, false, false, "CSRF_TOKEN_MISSING"},
	} {
		spy.calls, status = nil, tc.status
		req := httptest.NewRequest(tc.method, "/api/v1/status", nil)
		req = req.WithContext(withAgentPrincipal(req.Context(), service.AgentPrincipal{TokenID: "agt_1", Name: "triage", Scope: "operate"}))
		serve(h, req)
		if len(spy.calls) != 1 {
			t.Fatalf("%s %d: %d calls", tc.method, tc.status, len(spy.calls))
		}
		if c := spy.calls[0]; c.Mutating != tc.mutating || c.Refused != tc.refused || c.Code != tc.code {
			t.Errorf("%s %d: mutating=%v refused=%v code=%q, want %v %v %q", tc.method, tc.status, c.Mutating, c.Refused, c.Code, tc.mutating, tc.refused, tc.code)
		}
	}
}

// Falsify: take /agent/audit off core/agenttools' deny list. A token never reads the
// record of what tokens did.
func TestATokenCannotReadTheAuditTrail(t *testing.T) {
	router, spy := auditRouter()
	for _, tok := range []string{readToken, operateToken} {
		rec := serve(router, bearer(http.MethodGet, "/api/v1/agent/audit", "", tok))
		if rec.Code != http.StatusForbidden || !bodyHasCode(rec, "AGENT_ROUTE_DENIED") {
			t.Errorf("token %s: %d %s, want 403 AGENT_ROUTE_DENIED", tok[:8], rec.Code, rec.Body.String())
		}
	}
	if len(spy.asked) != 0 {
		t.Error("the trail was read for a token")
	}
}

func bodyHasCode(rec *httptest.ResponseRecorder, code string) bool {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(rec.Body.Bytes(), &env) == nil && env.Error.Code == code
}

// Falsify: drop a query parameter, or the cursor, on the way to the service.
func TestThePersonsAuditRequestReachesTheServiceWholeAndAnswersTheContractShape(t *testing.T) {
	router, spy := auditRouter()
	spy.entries = []service.AgentAuditEntry{{Seq: 9, TokenID: "agt_1", Tool: "status", Transport: "stdio", Status: 200, CorrelationID: "cid_1"}}
	spy.next = "9"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/audit?token=agt_1&since=7d&before=40&limit=25", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	rec := serve(router, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(spy.asked) != 1 || spy.asked[0] != (service.AgentAuditQuery{TokenID: "agt_1", Since: "7d", Before: "40", Limit: 25}) {
		t.Fatalf("the service was asked %+v", spy.asked)
	}
	var got apicontract.ListAgentAuditResponse
	dec := json.NewDecoder(rec.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("the answer is not the contract's: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Seq != 9 || got.NextCursor != "9" || got.Entries[0].Tool != "status" {
		t.Errorf("answer = %+v", got)
	}
}

// Falsify: map the service's invalid request to a 500.
func TestASinceThatIsNoTimeIsRefusedInvalidRequest(t *testing.T) {
	router, _ := auditRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/audit?since=banana", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	rec := serve(router, req)
	if rec.Code != http.StatusBadRequest || !bodyHasCode(rec, "INVALID_REQUEST") {
		t.Fatalf("%d %s, want 400 INVALID_REQUEST", rec.Code, rec.Body.String())
	}
}

// Falsify: serve the trail from a backend that cannot keep one.
func TestABackendWithoutAnAuditTrailSaysSo(t *testing.T) {
	b := agentFakeBackend{syncFakeBackend: newSyncFakeBackend(), fakeAgentAccess: newFakeAgentAccess()}
	router := NewRouter(RouterConfig{Platform: allowingPlatform("alice"), Backend: b, BinaryVersion: "test", Commit: "test"})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/audit", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	if rec := serve(router, req); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 NOT_CONFIGURED", rec.Code)
	}
}

// Falsify: copy the headers through unchecked. A tool is a plain identifier and
// a transport is stdio or http; anything else is recorded as no tool and api.
func TestTheRowNamesTheToolAndTransportTheCallSaysAndNothingElse(t *testing.T) {
	for _, tc := range []struct{ tool, transport, wantTool, wantTransport string }{
		{"status", "stdio", "status", "stdio"},
		{"backup_set_show", "http", "backup_set_show", "http"},
		{"../etc/passwd", "stdio", "", "stdio"},
		{"Status", "carrier-pigeon", "", "api"},
		{"", "", "", "api"},
	} {
		router, spy := auditRouter()
		req := bearer(http.MethodGet, "/api/v1/system/health", "", readToken)
		if tc.tool != "" {
			req.Header.Set(service.AgentToolHeader, tc.tool)
		}
		if tc.transport != "" {
			req.Header.Set(service.AgentTransportHeader, tc.transport)
		}
		serve(router, req)
		if len(spy.calls) != 1 || spy.calls[0].Tool != tc.wantTool || spy.calls[0].Transport != tc.wantTransport {
			t.Errorf("tool %q transport %q: recorded %+v, want tool %q transport %q", tc.tool, tc.transport, spy.calls, tc.wantTool, tc.wantTransport)
		}
	}
}

// Falsify: check the deny list before the token. A valid token's probe of a
// route it may never reach is a row with its id; an invalid one is refused as
// an invalid token, which tells it nothing about the route.
func TestADeniedRouteProbeNamesTheToken(t *testing.T) {
	router, access := agentRouter()
	rec := serve(router, bearer(http.MethodGet, "/api/v1/agent/tokens", "", readToken))
	if rec.Code != http.StatusForbidden || !bodyHasCode(rec, "AGENT_ROUTE_DENIED") {
		t.Fatalf("= %d %s, want 403 AGENT_ROUTE_DENIED", rec.Code, rec.Body.String())
	}
	if len(access.refusals) != 1 || access.refusals[0].TokenID != "agt_read" || access.refusals[0].TokenName != "triage" {
		t.Fatalf("the probe was audited as %+v, want a refusal that names agt_read", access.refusals)
	}

	router, access = agentRouter()
	rec = serve(router, bearer(http.MethodGet, "/api/v1/agent/tokens", "", "rat_not_a_token_000000000000000000000000000"))
	if rec.Code != http.StatusUnauthorized || !bodyHasCode(rec, "AGENT_TOKEN_INVALID") {
		t.Fatalf("an invalid token on a denied route = %d %s, want 401 AGENT_TOKEN_INVALID", rec.Code, rec.Body.String())
	}
	if len(access.refusals) != 1 || access.refusals[0].TokenID != "" {
		t.Errorf("audited refusals = %+v, want one with no token", access.refusals)
	}
}
