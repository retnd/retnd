package webhost

import (
	"net/http"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/agenttools"
)

// Q1 of the EPIC U panel (#1368): a bearer reaches only the routes the
// catalogue offers it. Safe methods used to pass untouched, so a token could
// read a workflow secret reference or a host map with a plain curl. Written
// red first.

// refusedReads lists the GET refusal rows of the catalogue: the routes a
// token has no tool to read, denied by the list or not.
func refusedReads() []agenttools.Row {
	var out []agenttools.Row
	for _, r := range agenttools.Rows() {
		if r.Tool == "" && r.Method == http.MethodGet && !agenttools.BearerReadable(r.Method, r.Route) {
			out = append(out, r)
		}
	}
	return out
}

// Falsify: let every safe method through in authenticateBearer again.
func TestABearerReadsOnlyWhatTheCatalogueOffers(t *testing.T) {
	rows := refusedReads()
	for _, must := range []string{"/system/version", "/settings/workflow", "/backup-sets/{source}/{set}/source-host", "/backup-sets/{source}/{set}/recovery-test/output", "/agent/tools"} {
		found := false
		for _, r := range rows {
			found = found || r.Route == must
		}
		if !found {
			t.Fatalf("the table has no GET refusal for %s, so this guard would pass without covering it", must)
		}
	}
	for _, row := range rows {
		path := "/api/v1" + concretePath(row.Route)
		for _, token := range []string{readToken, operateToken} {
			t.Run(row.Route+" "+token[:8], func(t *testing.T) {
				router, access := agentRouter()
				rec := serve(router, bearer(http.MethodGet, path, "", token))
				if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_ROUTE_DENIED" {
					t.Fatalf("GET %s = %d %s, want 403 AGENT_ROUTE_DENIED", path, rec.Code, rec.Body.String())
				}
				if len(access.refusals) != 1 || access.refusals[0].Code != "AGENT_ROUTE_DENIED" {
					t.Fatalf("audited refusals = %+v, want one AGENT_ROUTE_DENIED", access.refusals)
				}
				// A route the deny list names is refused before the token is
				// looked at, so its row names nobody. Any other refusal is a
				// token's, and says whose.
				if !agenttools.Denied(http.MethodGet, concretePath(row.Route)) && access.refusals[0].TokenID == "" {
					t.Errorf("the refusal of GET %s names no token", path)
				}
			})
		}
	}
}

// Every read tool's route is still reachable: the floor must not shut the
// tools it exists to serve. A 404 from the fake backend is an answer.
func TestABearerStillReadsEveryReadTool(t *testing.T) {
	for _, tool := range agenttools.Tools() {
		if tool.Method != http.MethodGet {
			continue
		}
		path := "/api/v1" + concretePath(tool.Route)
		t.Run(tool.Tool, func(t *testing.T) {
			router, access := agentRouter()
			rec := serve(router, bearer(http.MethodGet, path, "", readToken))
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Fatalf("GET %s = %d %s, a read tool's route must be reachable", path, rec.Code, rec.Body.String())
			}
			if len(access.refusals) != 0 {
				t.Errorf("a read the catalogue offers was audited as a refusal: %+v", access.refusals)
			}
		})
	}
}

// backup_set_show's pattern is a catch-all, and the router's pattern for a
// deeper path is its own. A read that matches a different pattern is not the
// tool's, however much the path looks like it.
//
// Falsify: allow a read whose path merely starts with a tool's route.
func TestAToolRouteIsNotABorrowedPrefix(t *testing.T) {
	for _, p := range []string{
		"/api/v1/backup-sets/src/set-1/workflow",
		"/api/v1/backup-sets/src/set-1/source-host",
		"/api/v1/backup-sets/src/set-1/recovery-test/output",
		"/api/v1/backup-sets/src/set-1/retention",
		"/api/v1/backup-sets/src/set-1/snapshots",
	} {
		t.Run(p, func(t *testing.T) {
			router, _ := agentRouter()
			rec := serve(router, bearer(http.MethodGet, p, "", readToken))
			if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_ROUTE_DENIED" {
				t.Fatalf("GET %s = %d %s, want 403 AGENT_ROUTE_DENIED", p, rec.Code, rec.Body.String())
			}
		})
	}
	router, _ := agentRouter()
	rec := serve(router, bearer(http.MethodGet, "/api/v1/backup-sets/src/set-1", "", readToken))
	if rec.Code == http.StatusForbidden {
		t.Errorf("backup_set_show's own route was refused: %s", rec.Body.String())
	}
}

// The other safe methods are no way round the floor, and neither is a path
// spelled so the router cleans it into a route the floor refuses.
func TestTheFloorHoldsForHeadOptionsAndSpelledPaths(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodHead, "/api/v1/system/version"},
		{http.MethodOptions, "/api/v1/settings"},
		{http.MethodGet, "/api/v1//system/version"},
		{http.MethodGet, "/api/v1/backups/../system/version"},
		{http.MethodGet, "/api/v1/system/%76ersion"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			router, _ := agentRouter()
			rec := serve(router, bearer(tc.method, tc.path, "", readToken))
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AGENT_ROUTE_DENIED") {
				t.Fatalf("%s %s = %d %s, want 403 AGENT_ROUTE_DENIED", tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// The MCP endpoint's own GET is a named extra on the floor: with the transport
// off it reaches the handler, which says so, rather than the floor refusing it
// as a route no tool sits on.
//
// Falsify: drop GET /agent/mcp from agenttools' bearer extras.
func TestTheMCPEndpointsGetPassesTheFloor(t *testing.T) {
	router, _ := agentRouter()
	rec := serve(router, bearer(http.MethodGet, "/api/v1/agent/mcp", "", readToken))
	if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_HTTP_TRANSPORT_OFF" {
		t.Fatalf("GET /api/v1/agent/mcp = %d %s, want 403 AGENT_HTTP_TRANSPORT_OFF", rec.Code, rec.Body.String())
	}
}
