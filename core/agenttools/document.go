package agenttools

import (
	"slices"
	"strings"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
)

// Document is the catalogue as the API's own object. retnd agent tools --json
// prints it and GET /api/v1/agent/tools returns it, from this one function, so
// the two cannot say different things. It holds nothing about this deployment:
// it is the same bytes on every host running the same binary.
func Document() apicontract.AgentToolsResponse {
	out := apicontract.AgentToolsResponse{
		Tools:        []apicontract.AgentTool{},
		Resources:    make([]apicontract.AgentResource, 0, len(resources)),
		Prompts:      make([]apicontract.AgentPrompt, 0, len(prompts)),
		Refusals:     []apicontract.AgentToolRefusal{},
		DeniedFields: slices.Clone(DeniedFields),
	}
	for _, r := range Rows() {
		if r.Tool == "" {
			out.Refusals = append(out.Refusals, apicontract.AgentToolRefusal{
				Method: r.Method, Route: r.Route, Action: r.Action, Why: r.Why,
				Denied: Denied(r.Method, concrete(r.Route)),
			})
			continue
		}
		out.Tools = append(out.Tools, apicontract.AgentTool{
			Name: r.Tool, Scope: string(r.Scope), Method: r.Method, Route: r.Route, Action: r.Action,
			Description: r.Description, Twin: Twin(r), Untrusted: nonNil(r.Untrusted),
		})
	}
	for _, r := range Resources() {
		out.Resources = append(out.Resources, apicontract.AgentResource{Uri: r.URI, Tool: r.Tool, Description: r.Description})
	}
	for _, p := range Prompts() {
		out.Prompts = append(out.Prompts, apicontract.AgentPrompt{
			Name: p.Name, Description: p.Description, Arguments: nonNil(p.Arguments), Tools: nonNil(p.Tools),
		})
	}
	return out
}

// Twin is the terminal command cliecho composes for a tool's route, taken from
// the example the route's own parse test drives, so it is a command the binary
// parses. It is empty for a row with no command, which the guard refuses for a
// tool.
func Twin(r Row) string {
	for _, ex := range cliecho.Examples() {
		if ex.Method != r.Method || ex.Route != r.Route {
			continue
		}
		if r.Action != "" && !strings.Contains(string(ex.Body), `"action":"`+r.Action+`"`) {
			continue
		}
		return cliecho.Echo(ex).Shell()
	}
	return ""
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// concrete turns a route pattern into a path that fits it.
func concrete(pattern string) string {
	if base, ok := strings.CutSuffix(pattern, "/*"); ok {
		pattern = base + "/x"
	}
	segs := strings.Split(pattern, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segs[i] = "x"
		}
	}
	return strings.Join(segs, "/")
}
