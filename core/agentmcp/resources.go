package agentmcp

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/retnd/retnd/core/agenttools"
)

// addResources registers every catalogue resource whose tool this server
// offers. A resource read is the same call as its tool, answered as the same
// Result serialised as JSON text, so the two cannot disagree. A template's
// variables bind to the tool's route placeholders in order.
func (s *server) addResources(srv *mcp.Server) {
	for _, r := range agenttools.Resources() {
		t, ok := s.tools[r.Tool]
		if !ok {
			continue
		}
		handler := s.resourceHandler(t, r.URI)
		if strings.Contains(r.URI, "{") {
			srv.AddResourceTemplate(&mcp.ResourceTemplate{Name: r.Tool, Description: r.Description, URITemplate: r.URI, MIMEType: "application/json"}, handler)
			continue
		}
		srv.AddResource(&mcp.Resource{Name: r.Tool, Description: r.Description, URI: r.URI, MIMEType: "application/json"}, handler)
	}
}

func (s *server) resourceHandler(t agenttools.Row, template string) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		values, ok := templateValues(template, req.Params.URI)
		if !ok {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		args := map[string]any{}
		for i, p := range routeParams(callRoute(t)) {
			if i < len(values) {
				args[p] = values[i]
			}
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(s.run(withRequestHeader(ctx, req.GetExtra()), t, raw))
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "application/json", Text: string(encoded)}}}, nil
	}
}

// templateValues matches a uri against a template whose variables are whole
// path segments and returns their unescaped values in order.
func templateValues(template, uri string) ([]string, bool) {
	want := strings.Split(strings.TrimPrefix(template, "retnd://"), "/")
	rest, ok := strings.CutPrefix(uri, "retnd://")
	if !ok {
		return nil, false
	}
	got := strings.Split(rest, "/")
	if len(got) != len(want) {
		return nil, false
	}
	var values []string
	for i, w := range want {
		if strings.HasPrefix(w, "{") {
			v, err := url.PathUnescape(got[i])
			if err != nil || v == "" {
				return nil, false
			}
			values = append(values, v)
			continue
		}
		if w != got[i] {
			return nil, false
		}
	}
	return values, true
}
