package webhost

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Owner decision on #1345: a bearer is an agent token only if it carries the
// rat_ prefix. Any other Authorization header is not ours and is left to the
// session exactly as it was before tokens existed.

func withAuthorization(method, path, header string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", header)
	req.RemoteAddr = "127.0.0.1:5000"
	return req
}

// Falsify: treat every Bearer header as an agent token again.
func TestAForeignBearerHeaderIsLeftToTheSession(t *testing.T) {
	for name, header := range map[string]string{
		"proxy jwt":    "Bearer eyJhbGciOiJIUzI1NiJ9.e30.c2ln",
		"opaque":       "Bearer proxy-issued-opaque-value",
		"empty":        "Bearer ",
		"word":         "Bearer null",
		"upper prefix": "Bearer RAT_" + strings.Repeat("0", 30),
	} {
		t.Run(name, func(t *testing.T) {
			router, access := agentRouter()
			rec := serve(router, withAuthorization(http.MethodGet, "/api/v1/system/version", header))
			if rec.Code != http.StatusOK {
				t.Fatalf("a foreign Bearer header on a cookie session = %d %s, want 200", rec.Code, rec.Body.String())
			}
			if len(access.refusals) != 0 {
				t.Errorf("a foreign Bearer header was audited as an agent refusal: %+v", access.refusals)
			}
		})
	}
}

// A foreign Bearer on a denied prefix is not an agent either: the route's own
// session rules decide, not AGENT_ROUTE_DENIED.
func TestAForeignBearerOnADeniedPrefixIsNotRefusedAsAnAgent(t *testing.T) {
	router, access := agentRouter()
	rec := serve(router, withAuthorization(http.MethodGet, "/api/v1/agent/tokens", "Bearer proxy-issued-opaque-value"))
	if rec.Code != http.StatusOK {
		t.Fatalf("a session listing tokens behind a proxy Bearer = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if len(access.refusals) != 0 {
		t.Errorf("audited refusals = %+v, want none", access.refusals)
	}
}

// Falsify: key the prefix check on the scheme alone.
func TestAnRatBearerStillFailsClosedBesideAValidSession(t *testing.T) {
	router, access := agentRouter()
	rec := serve(router, withAuthorization(http.MethodGet, "/api/v1/system/version", "Bearer rat_unknown_token_00000000000000000000000"))
	if rec.Code != http.StatusUnauthorized || responseErrorCode(rec.Body.String()) != "AGENT_TOKEN_INVALID" {
		t.Fatalf("= %d %s, want 401 AGENT_TOKEN_INVALID", rec.Code, rec.Body.String())
	}
	if len(access.refusals) != 1 {
		t.Errorf("audited refusals = %+v, want one", access.refusals)
	}
}

// With a token store, the denied prefixes refuse an rat_ bearer under every
// scope (the existing table covers read and operate); without one, they
// refuse it too, so a deployment that keeps no tokens does not hand a
// session-only route to a header that claims to be one.
func TestADeniedPrefixRefusesAnRatBearerWithOrWithoutATokenStore(t *testing.T) {
	paths := []string{"/api/v1/system/first-run", "/api/v1/recoveries/abc123", "/api/v1/agent/tokens", "/api/v1/agent/tokens/agt_x/revoke"}

	withStore, _ := agentRouter()
	noStore := NewRouter(RouterConfig{Platform: allowingPlatform("alice"), Backend: newSyncFakeBackend(), BinaryVersion: "test", Commit: "test"})

	for name, router := range map[string]http.Handler{"with a store": withStore, "without a store": noStore} {
		for _, path := range paths {
			t.Run(name+" "+path, func(t *testing.T) {
				rec := serve(router, bearer(http.MethodGet, path, "", readToken))
				if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_ROUTE_DENIED" {
					t.Fatalf("= %d %s, want 403 AGENT_ROUTE_DENIED", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

// Falsify: make AgentDeniedByMount refuse every Authorization header again.
func TestTheAuthMountRefusesAnRatBearerAndPassesAForeignOne(t *testing.T) {
	reached := 0
	h := AgentDeniedByMount(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusNoContent)
	}))
	if rec := serve(h, withAuthorization(http.MethodPost, "/api/v1/auth/login", "Bearer rat_whatever_0000000000000000000000000000")); rec.Code != http.StatusForbidden || reached != 0 {
		t.Fatalf("rat_ bearer on the mount = %d reached=%d, want 403 and not reached", rec.Code, reached)
	}
	if rec := serve(h, withAuthorization(http.MethodPost, "/api/v1/auth/login", "Bearer proxy-issued-opaque-value")); rec.Code != http.StatusNoContent || reached != 1 {
		t.Fatalf("foreign bearer on the mount = %d reached=%d, want it to pass", rec.Code, reached)
	}
}
