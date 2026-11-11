package serve_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/retnd/retnd/apps/common/auth/local"
)

// U1.2 (#1345) end to end: the real engine, the real local authenticator and
// the real BackupService behind it. Nothing in this file is a double, which is
// what lets it say the three things the unit tests around it can only assume:
// a session mints a token, the token then authenticates /api/v1 on its own, and
// a revoke is felt on the very next request.

func agentCall(t *testing.T, client *http.Client, method, url, body string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func TestASessionMintsATokenThatThenAuthenticatesAloneUntilItIsRevoked(t *testing.T) {
	h := newEngineHarness(t)
	session := h.client
	base := h.server.URL
	enrollAndLogIn(t, h, session, base)
	pair := map[string]string{local.CSRFHeaderName: csrfToken(t, session, base)}

	status, body := agentCall(t, session, http.MethodPost, base+"/api/v1/agent/tokens", `{"name":"triage"}`, pair)
	if status != http.StatusCreated {
		t.Fatalf("mint = %d %s", status, body)
	}
	var minted struct {
		Token      string `json:"token"`
		AgentToken struct {
			ID     string `json:"id"`
			Prefix string `json:"prefix"`
			Scope  string `json:"scope"`
			Owner  string `json:"owner"`
		} `json:"agent_token"`
	}
	if err := json.Unmarshal([]byte(body), &minted); err != nil {
		t.Fatal(err)
	}
	if minted.AgentToken.Scope != "read" || minted.AgentToken.Owner == "" || !strings.HasPrefix(minted.Token, minted.AgentToken.Prefix) {
		t.Fatalf("minted = %s", body)
	}

	// The bearer alone, with no cookie jar: a fresh client.
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}
	bare := &http.Client{}
	if status, body := agentCall(t, bare, http.MethodGet, base+"/api/v1/system/version", "", bearer); status != http.StatusOK {
		t.Fatalf("a valid token = %d %s, want 200", status, body)
	}

	// It cannot reach the token routes, and it cannot mutate.
	if status, body := agentCall(t, bare, http.MethodPost, base+"/api/v1/agent/tokens", `{"name":"child"}`, bearer); status != http.StatusForbidden || !strings.Contains(body, "AGENT_ROUTE_DENIED") {
		t.Errorf("a token minting a token = %d %s, want 403 AGENT_ROUTE_DENIED", status, body)
	}
	if status, body := agentCall(t, bare, http.MethodPost, base+"/api/v1/backup-sets/src/set-1/enabled", `{}`, bearer); status != http.StatusForbidden || !strings.Contains(body, "AGENT_SCOPE_INSUFFICIENT") {
		t.Errorf("a read token mutating = %d %s, want 403 AGENT_SCOPE_INSUFFICIENT", status, body)
	}

	// The list shows it, once authenticated it has been used, and nothing
	// but the prefix of the token is there.
	status, list := agentCall(t, session, http.MethodGet, base+"/api/v1/agent/tokens", "", nil)
	if status != http.StatusOK || !strings.Contains(list, `"name":"triage"`) || strings.Contains(list, minted.Token) || strings.Contains(list, `"hash"`) {
		t.Fatalf("list = %d %s", status, list)
	}
	if strings.Contains(list, `"last_used_at":null`) {
		t.Errorf("an authenticated token reports no last use: %s", list)
	}

	// A revoke is felt on the very next request.
	if status, body := agentCall(t, session, http.MethodPost, base+"/api/v1/agent/tokens/"+minted.AgentToken.ID+"/revoke", "", pair); status != http.StatusOK {
		t.Fatalf("revoke = %d %s", status, body)
	}
	if status, body := agentCall(t, bare, http.MethodGet, base+"/api/v1/system/version", "", bearer); status != http.StatusUnauthorized || !strings.Contains(body, "AGENT_TOKEN_INVALID") {
		t.Errorf("a revoked token = %d %s, want 401 AGENT_TOKEN_INVALID", status, body)
	}
}

// /api/v1/auth is mounted beside the API router, so the router's own check
// never sees it. Falsify: wrap the auth routes in nothing.
func TestABearerOnTheAuthRoutesIsRefusedWhateverTheToken(t *testing.T) {
	h := newEngineHarness(t)
	bearer := map[string]string{"Authorization": "Bearer rat_whatever_0000000000000000000000000000"}
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/enroll", "/api/v1/auth/logout", "/api/v1/auth/session"} {
		status, body := agentCall(t, &http.Client{}, http.MethodPost, h.server.URL+path, `{}`, bearer)
		if status != http.StatusForbidden || !strings.Contains(body, "AGENT_ROUTE_DENIED") {
			t.Errorf("%s with a bearer = %d %s, want 403 AGENT_ROUTE_DENIED", path, status, body)
		}
	}
}

func TestAnUnknownBearerIsRefusedAndACookieSessionBesideItIsNotConsulted(t *testing.T) {
	h := newEngineHarness(t)
	enrollAndLogIn(t, h, h.client, h.server.URL)
	// h.client carries a valid session cookie, and the bearer is garbage:
	// the request is the agent's, and it is refused.
	status, body := agentCall(t, h.client, http.MethodGet, h.server.URL+"/api/v1/system/version", "", map[string]string{"Authorization": "Bearer rat_unknown_00000000000000000000000000000"})
	if status != http.StatusUnauthorized || !strings.Contains(body, "AGENT_TOKEN_INVALID") {
		t.Errorf("= %d %s, want 401 AGENT_TOKEN_INVALID", status, body)
	}
}
