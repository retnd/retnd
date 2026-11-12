package agenttools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Q1 of the EPIC U panel (#1368): BearerReadable is the allowlist the wire
// reads for a safe method. Written red first.

// Falsify: match by prefix, or answer true for a refusal row.
func TestBearerReadableIsTheReadToolRoutesAndNothingElse(t *testing.T) {
	for _, r := range Rows() {
		got := BearerReadable(r.Method, r.Route)
		want := r.Tool != "" && r.Method == "GET"
		if got != want {
			t.Errorf("BearerReadable(%s, %s) = %v, want %v", r.Method, r.Route, got, want)
		}
	}
	for _, c := range []struct{ method, pattern string }{
		{"GET", "/backup-sets/{source}/{set}/workflow"},
		{"GET", "/backup-sets/{source}/{set}/source-host"},
		{"GET", "/backup-sets/x"},
		{"GET", "/system/version"},
		{"GET", "/backups/{source}/{set}"},
		{"GET", ""},
		{"HEAD", "/system/health"},
		{"OPTIONS", "/system/health"},
		{"POST", "/system/health"},
	} {
		if BearerReadable(c.method, c.pattern) {
			t.Errorf("BearerReadable(%s, %q) is true, want false", c.method, c.pattern)
		}
	}
}

// A refusal row never has a tool on its route, so the allowlist and the
// refusals cannot overlap.
func TestNoRefusalRowIsBearerReadable(t *testing.T) {
	for _, r := range Rows() {
		if r.Tool == "" && BearerReadable(r.Method, r.Route) {
			t.Errorf("%s is a refusal and bearer readable", r.Key())
		}
	}
}

// ---------------------------------------------------- the response schemas ---

type fullSchema struct {
	Ref                  string                `json:"$ref"`
	Items                *fullSchema           `json:"items"`
	Properties           map[string]fullSchema `json:"properties"`
	AllOf                []fullSchema          `json:"allOf"`
	OneOf                []fullSchema          `json:"oneOf"`
	AnyOf                []fullSchema          `json:"anyOf"`
	AdditionalProperties json.RawMessage       `json:"additionalProperties"`
}

// deniedFieldsIn walks a response schema, through $ref, items and the
// combinators, and reports every property the projection would drop, with the
// path it sits at. visited holds the refs on the current path, so a schema
// that refers to itself ends.
func deniedFieldsIn(s fullSchema, at string, all map[string]fullSchema, visited map[string]bool) []string {
	if s.Ref != "" {
		name := strings.TrimPrefix(s.Ref, "#/components/schemas/")
		if visited[name] {
			return nil
		}
		visited[name] = true
		defer delete(visited, name)
		s = all[name]
	}
	var out []string
	for key, p := range s.Properties {
		if deniedField(key) {
			out = append(out, at+"."+key)
		}
		out = append(out, deniedFieldsIn(p, at+"."+key, all, visited)...)
	}
	if s.Items != nil {
		out = append(out, deniedFieldsIn(*s.Items, at+"[]", all, visited)...)
	}
	for _, group := range [][]fullSchema{s.AllOf, s.OneOf, s.AnyOf} {
		for _, g := range group {
			out = append(out, deniedFieldsIn(g, at, all, visited)...)
		}
	}
	if len(s.AdditionalProperties) > 0 {
		var extra fullSchema
		if json.Unmarshal(s.AdditionalProperties, &extra) == nil {
			out = append(out, deniedFieldsIn(extra, at+"{}", all, visited)...)
		}
	}
	return out
}

// Falsify: add a field named access_token, or a recipient, to the response
// of any read tool's route in api/v1/openapi.json.
func TestNoToolRouteSchemaCarriesADeniedField(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "v1", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]fullSchema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, r := range Tools() {
		if r.Method != "GET" {
			continue
		}
		route := r.Route
		if route == "/backup-sets/*" {
			route = "/backup-sets/{source}/{set}"
		}
		var op struct {
			Responses map[string]struct {
				Content map[string]struct {
					Schema fullSchema `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		}
		if err := json.Unmarshal(doc.Paths[route]["get"], &op); err != nil || len(op.Responses["200"].Content) == 0 {
			t.Fatalf("%s: no 200 response in the contract for %s", r.Tool, route)
		}
		root := op.Responses["200"].Content["application/json"].Schema
		for _, f := range deniedFieldsIn(root, r.Tool, doc.Components.Schemas, map[string]bool{}) {
			t.Errorf("%s: the response schema carries %s, which the projection drops. A raw GET of this route would return it, so the route cannot be a read tool.", r.Tool, f)
		}
		checked++
	}
	if checked != len(specRead) {
		t.Errorf("checked %d read tool routes, want %d, so this guard would pass without covering them", checked, len(specRead))
	}
}

func TestTheDeniedFieldGuardSeesAPlantedField(t *testing.T) {
	all := map[string]fullSchema{
		"A": {Properties: map[string]fullSchema{"items": {Items: &fullSchema{Ref: "#/components/schemas/B"}}}},
		"B": {Properties: map[string]fullSchema{"name": {}, "access_token": {}, "next": {Ref: "#/components/schemas/A"}}},
	}
	got := deniedFieldsIn(fullSchema{Ref: "#/components/schemas/A"}, "t", all, map[string]bool{})
	if len(got) != 1 || got[0] != "t.items[].access_token" {
		t.Errorf("a planted access_token under a ref and an array was reported as %v", got)
	}
}
