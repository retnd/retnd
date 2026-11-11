package webhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/service"
)

// U1.2 (#1345) over HTTP: the bearer authenticator, the scope check, the
// denied prefixes, the CSRF rule and the three token routes. Written red
// first; each test names the mutation that has to turn it red.

const (
	readToken      = "rat_read_token_0000000000000000000000000000"
	operateToken   = "rat_operate_token_00000000000000000000000000"
	revokedToken   = "rat_revoked_token_0000000000000000000000000"
	plaintextToken = "rat_minted_token_00000000000000000000000000"
)

// fakeAgentAccess is an honest in-memory stand-in for the service's token
// methods: it resolves a presented token against a table the test fills, and
// records everything it was asked to audit.
type fakeAgentAccess struct {
	mu       sync.Mutex
	known    map[string]service.AgentPrincipal
	refusals []service.AgentRefusal
	minted   []service.MintAgentTokenRequest
	revoked  []string
}

func newFakeAgentAccess() *fakeAgentAccess {
	return &fakeAgentAccess{known: map[string]service.AgentPrincipal{
		readToken:    {TokenID: "agt_read", Name: "triage", Scope: "read"},
		operateToken: {TokenID: "agt_op", Name: "runner", Scope: "operate"},
	}}
}

func (f *fakeAgentAccess) AuthenticateAgentToken(_ context.Context, presented string) (service.AgentPrincipal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.known[presented]; ok {
		return p, nil
	}
	return service.AgentPrincipal{}, service.ErrAgentTokenInvalid
}

func (f *fakeAgentAccess) RecordAgentRefusal(_ context.Context, r service.AgentRefusal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refusals = append(f.refusals, r)
	return nil
}

func (f *fakeAgentAccess) MintAgentToken(_ context.Context, req service.MintAgentTokenRequest) (service.MintedAgentToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.minted = append(f.minted, req)
	if req.Scope == "admin" {
		return service.MintedAgentToken{}, service.ErrInvalidRequest
	}
	return service.MintedAgentToken{Token: plaintextToken, AgentToken: service.AgentToken{
		ID: "agt_new", Name: req.Name, Prefix: plaintextToken[:8], Scope: "read", Owner: req.Owner,
		CreatedAt: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2027, 2, 9, 9, 0, 0, 0, time.UTC),
		Status: "active",
	}}, nil
}

func (f *fakeAgentAccess) ListAgentTokens(context.Context) ([]service.AgentToken, error) {
	return []service.AgentToken{{ID: "agt_read", Name: "triage", Prefix: readToken[:8], Scope: "read", Owner: "alice", Status: "active"}}, nil
}

func (f *fakeAgentAccess) RevokeAgentToken(_ context.Context, id string) (service.AgentToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != "agt_read" {
		return service.AgentToken{}, service.ErrAgentTokenNotFound
	}
	f.revoked = append(f.revoked, id)
	delete(f.known, readToken)
	return service.AgentToken{ID: id, Name: "triage", Prefix: readToken[:8], Scope: "read", Owner: "alice", Status: "revoked"}, nil
}

type agentFakeBackend struct {
	*syncFakeBackend
	*fakeAgentAccess
}

func agentRouter() (http.Handler, *fakeAgentAccess) {
	access := newFakeAgentAccess()
	b := agentFakeBackend{syncFakeBackend: newSyncFakeBackend(), fakeAgentAccess: access}
	return NewRouter(RouterConfig{Platform: allowingPlatform("alice"), Backend: b, BinaryVersion: "test", Commit: "test"}), access
}

