package webhost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// Review of the EPIC U stack: an MCP session belongs to the token that opened
// it. Another token that learns the session id cannot post into it, read its
// stream or end it. Falsify: drop the token binding from mcpEndpoint.
func TestAnMCPSessionIsBoundToTheTokenThatOpenedIt(t *testing.T) {
	router, _ := mcpRouter(service.AgentPolicy{HTTPTransport: true, RateLimitPerMinute: 60})
	rec := serve(router, mcpPost(readToken, initializeFrame))
	session := rec.Header().Get("Mcp-Session-Id")
	if rec.Code != http.StatusOK || session == "" {
		t.Fatalf("initialize = %d %s, session %q", rec.Code, rec.Body.String(), session)
	}

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req := mcpPost(operateToken, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
		req.Method = method
		req.Header.Set("Mcp-Session-Id", session)
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		if rec := serve(router, req); rec.Code != http.StatusForbidden {
			t.Errorf("%s by another token into the session = %d %s, want 403", method, rec.Code, rec.Body.String())
		}
	}

	req := mcpPost(readToken, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	req.Header.Set("Mcp-Session-Id", session)
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	if rec := serve(router, req); rec.Code >= 300 {
		t.Errorf("the owning token into its own session = %d %s", rec.Code, rec.Body.String())
	}
}

// An idle session ends, so a token that opens sessions and walks away cannot
// hold engine memory for ever (the envelope is not counted by the limiter).
// Falsify: leave StreamableHTTPOptions.SessionTimeout at zero.
func TestAnIdleMCPSessionEnds(t *testing.T) {
	prev := agentMCPSessionTimeout
	agentMCPSessionTimeout = 50 * time.Millisecond
	t.Cleanup(func() { agentMCPSessionTimeout = prev })

	router, _ := mcpRouter(service.AgentPolicy{HTTPTransport: true, RateLimitPerMinute: 60})
	rec := serve(router, mcpPost(readToken, initializeFrame))
	session := rec.Header().Get("Mcp-Session-Id")
	if session == "" {
		t.Fatalf("initialize = %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		req := mcpPost(readToken, `{"jsonrpc":"2.0","id":9,"method":"ping"}`)
		req.Header.Set("Mcp-Session-Id", session)
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		time.Sleep(200 * time.Millisecond)
		if rec := serve(router, req); rec.Code == http.StatusNotFound {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("an idle session was still served after five seconds")
		}
	}
}

// The frames that are not tool calls (initialize, tools/list, ping and the
// notifications) each authenticate and touch the token, so they count against
// the token's minute like a call does, and a tool call is still counted once,
// by the request it becomes.
// Falsify: stop counting the frames that are not calls, or count a tool call twice.
func TestTheFramesThatAreNotCallsAreRateLimitedToo(t *testing.T) {
	router, _ := mcpRouter(service.AgentPolicy{HTTPTransport: true, RateLimitPerMinute: 3})
	for i := 1; i <= 3; i++ {
		if rec := serve(router, mcpPost(readToken, initializeFrame)); rec.Code != http.StatusOK {
			t.Fatalf("initialize %d = %d %s", i, rec.Code, rec.Body.String())
		}
	}
	for _, frame := range []string{initializeFrame, `{"jsonrpc":"2.0","id":9,"method":"ping"}`, `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`} {
		rec := serve(router, mcpPost(readToken, frame))
		if rec.Code != http.StatusTooManyRequests || responseErrorCode(rec.Body.String()) != "AGENT_RATE_LIMITED" || rec.Header().Get("Retry-After") == "" {
			t.Errorf("frame %.40s over the line = %d %s, want 429 AGENT_RATE_LIMITED with Retry-After", frame, rec.Code, rec.Body.String())
		}
	}
	// Another token has its own minute.
	if rec := serve(router, mcpPost(operateToken, initializeFrame)); rec.Code != http.StatusOK {
		t.Errorf("a second token was limited by the first: %d", rec.Code)
	}

	// A tool call is counted once: initialize, the notification and the call
	// are three, so the fourth frame is the first one refused.
	router, _ = mcpRouter(service.AgentPolicy{HTTPTransport: true, RateLimitPerMinute: 4})
	rec := serve(router, mcpPost(readToken, initializeFrame))
	session := rec.Header().Get("Mcp-Session-Id")
	frames := []string{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"status","arguments":{}}}`, `{"jsonrpc":"2.0","id":3,"method":"ping"}`}
	for i, frame := range frames {
		req := mcpPost(readToken, frame)
		req.Header.Set("Mcp-Session-Id", session)
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		if rec = serve(router, req); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("frame %d (%.30s) was refused, a tool call counted twice: %s", i+2, frame, rec.Body.String())
		}
	}
	req := mcpPost(readToken, `{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	req.Header.Set("Mcp-Session-Id", session)
	if rec = serve(router, req); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the fifth frame = %d, want 429", rec.Code)
	}
}
