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
func walk(t *testing.T, sh shape, key string, items, itemBytes int) []string {
	t.Helper()
	sh.tool.Untrusted = append(slices.Clone(sh.tool.Untrusted), key+"[].pad")
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
	var problems, pagesSeen []string
	caller := &fakeCaller{respond: func(c Call) (Response, error) {
		if c.Query.Get("before") != "" {
			problems = append(problems, "the engine was sent before="+c.Query.Get("before"))
		}
		if c.Query.Get("mcp_page") != "" {
			pagesSeen = append(pagesSeen, c.Query.Get("mcp_page"))
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
		moved, _ := json.Marshal(got.Untrusted)
		if len(data)+len(moved) > DefaultMaxResultBytes {
			problems = append(problems, "a page is "+strconv.Itoa(len(data)+len(moved))+" bytes with its untrusted values, over the cap")
		}
		var p map[string][]struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return append(problems, "the page does not decode: "+err.Error())
		}
		first := len(seen)
		for _, it := range p[key] {
			seen = append(seen, it.N)
		}
		// Untrusted pointers name an item's place in the whole list, not on
		// its page: the pad of item n is at /<array>/<n>/pad.
		for i := first; i < len(seen); i++ {
			if _, ok := got.Untrusted["/"+key+"/"+strconv.Itoa(seen[i])+"/pad"]; !ok {
				problems = append(problems, fmt.Sprintf("item %d has no untrusted pointer /%s/%d/pad on its page", seen[i], key, seen[i]))
				break
			}
		}
		// A list that is itself named untrusted (a set's include patterns)
		// leaves the data and arrives whole under its pointer.
		if moved, ok := got.Untrusted["/"+key]; ok {
			raw, _ := json.Marshal(moved)
			var items []struct {
				N int `json:"n"`
			}
			if err := json.Unmarshal(raw, &items); err != nil {
				return append(problems, "the untrusted list does not decode: "+err.Error())
			}
			for _, it := range items {
				seen = append(seen, it.N)
			}
		}
		if got.NextCursor == "" {
			break
		}
		args["query"] = map[string]any{"before": got.NextCursor}
	}
	if items > 0 && len(pagesSeen) == 0 && len(seen) > 0 && len(seen) < items {
		problems = append(problems, "later pages did not tell the engine where in the list they were")
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

// walkRouted is the same walk for a route that pages itself: it answers
// `limit` items from the row named by before, with a next_cursor, and the
// server has to halve the limit until a page fits and follow the cursor.
func walkRouted(t *testing.T, sh shape, key string, items, itemBytes int) []string {
	t.Helper()
	pad := strings.Repeat("x", itemBytes)
	caller := &fakeCaller{respond: func(c Call) (Response, error) {
		start, limit := 0, 200
		if b := c.Query.Get("before"); b != "" {
			start, _ = strconv.Atoi(strings.TrimPrefix(b, "row-"))
		}
		if l := c.Query.Get("limit"); l != "" {
			limit, _ = strconv.Atoi(l)
		}
		end := min(start+limit, items)
		var b strings.Builder
		b.WriteString(`{"` + key + `":[`)
		for i := start; i < end; i++ {
			if i > start {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"n":%d,"pad":%q}`, i, pad)
		}
		next := ""
		if end < items {
			next = "row-" + strconv.Itoa(end)
		}
		b.WriteString(`],"next_cursor":` + strconv.Quote(next) + `}`)
		return Response{Body: json.RawMessage(b.String()), CorrelationID: "cid"}, nil
	}}
	cs := connectTools(t, []agenttools.Row{sh.tool}, caller, 0)
	args := map[string]any{}
	for _, p := range routeParams(callRoute(sh.tool)) {
		args[p] = "x"
	}
	var seen []int
	for page := 0; page < 400; page++ {
		_, got := call(t, cs, sh.tool.Tool, args)
		if !got.OK {
			return []string{"refused " + got.Code + ": " + got.Message}
		}
		data, _ := json.Marshal(got.Data)
		var p map[string]json.RawMessage
		if err := json.Unmarshal(data, &p); err != nil {
			return []string{"the page does not decode: " + err.Error()}
		}
		var its []struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(p[key], &its); err != nil {
			return []string{"the array does not decode: " + err.Error()}
		}
		for _, it := range its {
			seen = append(seen, it.N)
		}
		if got.NextCursor == "" {
			break
		}
		args["query"] = map[string]any{"before": got.NextCursor}
	}
	if len(seen) != items {
		return []string{fmt.Sprintf("the routed walk returned %d items, want %d", len(seen), items)}
	}
	for i, n := range seen {
		if n != i {
			return []string{"the routed walk skipped or repeated an item"}
		}
	}
	return nil
}

// Falsify: make the pager refuse (AGENT_RESULT_TOO_LARGE) as the server did
// before #1529, or have it cut the list without a cursor.
func TestEveryListReadToolCanBeWalkedWhenItsAnswerIsOverTheCap(t *testing.T) {
	driven := map[string]bool{}
	for _, sh := range readShapes(t) {
		if len(sh.arrays) == 0 {
			continue
		}
		if sh.routePaged && !sh.hasLimit {
			t.Errorf("%s: the route pages with next_cursor and takes no limit, so the server has no smaller page to ask for", sh.tool.Tool)
			continue
		}
		driven[sh.tool.Tool] = true
		// Each array of the answer takes a turn as the one that is too big.
		for _, key := range sh.arrays {
			t.Run(sh.tool.Tool+"/"+key, func(t *testing.T) {
				var problems []string
				if sh.routePaged {
					problems = walkRouted(t, sh, key, 200, 1000)
				} else {
					problems = walk(t, sh, key, 200, 1000)
				}
				for _, problem := range problems {
					t.Error(problem)
				}
			})
		}
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
	if got := walk(t, sh, sh.arrays[0], 3, DefaultMaxResultBytes+1); len(got) == 0 {
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
	for _, bad := range []string{"retnd-page:", "retnd-page:x:y", "retnd-page:-1:abc", "retnd-page:5", "retnd-page:5:abc:operations", "retnd-page:5:" + strings.Repeat("a", 32) + ":"} {
		res, got := call(t, cs, "operations_list", map[string]any{"query": map[string]any{"before": bad}})
		if !res.IsError || got.Code != CodeInvalidRequest || got.ExitCode != ExitUsage {
			t.Errorf("before=%q gave %+v, want INVALID_REQUEST, exit 2", bad, got)
		}
	}
	if len(caller.calls) != 0 {
		t.Errorf("the engine was asked %d times for a cursor nobody issued", len(caller.calls))
	}
}

// A live list keeps its cursor while its rows change underneath it: the
// fingerprint is the items' identity, not their progress.
func TestACursorSurvivesAVolatileFieldAndNothingElse(t *testing.T) {
	progress := 0
	list := func() json.RawMessage {
		var b strings.Builder
		b.WriteString(`{"operations":[`)
		for i := 0; i < 200; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"id":"op-%d","progress":%d,"pad":%q}`, i, progress, strings.Repeat("x", 1000))
		}
		b.WriteString("]}")
		return json.RawMessage(b.String())
	}
	caller := &fakeCaller{respond: func(Call) (Response, error) { return Response{Body: list(), CorrelationID: "cid"}, nil }}
	cs := connectTools(t, []agenttools.Row{row("operations_list", "GET", "/operations", "Operations.")}, caller, 0)
	_, first := call(t, cs, "operations_list", nil)
	progress = 7
	_, second := call(t, cs, "operations_list", map[string]any{"query": map[string]any{"before": first.NextCursor}})
	if !second.OK {
		t.Fatalf("a progress counter moved and the cursor was refused: %+v", second)
	}
	if !strings.Contains(second.Command, "# items from ") {
		t.Errorf("page two's command %q does not say where in the list it is", second.Command)
	}
	last := caller.calls[len(caller.calls)-1]
	if last.Query.Get("mcp_page") == "" || last.Query.Get("before") != "" {
		t.Errorf("the engine was asked %v, want mcp_page set and no before", last.Query)
	}
	if strings.Contains(first.NextCursor, "op-") || len(strings.Split(first.NextCursor, ":")) != 4 {
		t.Errorf("cursor %q should be prefix, offset, tag and array and carry no item", first.NextCursor)
	}
}
