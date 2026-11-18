package agentcorpus

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Surface is one thing an agent can read, as the bytes a client receives.
type Surface struct {
	// Name is the row: "tools/list", "tool:backups_list", "resource:retnd://status", "prompt:triage-health".
	Name string
	Kind string // "list", "tool", "resource", "prompt"
	Tool string // the tool, for Kind "tool"
	Raw  []byte
	// Error is set when the server answered with a JSON-RPC error. An error
	// carries nothing, which is why a corpus row also asserts it saw data.
	Error *RPCError
}

// argValues is what a corpus call supplies for a property by name. The
// catalogue's schemas are read at run time and only the properties they
// declare are sent, so a renamed property drops out instead of failing the
// whole walk.
var argValues = map[string]any{
	"source": "production", "set": "postgres-primary", "backup_set": "production/postgres-primary",
	"name": "backup.dump", "artifact": "production/postgres-primary/backup.dump", "id": "offsite",
	"run": "run-1", "run_id": "run-1", "step": "step-1", "op": "op-1", "operation_id": "op-1", "medium": "offsite", "limit": 10,
}

func argsFor(schema json.RawMessage) map[string]any {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(schema, &s)
	out := map[string]any{}
	for k := range s.Properties {
		if v, ok := argValues[k]; ok {
			out[k] = v
		}
	}
	return out
}

// ReadTools is the read tools the spec's names table lists (section 12).
var ReadTools = []string{
	"status", "backup_sets_list", "backup_set_show", "backups_list", "backup_show", "copies_show", "encryption_state",
	"assurance_due", "assurance_show", "activity", "activity_live", "quarantine_list", "operations_list", "operation_show",
	"storage_mediums_list", "storage_usage", "retention_preview", "workflow_runs_list", "workflow_run_show",
}

// Walk reads every surface the session offers: the three listings, every
// read tool, every resource and resource template, every prompt. A tool the
// catalogue lacks is skipped rather than failed, because the row that needs
// it fails on its own with a name.
func Walk(t *testing.T, c Conn) []Surface {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var out []Surface
	add := func(name, kind, tool string, raw json.RawMessage, e *RPCError) {
		out = append(out, Surface{Name: name, Kind: kind, Tool: tool, Raw: raw, Error: e})
	}
	call := func(method string, params any) (json.RawMessage, *RPCError) {
		raw, rpcErr, err := c.Call(ctx, method, params)
		if err != nil {
			t.Fatalf("%s over %s: %v", method, c.Transport(), err)
		}
		return raw, rpcErr
	}

	raw, e := call("tools/list", map[string]any{})
	add("tools/list", "list", "", raw, e)
	var tools struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(raw, &tools)
	have := map[string]json.RawMessage{}
	for _, tl := range tools.Tools {
		have[tl.Name] = tl.InputSchema
	}
	for _, name := range ReadTools {
		schema, ok := have[name]
		if !ok {
			continue
		}
		raw, e := call("tools/call", map[string]any{"name": name, "arguments": argsFor(schema)})
		add("tool:"+name, "tool", name, raw, e)
	}

	raw, e = call("resources/list", map[string]any{})
	add("resources/list", "list", "", raw, e)
	var res struct {
		Resources []struct {
			URI string `json:"uri"`
		} `json:"resources"`
	}
	_ = json.Unmarshal(raw, &res)
	raw, e = call("resources/templates/list", map[string]any{})
	add("resources/templates/list", "list", "", raw, e)
	var tpl struct {
		Templates []struct {
			URITemplate string `json:"uriTemplate"`
		} `json:"resourceTemplates"`
	}
	_ = json.Unmarshal(raw, &tpl)
	uris := map[string]bool{}
	for _, r := range res.Resources {
		uris[r.URI] = true
	}
	for _, tp := range tpl.Templates {
		u := tp.URITemplate
		for _, p := range [][2]string{{"{source}", "production"}, {"{set}", "postgres-primary"}, {"{name}", "backup.dump"}} {
			u = strings.ReplaceAll(u, p[0], p[1])
		}
		uris[u] = true
	}
	for u := range uris {
		raw, e := call("resources/read", map[string]any{"uri": u})
		add("resource:"+u, "resource", "", raw, e)
	}

	raw, e = call("prompts/list", map[string]any{})
	add("prompts/list", "list", "", raw, e)
	var prompts struct {
		Prompts []struct {
			Name      string `json:"name"`
			Arguments []struct {
				Name string `json:"name"`
			} `json:"arguments"`
		} `json:"prompts"`
	}
	_ = json.Unmarshal(raw, &prompts)
	for _, p := range prompts.Prompts {
		args := map[string]string{}
		for _, a := range p.Arguments {
			if v, ok := argValues[a.Name].(string); ok {
				args[a.Name] = v
			}
		}
		raw, e := call("prompts/get", map[string]any{"name": p.Name, "arguments": args})
		add("prompt:"+p.Name, "prompt", "", raw, e)
	}
	return out
}
