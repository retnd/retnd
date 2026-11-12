package webhost

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/agenttools"
)

// U1.3 (#1346) over the router: the catalogue holds every registered route,
// the deny list is enforced on the wire and not only by a transport, and the
// catalogue route answers the object the terminal prints.

// uncovered lists the registered routes the table has no row for. It reads
// every router a deployment mounts, as the route-to-verb guard does, and
// leaves the two health probes, which are outside /api/v1 and answered by
// core/cliecho.RecordedReasons.
func uncovered(registered map[string]bool, rows []agenttools.Row) []string {
	have := map[string]bool{}
	for _, r := range rows {
		have[r.Key()] = true
	}
	var out []string
	for route := range registered {
		if strings.Contains(route, " /health/") {
			continue
		}
		if !have[route] {
			out = append(out, route)
		}
	}
	sort.Strings(out)
	return out
}

// stale lists the rows for a route no router registers: a refusal that
// outlived its route.
func stale(registered map[string]bool, rows []agenttools.Row) []string {
	var out []string
	for _, r := range rows {
		if !registered[r.Key()] {
			out = append(out, r.Key())
		}
	}
	sort.Strings(out)
	return out
}

// twice lists the (method, route, action) answered by more than one row.
func twice(rows []agenttools.Row) []string {
	count := map[string]int{}
	for _, r := range rows {
		count[r.Key()+" "+r.Action]++
	}
	var out []string
	for k, n := range count {
		if n > 1 {
			out = append(out, strings.TrimSpace(k))
		}
	}
	sort.Strings(out)
	return out
}

// Falsify: add a route with neither a tool nor a refusal, or add a row twice.
func TestEveryAPIRouteIsAToolOrANamedRefusal(t *testing.T) {
	registered := everyRegisteredRoute(t)
	for _, must := range []string{"POST /operations", "GET /agent/tools", "POST /auth/login", "GET /backup-sets/*"} {
		if !registered[must] {
			t.Fatalf("the walk did not see %s, so this guard would pass without covering it", must)
		}
	}
	rows := agenttools.Rows()
	for _, route := range uncovered(registered, rows) {
		t.Errorf("%s is registered and core/agenttools has no row for it. Make it a tool, or a refusal that says why no agent reaches it.", route)
	}
	for _, route := range stale(registered, rows) {
		t.Errorf("core/agenttools has a row for %s and no router registers it", route)
	}
	for _, k := range twice(rows) {
		t.Errorf("%s is answered by more than one row", k)
	}
}

func TestTheCoverageGuardSeesAnUnansweredRouteAndADuplicate(t *testing.T) {
	registered := everyRegisteredRoute(t)
	registered["POST /a-route-nobody-answered"] = true
	if got := uncovered(registered, agenttools.Rows()); len(got) != 1 || got[0] != "POST /a-route-nobody-answered" {
		t.Errorf("a registered route with no row was not reported: %v", got)
	}

	dup := append(agenttools.Rows(), agenttools.Rows()[0])
	if got := twice(dup); len(got) != 1 {
		t.Errorf("a duplicated row was not reported: %v", got)
	}

	delete(registered, "GET /agent/tools")
	if got := stale(registered, agenttools.Rows()); len(got) == 0 {
		t.Error("a row for an unregistered route was not reported")
	}
}

// concretePath makes a request path out of a deny list pattern.
func concretePath(pattern string) string {
	pattern = strings.Replace(pattern, "/*", "/x", 1)
	for {
		i := strings.Index(pattern, "{")
		if i < 0 {
			return pattern
		}
		j := strings.Index(pattern[i:], "}")
		pattern = pattern[:i] + "x" + pattern[i+j+1:]
	}
}

