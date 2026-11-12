package agentmcp

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resource is one retnd:// resource and the read tool that answers it. A
// template's variables bind to the tool's route placeholders in order.
type resource struct {
	uri, name, description, tool string
}

var resources = []resource{
	{"retnd://status", "status", "Overall backup health: what is stale, failing or fine.", "status"},
	{"retnd://backup-sets", "backup-sets", "Every backup set with its latest state.", "backup_sets_list"},
	{"retnd://backup-sets/{source}/{set}", "backup-set", "One backup set: its configuration and state.", "backup_set_show"},
	{"retnd://backups/{source}/{set}/{name}", "backup", "One backup and its copies.", "backup_show"},
	{"retnd://activity", "activity", "Recent backup lifecycle events, newest first.", "activity"},
	{"retnd://encryption", "encryption", "Whether retained copies are encrypted, and which keys are active. Never key material.", "encryption_state"},
	{"retnd://assurance/due", "assurance-due", "Scrubs and recovery tests that are due.", "assurance_due"},
}

// addResources registers every resource whose tool is in the catalogue. A
// resource read is the same call as the tool, answered as the same Result
// serialised as JSON text, so the two cannot disagree.
func (s *server) addResources(srv *mcp.Server) {
	for _, r := range resources {
		t, ok := s.tools[r.tool]
		if !ok {
			continue
		}
		handler := s.resourceHandler(t, r)
		if strings.Contains(r.uri, "{") {
			srv.AddResourceTemplate(&mcp.ResourceTemplate{Name: r.name, Description: r.description, URITemplate: r.uri, MIMEType: "application/json"}, handler)
			continue
		}
		srv.AddResource(&mcp.Resource{Name: r.name, Description: r.description, URI: r.uri, MIMEType: "application/json"}, handler)
	}
}

func (s *server) resourceHandler(t Tool, r resource) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		values, ok := templateValues(r.uri, req.Params.URI)
		if !ok {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		args := map[string]any{}
		for i, p := range routeParams(t.Route) {
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
