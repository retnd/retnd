package webhost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/agentmcp"
	"github.com/retnd/retnd/core/agenttools"
	"github.com/retnd/retnd/core/service"
)

// U1.4 (#1347) over HTTP: the streamable transport at /api/v1/agent/mcp, the
// per-token rate limit and the bound on invalid-token audit rows. Written
// red first; each test names the mutation that has to turn it red.

// policyBackend is the fake backend with an agents: block to report.
type policyBackend struct {
	agentFakeBackend
	policy service.AgentPolicy
}

func (p policyBackend) AgentPolicy() service.AgentPolicy { return p.policy }

func mcpRouter(policy service.AgentPolicy) (http.Handler, *fakeAgentAccess) {
	access := newFakeAgentAccess()
	b := policyBackend{agentFakeBackend: agentFakeBackend{syncFakeBackend: newSyncFakeBackend(), fakeAgentAccess: access}, policy: policy}
	return NewRouter(RouterConfig{Platform: allowingPlatform("alice"), Backend: b, BinaryVersion: "test", Commit: "test"}), access
}

const initializeFrame = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`

func mcpPost(token, body string) *http.Request {
	req := bearer(http.MethodPost, "/api/v1/agent/mcp", body, token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	return req
}

// Falsify: default agents.http_transport to on.
func TestTheMCPEndpointRefusesUntilEnabled(t *testing.T) {
	router, _ := mcpRouter(service.AgentPolicy{RateLimitPerMinute: 60})
	rec := serve(router, mcpPost(readToken, initializeFrame))
	if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_HTTP_TRANSPORT_OFF" {
		t.Fatalf("transport off = %d %s, want 403 AGENT_HTTP_TRANSPORT_OFF", rec.Code, rec.Body.String())
	}

	// A router whose backend reports no policy at all is the default
	// deployment, and the default is off.
	plain, _ := agentRouter()
	rec = serve(plain, mcpPost(readToken, initializeFrame))
	if responseErrorCode(rec.Body.String()) != "AGENT_HTTP_TRANSPORT_OFF" {
		t.Fatalf("no policy = %d %s, want AGENT_HTTP_TRANSPORT_OFF", rec.Code, rec.Body.String())
	}

	on, _ := mcpRouter(service.AgentPolicy{HTTPTransport: true, RateLimitPerMinute: 60})
	rec = serve(on, mcpPost(readToken, initializeFrame))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"serverInfo"`) {
		t.Fatalf("transport on = %d %s, want the initialize answer", rec.Code, rec.Body.String())
	}
}

// Falsify: accept a cookie session at the endpoint.
func TestTheMCPEndpointIsBearerOnly(t *testing.T) {
	router, _ := mcpRouter(service.AgentPolicy{HTTPTransport: true, RateLimitPerMinute: 60})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/mcp", strings.NewReader(initializeFrame))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := serve(router, csrfPaired(req))
	if rec.Code != http.StatusUnauthorized || responseErrorCode(rec.Body.String()) != "AGENT_TOKEN_INVALID" {
		t.Fatalf("a cookie session = %d %s, want 401 AGENT_TOKEN_INVALID", rec.Code, rec.Body.String())
	}

	if rec := serve(router, mcpPost(readToken, initializeFrame)); rec.Code != http.StatusOK {
		t.Fatalf("a bearer = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

func TestAReadTokenMayPostMCPFramesButStillStartsNothing(t *testing.T) {
	router, _ := mcpRouter(service.AgentPolicy{HTTPTransport: true, RateLimitPerMinute: 60})
	if rec := serve(router, mcpPost(readToken, initializeFrame)); rec.Code != http.StatusOK {
		t.Fatalf("a read token posting a frame = %d %s", rec.Code, rec.Body.String())
	}
	rec := serve(router, bearer(http.MethodPost, "/api/v1/operations", `{}`, readToken))
	if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_SCOPE_INSUFFICIENT" {
		t.Fatalf("a read token on a mutating route = %d %s, want 403 AGENT_SCOPE_INSUFFICIENT", rec.Code, rec.Body.String())
	}
}

// A tool call over HTTP is an ordinary /api/v1 request made as the token, and
// answers the same Result the stdio transport does.
func TestAToolCallOverHTTPAnswersTheResultObject(t *testing.T) {
	prev := agentToolRows
	agentToolRows = []agenttools.Row{{Tool: "status", Method: http.MethodGet, Route: "/system/health", Scope: agenttools.ScopeRead, Description: "The engine's health."}}
	t.Cleanup(func() { agentToolRows = prev })

	router, _ := mcpRouter(service.AgentPolicy{HTTPTransport: true, RateLimitPerMinute: 60})
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"status","arguments":{}}}`

	// A stateful session wants initialize first, then the id it was given.
	rec := serve(router, mcpPost(readToken, initializeFrame))
	session := rec.Header().Get("Mcp-Session-Id")
	for _, frame := range []string{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, call} {
		req := mcpPost(readToken, frame)
		req.Header.Set("Mcp-Session-Id", session)
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		rec = serve(router, req)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/call = %d %s", rec.Code, rec.Body.String())
	}
	var answer struct {
		Result struct {
			IsError           bool            `json:"isError"`
			StructuredContent agentmcp.Result `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("not a JSON-RPC answer: %v: %s", err, rec.Body.String())
	}
	got := answer.Result.StructuredContent
	if answer.Result.IsError || !got.OK || got.Code != "OK" {
		t.Errorf("result = %+v", got)
	}
	if got.CorrelationID == "" {
		t.Error("the result carries no correlation id")
	}
}

