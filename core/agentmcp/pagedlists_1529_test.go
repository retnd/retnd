package agentmcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/agenttools"
)

// #1529: AGENT_RESULT_TOO_LARGE is never a dead end for a read tool. A read
// whose answer is a list either has a route cursor (the route pages it, and
// takes a limit to narrow it) or the server pages it itself, with a cursor of
// its own, so an agent can always walk the whole answer. The artifact lists
// are #1528's and are driven here like the rest: the server's pager only
// steps in for a route that has no cursor, so the two do not meet.

type shape struct {
	tool       agenttools.Row
	arrays     []string
	routePaged bool
	hasLimit   bool
}

type schema struct {
	Ref        string            `json:"$ref"`
	Type       json.RawMessage   `json:"type"`
	Items      *schema           `json:"items"`
	Properties map[string]schema `json:"properties"`
}

// readShapes reads, from the contract, what each read tool's answer looks
// like: its top-level arrays, whether the route carries next_cursor, and
// whether it takes a limit.
func readShapes(t *testing.T) []shape {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "v1", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
			Responses map[string]struct {
				Content map[string]struct {
					Schema schema `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var out []shape
	for _, tool := range agenttools.Tools() {
		if tool.Scope != agenttools.ScopeRead {
			continue
		}
		op, ok := doc.Paths[callRoute(tool)]["get"]
		if !ok || len(op.Responses["200"].Content) == 0 {
			t.Fatalf("%s: no 200 response in the contract for %s", tool.Tool, callRoute(tool))
		}
		root := op.Responses["200"].Content["application/json"].Schema
		if root.Ref != "" {
			root = doc.Components.Schemas[strings.TrimPrefix(root.Ref, "#/components/schemas/")]
		}
		sh := shape{tool: tool}
		for name, prop := range root.Properties {
			if strings.Contains(string(prop.Type), `"array"`) {
				sh.arrays = append(sh.arrays, name)
			}
		}
		sort.Strings(sh.arrays)
		_, sh.routePaged = root.Properties["next_cursor"]
		for _, p := range op.Parameters {
			if p.In == "query" && p.Name == "limit" {
				sh.hasLimit = true
			}
		}
		out = append(out, sh)
	}
	return out
}

// walk answers a tool with a list far over the cap, from an engine that has
// no page of its own, and follows next_cursor until it ends. It returns what
// went wrong: a refusal, a repeated or skipped item, a cursor that reached the
// engine.
func walk(t *testing.T, sh shape, items, itemBytes int) []string {
	t.Helper()
	key := sh.arrays[0]
	body := func() json.RawMessage {
		var b strings.Builder
		b.WriteString(`{"` + key + `":[`)
		for i := 0; i < items; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"n":%d,"pad":%q}`, i, strings.Repeat("x", itemBytes))
		}
		b.WriteString("]}")
		return json.RawMessage(b.String())
	}()
	var problems []string
	caller := &fakeCaller{respond: func(c Call) (Response, error) {
		if c.Query.Get("before") != "" {
			problems = append(problems, "the engine was sent before="+c.Query.Get("before"))
		}
		return Response{Body: body, CorrelationID: "cid"}, nil
	}}
	cs := connectTools(t, []agenttools.Row{sh.tool}, caller, 0)
	args := map[string]any{}
	for _, p := range routeParams(callRoute(sh.tool)) {
		args[p] = "x"
	}
	var seen []int
	for page := 0; ; page++ {
		if page > 200 {
			return append(problems, "the cursor never ended")
		}
		res, got := call(t, cs, sh.tool.Tool, args)
		if !got.OK {
			return append(problems, "refused "+got.Code+": "+got.Message)
		}
		_ = res
		data, _ := json.Marshal(got.Data)
		if len(data) > DefaultMaxResultBytes {
			problems = append(problems, "a page is "+strconv.Itoa(len(data))+" bytes, over the cap")
		}
		var p map[string][]struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return append(problems, "the page does not decode: "+err.Error())
		}
		for _, it := range p[key] {
			seen = append(seen, it.N)
		}
		if got.NextCursor == "" {
			break
		}
		args["query"] = map[string]any{"before": got.NextCursor}
	}
	want := make([]int, items)
	for i := range want {
		want[i] = i
	}
	if !slices.Equal(seen, want) {
		problems = append(problems, fmt.Sprintf("the walk returned %d items, want each of %d once and in order", len(seen), items))
	}
	return problems
}