// Falsify: enforce the deny list in a transport only, or drop an entry.
func TestABearerIsRefusedEveryRouteOnTheDenyListOnTheWire(t *testing.T) {
	for _, entry := range agenttools.DeniedRoutes() {
		method, pattern, _ := strings.Cut(entry, " ")
		methods := []string{method}
		if method == "*" {
			methods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete}
		}
		for _, m := range methods {
			for _, token := range []string{readToken, operateToken} {
				path := "/api/v1" + concretePath(pattern)
				t.Run(m+" "+path+" "+token[:8], func(t *testing.T) {
					router, access := agentRouter()
					rec := serve(router, bearer(m, path, "{}", token))
					if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_ROUTE_DENIED" {
						t.Fatalf("= %d %s, want 403 AGENT_ROUTE_DENIED", rec.Code, rec.Body.String())
					}
					if len(access.refusals) != 1 || access.refusals[0].Code != "AGENT_ROUTE_DENIED" {
						t.Errorf("audited refusals = %+v", access.refusals)
					}
				})
			}
		}
	}
}

// The routes that carry material, driven by name and not through the list's
// own patterns: this is the table a reviewer reads, and it is what keeps the
// list from being rewritten to match a mistake.
func TestABearerCannotReadKeyMaterialOrAnythingLikeIt(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/encryption/keys"},
		{http.MethodPost, "/api/v1/encryption/keys/k0123456789abcdef/export"},
		{http.MethodGet, "/api/v1/ssh-keys"},
		{http.MethodGet, "/api/v1/ssh/key-candidates"},
		{http.MethodGet, "/api/v1/storage-mediums/m1/configuration"},
		{http.MethodGet, "/api/v1/settings/workflow/environment"},
		{http.MethodGet, "/api/v1/backup-sets/src/set-1/workflow/environment"},
		{http.MethodGet, "/api/v1/workflow-runs/r1/steps/s1/logs"},
		{http.MethodPost, "/api/v1/backups/src/set-1/dump.tar/recovery"},
		{http.MethodGet, "/api/v1/encryption//keys"},
		{http.MethodGet, "/api/v1/system/../encryption/keys"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			router, _ := agentRouter()
			rec := serve(router, bearer(tc.method, tc.path, "", readToken))
			if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_ROUTE_DENIED" {
				t.Fatalf("= %d %s, want 403 AGENT_ROUTE_DENIED", rec.Code, rec.Body.String())
			}
		})
	}
}

// Falsify: consult the list only when a token store is wired.
func TestTheDenyListHoldsWithNoTokenStoreToo(t *testing.T) {
	h := authMiddleware(allowingPlatform("alice"), nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, p := range []string{"/api/v1/encryption/keys", "/api/v1/ssh-keys", "/api/v1/storage-credentials"} {
		rec := serve(h, bearer(http.MethodGet, p, "", readToken))
		if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_ROUTE_DENIED" {
			t.Errorf("%s with no token store = %d %s, want 403 AGENT_ROUTE_DENIED", p, rec.Code, rec.Body.String())
		}
	}
}

