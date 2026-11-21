package agentmcp

import (
	"testing"

	"github.com/retnd/retnd/core/agenttools"
)

// Issue #1594's review: backup_retirement_show is a read-scope tool with a
// free-form query, and the preview route reads the whole copy and runs the
// set's validator when it carries accept_unverified_copy. An acknowledgement
// is a person's, so the server refuses every acknowledgement name in any
// tool's query, in any letter case, before anything reaches the route.
func TestNoToolPassesAnAcknowledgementThroughItsQuery_1594(t *testing.T) {
	caller := okBody(`{}`)
	cs := connectTools(t, agenttools.Rows(), caller, 0)
	for _, r := range agenttools.Rows() {
		args := map[string]any{}
		for _, p := range routeParams(callRoute(r)) {
			args[p] = "x"
		}
		for _, key := range []string{"accept_unverified_copy", "Accept_Unverified_Copy", "accept_source_present", "no_successor"} {
			a := map[string]any{"query": map[string]any{key: "true"}}
			for k, v := range args {
				a[k] = v
			}
			res, got := call(t, cs, r.Tool, a)
			if !res.IsError || got.OK || got.Code != CodeInvalidRequest {
				t.Errorf("%s with query.%s = %+v, want a refused request", r.Tool, key, got)
			}
		}
	}
	if len(caller.calls) != 0 {
		t.Errorf("%d calls reached the route, want none: %+v", len(caller.calls), caller.calls[0])
	}
}