func bearer(method, path, body, token string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "127.0.0.1:5000"
	return req
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAValidBearerTokenAuthenticatesAsTheAgent(t *testing.T) {
	router, access := agentRouter()
	rec := serve(router, bearer(http.MethodGet, "/api/v1/system/version", "", readToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(access.refusals) != 0 {
		t.Errorf("a valid token was audited as a refusal: %+v", access.refusals)
	}
}

func TestTheAuthenticatedActorIsAgentColonName(t *testing.T) {
	access := newFakeAgentAccess()
	var actor string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor = actorFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	h := authMiddleware(allowingPlatform("alice"), access)(next)
	if rec := serve(h, bearer(http.MethodGet, "/api/v1/system/version", "", readToken)); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if actor != "agent:triage" {
		t.Errorf("actor = %q, want agent:triage", actor)
	}
}

// Falsify: let a missing, unknown or revoked token fall through to the
// session, or skip the audit write.
func TestAnInvalidBearerIsRefusedBeforeAnyHandlerAndAudited(t *testing.T) {
	for name, header := range map[string]string{
		"unknown":       "Bearer rat_unknown_token_00000000000000000000000",
		"revoked":       "Bearer " + revokedToken,
		"empty":         "Bearer ",
		"lowercase":     "bearer rat_unknown_token_00000000000000000000000",
		"extra spaces":  "Bearer   rat_unknown_token_00000000000000000000000",
		"two tokens":    "Bearer " + readToken + " " + operateToken,
		"token as word": "Bearer null",
	} {
		t.Run(name, func(t *testing.T) {
			router, access := agentRouter()
			// The platform below would authenticate this request as alice
			// if the bearer header were ignored, which is the fall-through
			// this test exists to forbid.
			req := httptest.NewRequest(http.MethodGet, "/api/v1/system/version", nil)
			req.Header.Set("Authorization", header)
			rec := serve(router, req)
			if rec.Code != http.StatusUnauthorized || responseErrorCode(rec.Body.String()) != "AGENT_TOKEN_INVALID" {
				t.Fatalf("%s = %d %s, want 401 AGENT_TOKEN_INVALID", name, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "rat_") {
				t.Errorf("the refusal echoes the presented token: %s", rec.Body.String())
			}
			if len(access.refusals) != 1 || access.refusals[0].Code != "AGENT_TOKEN_INVALID" || access.refusals[0].Status != 401 {
				t.Errorf("audited refusals = %+v, want one AGENT_TOKEN_INVALID", access.refusals)
			}
			if access.refusals[0].CorrelationID == "" || rec.Header().Get(CorrelationHeader) != access.refusals[0].CorrelationID {
				t.Errorf("the audit row's correlation id %q is not the response's %q", access.refusals[0].CorrelationID, rec.Header().Get(CorrelationHeader))
			}
		})
	}
}

func TestANonBearerAuthorizationHeaderIsLeftToTheSession(t *testing.T) {
	router, _ := agentRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/version", nil)
	req.Header.Set("Authorization", "Basic YWxpY2U6c2VjcmV0")
	if rec := serve(router, req); rec.Code != http.StatusOK {
		t.Errorf("a Basic header changed a session request: %d %s", rec.Code, rec.Body.String())
	}
}

// Falsify: skip requireCSRF for every request.
func TestACookieSessionStillNeedsTheCSRFPair(t *testing.T) {
	router, _ := agentRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/tokens", strings.NewReader(`{"name":"triage"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(router, req)
	if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "CSRF_TOKEN_MISSING" {
		t.Fatalf("a session with no pair = %d %s, want 403 CSRF_TOKEN_MISSING", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/agent/tokens", strings.NewReader(`{"name":"triage"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = serve(router, csrfPaired(req))
	if rec.Code != http.StatusCreated {
		t.Errorf("a session with the pair = %d %s, want 201", rec.Code, rec.Body.String())
	}
}

func TestOnlyABearerPrincipalSkipsCSRF(t *testing.T) {
	reached := false
	h := requireCSRF(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	if rec := serve(h, req); rec.Code != http.StatusForbidden || reached {
		t.Fatalf("no principal, no cookies = %d reached=%v, want 403", rec.Code, reached)
	}
	req = httptest.NewRequest(http.MethodPost, "/x", nil)
	req = req.WithContext(withAgentPrincipal(req.Context(), service.AgentPrincipal{TokenID: "agt_read", Name: "triage", Scope: "read"}))
	if rec := serve(h, req); rec.Code != http.StatusOK || !reached {
		t.Errorf("bearer principal = %d reached=%v, want it to pass", rec.Code, reached)
	}
}

// Falsify: remove one prefix from the deny list, and the row for it fails.
func TestABearerOnADeniedPrefixIsRefusedUnderEveryScope(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/system/first-run"},
		{http.MethodPost, "/api/v1/system/first-run"},
		{http.MethodPost, "/api/v1/system/first-run/skip"},
		{http.MethodGet, "/api/v1/recoveries/abc123"},
		{http.MethodPost, "/api/v1/recoveries"},
		{http.MethodGet, "/api/v1/agent/tokens"},
		{http.MethodPost, "/api/v1/agent/tokens"},
		{http.MethodPost, "/api/v1/agent/tokens/agt_read/revoke"},
	} {
		for _, token := range []string{readToken, operateToken} {
			t.Run(tc.method+" "+tc.path+" "+token[:8], func(t *testing.T) {
				router, access := agentRouter()
				rec := serve(router, bearer(tc.method, tc.path, "{}", token))
				if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_ROUTE_DENIED" {
					t.Fatalf("= %d %s, want 403 AGENT_ROUTE_DENIED", rec.Code, rec.Body.String())
				}
				if len(access.refusals) != 1 || access.refusals[0].Code != "AGENT_ROUTE_DENIED" {
					t.Errorf("audited refusals = %+v", access.refusals)
				}
				if len(access.minted)+len(access.revoked) != 0 {
					t.Error("a denied route still reached the token store")
				}
			})
		}
	}
}

// Falsify: drop the scope check.
func TestAReadTokenStartsNothingAndChangesNothing(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/backup-sets/src/set-1/enabled"},
		{http.MethodPatch, "/api/v1/backup-sets/src/set-1"},
		{http.MethodPut, "/api/v1/backup-sets/src/set-1/encryption"},
		{http.MethodDelete, "/api/v1/backup-sets/src/set-1/encryption"},
		{http.MethodPost, "/api/v1/operations"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			router, _ := agentRouter()
			rec := serve(router, bearer(tc.method, tc.path, "{}", readToken))
			if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_SCOPE_INSUFFICIENT" {
				t.Fatalf("= %d %s, want 403 AGENT_SCOPE_INSUFFICIENT", rec.Code, rec.Body.String())
			}
		})
	}
}

// Until confirmations land (U2.1) an operate token has no path to a
// mutation either: it is told a person has to confirm it.
func TestAnOperateTokenCannotMutateDirectly(t *testing.T) {
	router, _ := agentRouter()
	rec := serve(router, bearer(http.MethodPost, "/api/v1/backup-sets/src/set-1/enabled", `{"enabled":false}`, operateToken))
	if rec.Code != http.StatusConflict || responseErrorCode(rec.Body.String()) != "AGENT_CONFIRMATION_REQUIRED" {
		t.Errorf("= %d %s, want 409 AGENT_CONFIRMATION_REQUIRED", rec.Code, rec.Body.String())
	}
}

func TestEveryRefusedBearerRequestStillGetsACorrelationID(t *testing.T) {
	router, _ := agentRouter()
	rec := serve(router, bearer(http.MethodGet, "/api/v1/system/version", "", "rat_unknown_token_00000000000000000000000"))
	if rec.Header().Get(CorrelationHeader) == "" {
		t.Error("no correlation id on a bearer refusal")
	}
}

// Falsify: let a bearer mint.
func TestATokenCannotMintAToken(t *testing.T) {
	router, access := agentRouter()
	rec := serve(router, bearer(http.MethodPost, "/api/v1/agent/tokens", `{"name":"child"}`, operateToken))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a bearer mint = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if len(access.minted) != 0 {
		t.Error("the mint reached the store")
	}
}

type mintedWire struct {
	Token      string         `json:"token"`
	AgentToken map[string]any `json:"agent_token"`
}

func TestASessionMintsATokenAndTheOwnerIsTheSessionUser(t *testing.T) {
	router, access := agentRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/tokens", strings.NewReader(`{"name":"triage","scope":"read","expires_in":"30d"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(router, csrfPaired(req))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got mintedWire
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Token != plaintextToken || got.AgentToken["name"] != "triage" {
		t.Errorf("body = %s", rec.Body.String())
	}
	if len(access.minted) != 1 || access.minted[0].Owner != "alice" || access.minted[0].ExpiresIn != "30d" || access.minted[0].Name != "triage" {
		t.Errorf("the store was asked %+v, want owner alice, 30d, triage", access.minted)
	}
}

func TestAMintWithAnInvalidScopeIsAnInvalidRequest(t *testing.T) {
	router, _ := agentRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/tokens", strings.NewReader(`{"name":"x","scope":"admin"}`))
	rec := serve(router, csrfPaired(req))
	if rec.Code != http.StatusBadRequest || responseErrorCode(rec.Body.String()) != "INVALID_REQUEST" {
		t.Errorf("= %d %s, want 400 INVALID_REQUEST", rec.Code, rec.Body.String())
	}
}

// Falsify: return the hash from GET /api/v1/agent/tokens.
func TestTheTokenListNeverCarriesTheHashOrMoreThanThePrefix(t *testing.T) {
	router, _ := agentRouter()
	rec := serve(router, httptest.NewRequest(http.MethodGet, "/api/v1/agent/tokens", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, banned := range []string{"hash", readToken, readToken[:9]} {
		if strings.Contains(body, banned) {
			t.Errorf("the list carries %q: %s", banned, body)
		}
	}
	if !strings.Contains(body, readToken[:8]) {
		t.Errorf("the list lacks the eight-character prefix: %s", body)
	}
}

func TestRevokeIsASessionPostAndTheNextBearerRequestIsRefused(t *testing.T) {
	router, access := agentRouter()
	if rec := serve(router, bearer(http.MethodGet, "/api/v1/system/version", "", readToken)); rec.Code != http.StatusOK {
		t.Fatalf("before the revoke: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/tokens/agt_read/revoke", nil)
	if rec := serve(router, csrfPaired(req)); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	if len(access.revoked) != 1 {
		t.Fatalf("revoked = %v", access.revoked)
	}
	rec := serve(router, bearer(http.MethodGet, "/api/v1/system/version", "", readToken))
	if rec.Code != http.StatusUnauthorized || responseErrorCode(rec.Body.String()) != "AGENT_TOKEN_INVALID" {
		t.Errorf("after the revoke = %d %s, want 401 AGENT_TOKEN_INVALID", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/agent/tokens/agt_nope/revoke", nil)
	rec = serve(router, csrfPaired(req))
	if rec.Code != http.StatusNotFound || responseErrorCode(rec.Body.String()) != "AGENT_TOKEN_NOT_FOUND" {
		t.Errorf("revoke of an unknown id = %d %s, want 404 AGENT_TOKEN_NOT_FOUND", rec.Code, rec.Body.String())
	}
}

// Falsify: only consult the deny list when a token store is wired. The
// first-run router has none, and /system/first-run is the route it serves.
func TestABearerOnADeniedPrefixIsRefusedWithNoTokenStore(t *testing.T) {
	h := authMiddleware(allowingPlatform("alice"), nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, p := range []string{"/api/v1/system/first-run", "/api/v1/system/first-run/skip", "/api/v1/recoveries"} {
		rec := serve(h, bearer(http.MethodPost, p, "{}", readToken))
		if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_ROUTE_DENIED" {
			t.Errorf("%s with no token store = %d %s, want 403 AGENT_ROUTE_DENIED", p, rec.Code, rec.Body.String())
		}
	}
	if rec := serve(h, bearer(http.MethodGet, "/api/v1/system/version", "", readToken)); rec.Code != http.StatusNoContent {
		t.Errorf("an open route with no token store = %d, want the header ignored as before", rec.Code)
	}
}

// A deployment that never minted a token: no Authorization header means the
// request meets exactly the checks it met before.
func TestARequestWithNoBearerHeaderIsUntouched(t *testing.T) {
	router, access := agentRouter()
	rec := serve(router, httptest.NewRequest(http.MethodGet, "/api/v1/system/version", nil))
	if rec.Code != http.StatusOK || len(access.refusals) != 0 {
		t.Errorf("= %d, refusals %+v", rec.Code, access.refusals)
	}
}