// A change with no tool is not put in front of a person to approve, whatever
// the token's scope. A read token is still told it is a read token.
//
// Falsify: drop the catalogue check and let every change reach the
// confirmation answer.
func TestAChangeWithNoToolIsRefusedOutright(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodPatch, "/api/v1/backup-sets/src/set-1"},
		{http.MethodPut, "/api/v1/backup-sets/src/set-1/encryption"},
		{http.MethodPost, "/api/v1/encryption/reencrypt"},
		{http.MethodPatch, "/api/v1/settings"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			router, access := agentRouter()
			rec := serve(router, bearer(tc.method, tc.path, "{}", operateToken))
			if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_ROUTE_DENIED" {
				t.Fatalf("operate token = %d %s, want 403 AGENT_ROUTE_DENIED", rec.Code, rec.Body.String())
			}
			if n := len(access.refusals); n != 1 || access.refusals[0].TokenID != "agt_op" {
				t.Errorf("the refusal was not audited against the token: %+v", access.refusals)
			}
			rec = serve(router, bearer(tc.method, tc.path, "{}", readToken))
			if responseErrorCode(rec.Body.String()) != "AGENT_SCOPE_INSUFFICIENT" {
				t.Errorf("read token = %d %s, want AGENT_SCOPE_INSUFFICIENT", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAChangeThatHasAToolStillAsksForAPerson(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/backup-sets/src/set-1/enabled"},
		{http.MethodPut, "/api/v1/backup-sets/src/set-1/retention"},
		{http.MethodDelete, "/api/v1/backup-sets/src/set-1/retention"},
		{http.MethodPatch, "/api/v1/backup-sets/src/set-1/assurance"},
		{http.MethodPost, "/api/v1/operations"},
		{http.MethodPost, "/api/v1/backups/src/set-1/dump.tar/retry"},
	} {
		router, _ := agentRouter()
		body := "{}"
		if strings.HasSuffix(tc.path, "/operations") {
			body = `{"action":"run_cycle"}`
		}
		rec := serve(router, bearer(tc.method, tc.path, body, operateToken))
		if rec.Code != http.StatusConflict || responseErrorCode(rec.Body.String()) != "AGENT_CONFIRMATION_REQUIRED" {
			t.Errorf("%s %s = %d %s, want 409 AGENT_CONFIRMATION_REQUIRED", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

// The operations route carries seven acts and the catalogue has a tool for two.
// A restore or a hold sent to it by a token is refused outright, not offered to
// a person, and the body is still there for the handler after the peek.
//
// Falsify: offer the whole route to a token that is allowed any of it.
func TestOnlyTheOperationsWithAToolAreOfferedOnTheSharedRoute(t *testing.T) {
	for _, tc := range []struct {
		body string
		code string
		want int
	}{
		{`{"action":"run_cycle","config_revision":"r1"}`, "AGENT_CONFIRMATION_REQUIRED", http.StatusConflict},
		{`{"action":"run_backup_set","backup_set_id":"src/set-1"}`, "AGENT_CONFIRMATION_REQUIRED", http.StatusConflict},
		{`{"action":"restore_placement","restore":{"artifact_id":"src/set-1/a","medium":"m"}}`, "AGENT_ROUTE_DENIED", http.StatusForbidden},
		{`{"action":"restore_snapshot"}`, "AGENT_ROUTE_DENIED", http.StatusForbidden},
		{`{"action":"hold_snapshot"}`, "AGENT_ROUTE_DENIED", http.StatusForbidden},
		{`{}`, "AGENT_ROUTE_DENIED", http.StatusForbidden},
		{`not json`, "AGENT_ROUTE_DENIED", http.StatusForbidden},
	} {
		router, _ := agentRouter()
		rec := serve(router, bearer(http.MethodPost, "/api/v1/operations", tc.body, operateToken))
		if rec.Code != tc.want || responseErrorCode(rec.Body.String()) != tc.code {
			t.Errorf("%s = %d %s, want %d %s", tc.body, rec.Code, rec.Body.String(), tc.want, tc.code)
		}
	}
}

func TestThePeekAtAnActionLeavesTheBodyForTheNextReader(t *testing.T) {
	const body = `{"action":"run_cycle","config_revision":"r1"}`
	req := bearer(http.MethodPost, "/api/v1/operations", body, operateToken)
	if !agentChangeOffered(req) {
		t.Fatal("run_cycle was not offered")
	}
	left, err := io.ReadAll(req.Body)
	if err != nil || string(left) != body {
		t.Errorf("the body after the peek = %q (%v), want it whole", left, err)
	}
}

// GET /api/v1/agent/tools answers the object agenttools.Document builds, which
// is the object retnd agent tools --json prints, for a session and for a
// bearer token alike.
//
// Falsify: drop one tool from the handler's answer.
func TestTheCatalogueRouteAnswersTheLibrarysObject(t *testing.T) {
	want, err := json.Marshal(agenttools.Document())
	if err != nil {
		t.Fatal(err)
	}
	router, _ := agentRouter()
	for name, req := range map[string]*http.Request{
		"session": httptest.NewRequest(http.MethodGet, "/api/v1/agent/tools", nil),
		"bearer":  bearer(http.MethodGet, "/api/v1/agent/tools", "", readToken),
	} {
		rec := serve(router, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: = %d %s", name, rec.Code, rec.Body.String())
		}
		var got any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		round, _ := json.Marshal(got)
		var wantAny any
		_ = json.Unmarshal(want, &wantAny)
		wantRound, _ := json.Marshal(wantAny)
		if string(round) != string(wantRound) {
			t.Errorf("%s: GET /agent/tools is not the catalogue document:\n got %s\nwant %s", name, round, wantRound)
		}
	}
}
