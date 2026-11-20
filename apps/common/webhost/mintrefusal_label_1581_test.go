package webhost

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1581: the two mints share one transport check, and the action-log row a
// refusal leaves has to name the mint that was refused. The check used to
// hard-code "mint an agent token", so a refused scrape-token mint read in the
// log as somebody minting an agent token.
//
// Falsify: put the shared label back in requireMintTransportAck, or swap the
// two labels the routes pass.

// refusedMint sends one unacknowledged mint from another machine over plain
// HTTP and returns the action-log row it left.
func refusedMint(t *testing.T, router http.Handler, actions *recordingBackend, req *http.Request) (operation, code string) {
	t.Helper()
	req.RemoteAddr = "192.0.2.15:4000"
	rec := serve(router, csrfPaired(req))
	if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "INSECURE_TRANSPORT_ACK_REQUIRED" {
		t.Fatalf("an unacknowledged remote mint = %d %s, want 403 INSECURE_TRANSPORT_ACK_REQUIRED", rec.Code, rec.Body.String())
	}
	rows := actions.recorded()
	if len(rows) != 1 {
		t.Fatalf("one refused mint left %d action-log rows, want exactly 1: %+v", len(rows), rows)
	}

	return rows[0].Operation, rows[0].ErrorCode
}

func TestARefusedScrapeTokenMintIsLoggedAsAScrapeTokenMint(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	op, code := refusedMint(t, rig.admin, rig.actions,
		httptest.NewRequest(http.MethodPost, "/api/v1/settings/observability/scrape-token", nil))
	if code != "INSECURE_TRANSPORT_ACK_REQUIRED" {
		t.Errorf("the row's error code = %q, want INSECURE_TRANSPORT_ACK_REQUIRED", code)
	}
	if op != "mint a scrape token" {
		t.Errorf("a refused scrape-token mint is logged as operation %q, want %q", op, "mint a scrape token")
	}
	if strings.Contains(op, "agent") {
		t.Errorf("a refused scrape-token mint names an agent token in the action log: %q", op)
	}
}

func TestARefusedAgentTokenMintKeepsItsOwnLabel(t *testing.T) {
	access := newFakeAgentAccess()
	actions := &recordingBackend{}
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"),
		Backend:  agentFakeBackend{syncFakeBackend: newSyncFakeBackend(), fakeAgentAccess: access},
		Recorder: actions, BinaryVersion: "test", Commit: "test",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/tokens", strings.NewReader(`{"name":"triage"}`))
	req.Header.Set("Content-Type", "application/json")
	op, _ := refusedMint(t, router, actions, req)
	if op != "mint an agent token" {
		t.Errorf("a refused agent-token mint is logged as operation %q, want %q", op, "mint an agent token")
	}
	if len(access.minted) != 0 {
		t.Errorf("a refused mint reached the store: %+v", access.minted)
	}
}
