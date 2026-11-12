package agenttools

import "testing"

// TestProposesAnswersOnlyForTheToolsOwnRoute holds the exact match U2.1b names
// to the real catalogue (#1379): a tool is proposed only with its own method,
// a route that fits its own, and, where several acts share the route, its own
// action word. The webhost tests use a table of their own, so without this a
// loosened match stays green.
func TestProposesAnswersOnlyForTheToolsOwnRoute(t *testing.T) {
	cases := []struct {
		name                       string
		tool, method, path, action string
		want                       bool
		scope                      Scope
	}{
		{"its own route and action", "run_cycle", "POST", "/operations", "run_cycle", true, ScopeOperate},
		{"another tool's route", "run_cycle", "POST", "/backup-sets", "run_cycle", false, ""},
		{"the wrong method", "run_cycle", "PUT", "/operations", "run_cycle", false, ""},
		{"another action on the shared route", "run_cycle", "POST", "/operations", "restore_snapshot", false, ""},
		{"no action on the shared route", "run_cycle", "POST", "/operations", "", false, ""},
		{"a read tool", "status", "GET", "/system/health", "", false, ""},
		{"an unknown tool", "no_such_tool", "POST", "/operations", "run_cycle", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scope, ok := Proposes(c.tool, c.method, c.path, c.action)
			if ok != c.want || scope != c.scope {
				t.Fatalf("Proposes(%q, %q, %q, %q) = (%q, %v), want (%q, %v)", c.tool, c.method, c.path, c.action, scope, ok, c.scope, c.want)
			}
		})
	}
}