// Falsify: remove the per-token limiter.
func TestTheSixtyFirstCallInAMinuteIsRefused(t *testing.T) {
	router, access := mcpRouter(service.AgentPolicy{RateLimitPerMinute: 60})
	for i := 1; i <= 60; i++ {
		if rec := serve(router, bearer(http.MethodGet, "/api/v1/system/health", "", readToken)); rec.Code != http.StatusOK {
			t.Fatalf("call %d = %d %s, want 200", i, rec.Code, rec.Body.String())
		}
	}
	rec := serve(router, bearer(http.MethodGet, "/api/v1/system/health", "", readToken))
	if rec.Code != http.StatusTooManyRequests || responseErrorCode(rec.Body.String()) != "AGENT_RATE_LIMITED" {
		t.Fatalf("call 61 = %d %s, want 429 AGENT_RATE_LIMITED", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "wait ") {
		t.Errorf("the refusal gives no wait: header %q body %s", rec.Header().Get("Retry-After"), rec.Body.String())
	}
	last := access.refusals[len(access.refusals)-1]
	if last.Code != "AGENT_RATE_LIMITED" || last.TokenID != "agt_read" || last.Status != 429 {
		t.Errorf("audited %+v, want the limited token's AGENT_RATE_LIMITED row", last)
	}

	// The line is per token: another token has its own minute.
	if rec := serve(router, bearer(http.MethodGet, "/api/v1/system/health", "", operateToken)); rec.Code != http.StatusOK {
		t.Errorf("a second token was limited by the first: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTheConfiguredRateIsTheOneThatApplies(t *testing.T) {
	router, _ := mcpRouter(service.AgentPolicy{RateLimitPerMinute: 3})
	for i := 1; i <= 3; i++ {
		if rec := serve(router, bearer(http.MethodGet, "/api/v1/system/health", "", readToken)); rec.Code != http.StatusOK {
			t.Fatalf("call %d = %d", i, rec.Code)
		}
	}
	if rec := serve(router, bearer(http.MethodGet, "/api/v1/system/health", "", readToken)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("call 4 = %d, want 429", rec.Code)
	}
}

// Falsify: write a row for every refused token, or key the bound on the token.
func TestInvalidTokenAuditRowsAreBoundedPerAddress(t *testing.T) {
	router, access := mcpRouter(service.AgentPolicy{RateLimitPerMinute: 60})
	from := func(addr string) {
		req := bearer(http.MethodGet, "/api/v1/system/health", "", "rat_garbage_token_000000000000000000000000")
		req.RemoteAddr = addr
		rec := serve(router, req)
		if rec.Code != http.StatusUnauthorized || responseErrorCode(rec.Body.String()) != "AGENT_TOKEN_INVALID" {
			t.Fatalf("a garbage token from %s = %d %s, want 401 AGENT_TOKEN_INVALID", addr, rec.Code, rec.Body.String())
		}
	}
	for i := 0; i < 200; i++ {
		from("203.0.113.9:4000")
	}
	if got := len(access.refusals); got > invalidTokenRowsPerMinute {
		t.Fatalf("200 refusals from one address wrote %d rows, want at most %d", got, invalidTokenRowsPerMinute)
	}
	before := len(access.refusals)
	from("198.51.100.4:4000")
	if len(access.refusals) != before+1 {
		t.Errorf("a second address wrote %d new rows, want 1", len(access.refusals)-before)
	}
}

// Falsify: drop the cap, or never give a slot back.
func TestATokenHoldsAtMostTwoStreams(t *testing.T) {
	guard := newAgentGuard(service.AgentPolicy{RateLimitPerMinute: 60}, false)
	var releases []func()
	for i := 0; i < maxStreamsPerToken; i++ {
		release, ok := guard.openStream("agt_read")
		if !ok {
			t.Fatalf("stream %d was refused", i+1)
		}
		releases = append(releases, release)
	}
	if _, ok := guard.openStream("agt_read"); ok {
		t.Fatal("a third stream was allowed")
	}
	if _, ok := guard.openStream("agt_op"); !ok {
		t.Error("another token's stream was refused")
	}
	releases[0]()
	if _, ok := guard.openStream("agt_read"); !ok {
		t.Error("a released slot was not given back")
	}
}