// Falsify: make the pager refuse (AGENT_RESULT_TOO_LARGE) as the server did
// before #1529, or have it cut the list without a cursor.
func TestEveryListReadToolCanBeWalkedWhenItsAnswerIsOverTheCap(t *testing.T) {
	driven := map[string]bool{}
	for _, sh := range readShapes(t) {
		if len(sh.arrays) == 0 {
			continue
		}
		if sh.routePaged {
			if !sh.hasLimit {
				t.Errorf("%s: the route pages with next_cursor and takes no limit, so the server has no smaller page to ask for", sh.tool.Tool)
			}
			continue
		}
		driven[sh.tool.Tool] = true
		t.Run(sh.tool.Tool, func(t *testing.T) {
			for _, problem := range walk(t, sh, 200, 1000) {
				t.Error(problem)
			}
		})
	}
	// A guard that walked nothing would pass. These are the reads this issue
	// is about, and the artifact lists #1528 owns.
	for _, must := range []string{"operations_list", "assurance_due", "retention_preview", "backups_list", "quarantine_list"} {
		if !driven[must] {
			t.Errorf("the walk did not drive %s, so this guard would pass without covering it", must)
		}
	}
}

// The control: one item that is itself over the cap is a real dead end, and
// the walk has to say so rather than pass.
func TestTheWalkSeesAnItemThatCanNeverFit(t *testing.T) {
	var sh shape
	for _, s := range readShapes(t) {
		if s.tool.Tool == "operations_list" {
			sh = s
		}
	}
	if len(sh.arrays) == 0 {
		t.Fatal("operations_list has no array in the contract")
	}
	if got := walk(t, sh, 3, DefaultMaxResultBytes+1); len(got) == 0 {
		t.Error("a list whose single item is over the cap was walked without a complaint")
	}
}

// A cursor is only good for the answer it came from. If the list moved, the
// next slice would skip or repeat items, so it is refused, not guessed.
func TestACursorFromAnAnswerThatHasMovedIsRefused(t *testing.T) {
	list := func(n int) json.RawMessage {
		var b strings.Builder
		b.WriteString(`{"operations":[`)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"n":%d,"pad":%q}`, i, strings.Repeat("x", 1000))
		}
		b.WriteString("]}")
		return json.RawMessage(b.String())
	}
	n := 200
	caller := &fakeCaller{respond: func(Call) (Response, error) { return Response{Body: list(n), CorrelationID: "cid"}, nil }}
	tools := []agenttools.Row{row("operations_list", "GET", "/operations", "Operations.")}
	cs := connectTools(t, tools, caller, 0)
	_, first := call(t, cs, "operations_list", nil)
	if !first.OK || first.NextCursor == "" {
		t.Fatalf("first page = %+v, want ok with a cursor", first)
	}
	n = 201
	res, got := call(t, cs, "operations_list", map[string]any{"query": map[string]any{"before": first.NextCursor}})
	if !res.IsError || got.OK || got.Code != CodeInvalidRequest || got.ExitCode != ExitUsage || got.Data != nil {
		t.Errorf("a cursor from a list that moved = %+v, want INVALID_REQUEST, exit 2 and no data", got)
	}
}

func TestACursorTheServerDidNotIssueIsAUsageError(t *testing.T) {
	caller := okBody(`{"operations":[]}`)
	tools := []agenttools.Row{row("operations_list", "GET", "/operations", "Operations.")}
	cs := connectTools(t, tools, caller, 0)
	for _, bad := range []string{"retnd-page:", "retnd-page:x:y", "retnd-page:-1:abc", "retnd-page:5"} {
		res, got := call(t, cs, "operations_list", map[string]any{"query": map[string]any{"before": bad}})
		if !res.IsError || got.Code != CodeInvalidRequest || got.ExitCode != ExitUsage {
			t.Errorf("before=%q gave %+v, want INVALID_REQUEST, exit 2", bad, got)
		}
	}
	if len(caller.calls) != 0 {
		t.Errorf("the engine was asked %d times for a cursor nobody issued", len(caller.calls))
	}
}
