package cliecho

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"

	"strings"
	"testing"
)

// The query-string half of the field check (#1253's review).
//
// A filter on a list is a request field like any other: a panel narrowed
// to one backup set and a line that lists every set are two different
// reads, and the operator copying the second one gets more than the panel
// showed. The body check can't see these, because a query parameter isn't
// in any request schema, so this reads them from api/v1/openapi.json, the
// one place they're declared.
//
// For each declared parameter of a route with a builder, the route is
// echoed with and without that parameter set, and the two lines have to
// differ. The value is one of the right kind: a canary for a free string,
// each declared value for an enum, a number for an integer, a timestamp
// for a date-time.

// queryParam is one declared query parameter and the values to try it
// with.
type queryParam struct {
	name   string
	values []string
}

// declaredQueryParams reads every operation's query parameters out of the
// contract document, keyed the way the routes table is.
func declaredQueryParams(t *testing.T) map[string][]queryParam {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "v1", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	type schema struct {
		Type   string   `json:"type"`
		Format string   `json:"format"`
		Enum   []string `json:"enum"`
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string][]queryParam{}
	for path, ops := range doc.Paths {
		for method, rawOp := range ops {
			var op struct {
				Parameters []struct {
					Name   string `json:"name"`
					In     string `json:"in"`
					Schema schema `json:"schema"`
				} `json:"parameters"`
			}
			if json.Unmarshal(rawOp, &op) != nil {
				continue
			}
			for _, p := range op.Parameters {
				if p.In != "query" {
					continue
				}
				var values []string
				switch {
				case len(p.Schema.Enum) > 0:
					values = p.Schema.Enum
				case p.Schema.Type == "integer" || p.Schema.Type == "number":
					values = []string{"7", "4321"}
				case p.Schema.Type == "boolean":
					values = []string{"true", "false"}
				case p.Schema.Format == "date-time":
					values = []string{"2026-10-01T00:00:00Z"}
				default:
					values = []string{fieldCanaryFor("query." + p.Name)}
				}
				k := key(strings.ToUpper(method), path)
				if override, ok := queryValues[k+" "+p.Name]; ok {
					values = override
				}
				out[k] = append(out[k], queryParam{name: p.Name, values: values})
			}
		}
	}
	return out
}

// queryParamsReachingTheLine is, for each route with a builder, each
// declared query parameter and whether setting it changed the line.
func queryParamsReachingTheLine(t *testing.T) fieldEchoes {
	t.Helper()
	params := map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar", "id": "offsite_s3", "run": "r-1", "step": "s-1"}
	out := fieldEchoes{}
	for route, declared := range declaredQueryParams(t) {
		e, ok := routes[route]
		if !ok || e.build == nil {
			continue
		}
		method, path, _ := strings.Cut(route, " ")
		base := Echo(Action{Method: method, Route: path, Params: params, Query: url.Values{}})
		out[route] = map[string]bool{}
		for _, p := range declared {
			out[route][p.name] = false
			for _, v := range p.values {
				line := Echo(Action{Method: method, Route: path, Params: params, Query: url.Values{p.name: {v}}})
				if !slices.Equal(line.Command, base.Command) || line.GapDetail != base.GapDetail {
					out[route][p.name] = true
				}
			}
		}
	}
	return out
}

func TestEveryQueryParameterReachesTheLineOrIsExplained(t *testing.T) {
	seen := queryParamsReachingTheLine(t)
	if len(seen) == 0 {
		t.Fatal("no route with a builder declares a query parameter, so this checked nothing")
	}
	for _, dropped := range droppedFields(seen, queryParamsNotEchoed) {
		at := strings.LastIndex(dropped, " ")
		t.Errorf("?%s is a query parameter %s declares, and setting it doesn't change the line. Echo it, or add it to queryParamsNotEchoed under %q with the reason.",
			dropped[at+1:], dropped[:at], dropped[:at])
	}
	for _, stale := range staleFieldReasons(seen, queryParamsNotEchoed) {
		t.Errorf("queryParamsNotEchoed explains %s. Delete the line.", stale)
	}
}

// The mutation: a filter the builder stops reading. GET /backups narrowed
// by setId is the one #1150 fixed, so it is the one to break.
func TestEveryQueryParameterReachesTheLine_MutationADroppedFilterIsCaught(t *testing.T) {
	const route = "GET /backups"
	saved := routes[route]
	t.Cleanup(func() { routes[route] = saved })
	routes[route] = entry{build: func(Action) *cmd { return newCmd("artifacts") }, examples: saved.examples}

	got := droppedFields(queryParamsReachingTheLine(t), queryParamsNotEchoed)
	if !slices.Contains(got, route+" setId") {
		t.Errorf("a builder that ignores ?setId was not reported; got %v", got)
	}
}

// queryValues is where the contract types a parameter more loosely than
// the route reads it, so the value of the right kind has to be written
// down: a string the route only honours when it is one of a few words, or
// a numeric token.
var queryValues = map[string][]string{
	// "all (the default), due or overdue", as prose rather than an enum.
	"GET /assurance/due status": {"all", "due", "overdue"},
	// A row-position token, typed as a string; the feed issues positive
	// row ids, and those are what the route reads.
	"GET /activity before": {"120"},
}

// queryParamsNotEchoed is every declared query parameter a builder leaves
// off its line on purpose, with the reason.
var queryParamsNotEchoed = map[string]map[string]string{
	"GET /activity/live": {
		"since":  "the browser's resume cursor into a feed it is already reading; a follow started at a terminal starts from now, and the verb has no flag for it",
		"before": "a page backwards through the record while the live feed is open; the terminal reads older events with `activity --before`, which is GET /activity's line, and this route's line stays the follow",
	},
	"GET /workflow-runs/{run}/steps/{step}/logs": {
		"after": "the browser's resume cursor into a log it is partway through; printing it would hand an operator a line that silently skips everything before it (the routes.go comment on this builder)",
	},
}
