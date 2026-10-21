package cliecho

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// #1252: the medium configure line exists twice, once here and once in the
// browser (ui/shared/src/pages/destinationConfigureCommands.ts), and the
// browser's copy is printed beside the wizard's buttons before the request
// is made. testdata/medium_configure_1252.json holds the cases both suites
// check, so a change on either side that the other has not made fails a
// test instead of printing a line that configures something else.
//
// UPDATE_GOLDEN=1 rewrites the expected lines and endpoint cuts from what
// this package prints now. Read the diff before committing it: the browser
// suite reads the same file and goes red on anything it disagrees with.

const golden1252 = "testdata/medium_configure_1252.json"

type golden1252File struct {
	About           string                `json:"about"`
	EndpointVectors []golden1252Endpoint  `json:"endpoint_vectors"`
	ConfigureCases  []golden1252Configure `json:"configure_cases"`
	DefaultCases    []golden1252Default   `json:"default_cases"`
}

type golden1252Endpoint struct {
	In       string `json:"in"`
	Out      string `json:"out"`
	Withheld bool   `json:"withheld"`
}

type golden1252Configure struct {
	Name          string            `json:"name"`
	ID            string            `json:"id"`
	Backend       string            `json:"backend"`
	Fields        map[string]string `json:"fields"`
	Kinds         map[string]string `json:"kinds"`
	CredentialsID string            `json:"credentials_id"`
	Preflight     bool              `json:"preflight"`
	Line          string            `json:"line"`
	Runnable      bool              `json:"runnable"`
}

type golden1252Default struct {
	ID   string `json:"id"`
	Line string `json:"line"`
}

// configureBody is the request body the browser sends for a case: fields
// sorted by id, the way toWireMediumConfiguration sorts them.
func (c golden1252Configure) configureBody(t *testing.T) []byte {
	t.Helper()
	ids := make([]string, 0, len(c.Fields))
	for id := range c.Fields {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	type field struct {
		Field string `json:"field"`
		Value string `json:"value"`
	}
	body := struct {
		Backend     string            `json:"backend"`
		Fields      []field           `json:"fields"`
		Credentials map[string]string `json:"credentials"`
	}{Backend: c.Backend, Fields: []field{}, Credentials: map[string]string{}}
	for _, id := range ids {
		body.Fields = append(body.Fields, field{id, c.Fields[id]})
	}
	if c.CredentialsID != "" {
		body.Credentials["credentials_id"] = c.CredentialsID
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (c golden1252Configure) action(t *testing.T) Action {
	route := "/storage-mediums/{id}/configuration"
	method := "PUT"
	if c.Preflight {
		route, method = route+"/preflight", "POST"
	}
	return Action{Method: method, Route: route, Params: map[string]string{"id": c.ID}, Body: c.configureBody(t)}
}

func TestIssue1252_TheConfigureLineMatchesTheSharedGolden(t *testing.T) {
	raw, err := os.ReadFile(golden1252)
	if err != nil {
		t.Fatal(err)
	}
	var g golden1252File
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	update := os.Getenv("UPDATE_GOLDEN") == "1"

	for i, v := range g.EndpointVectors {
		out, ok := endpointWithoutUserinfo(v.In)
		if update {
			g.EndpointVectors[i].Out, g.EndpointVectors[i].Withheld = out, !ok
			continue
		}
		if out != v.Out || ok == v.Withheld {
			t.Errorf("endpointWithoutUserinfo(%q) = %q, readable %v; want %q, withheld %v", v.In, out, ok, v.Out, v.Withheld)
		}
	}

	for i, c := range g.ConfigureCases {
		line := Echo(c.action(t))
		if line.Gap != "" {
			t.Errorf("%s: echoes a gap %q, want a command", c.Name, line.Gap)
			continue
		}
		if update {
			g.ConfigureCases[i].Line, g.ConfigureCases[i].Runnable = line.Shell(), !line.Placeholder
			continue
		}
		if line.Shell() != c.Line || line.Placeholder == c.Runnable {
			t.Errorf("%s:\n got  %q (runnable %v)\n want %q (runnable %v)", c.Name, line.Shell(), !line.Placeholder, c.Line, c.Runnable)
		}
	}

	for i, c := range g.DefaultCases {
		line := Echo(Action{Method: "PUT", Route: "/storage-mediums/{id}/default", Params: map[string]string{"id": c.ID}})
		if update {
			g.DefaultCases[i].Line = line.Shell()
			continue
		}
		if line.Shell() != c.Line {
			t.Errorf("medium default %s: got %q, want %q", c.ID, line.Shell(), c.Line)
		}
	}

	if update {
		out, err := json.MarshalIndent(g, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Clean(golden1252), append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// No vector in the shared file may put a password on a line, whichever way
// the cut went: the withheld ones print nothing, and the readable ones print
// an address with no userinfo left in it.
func TestIssue1252_NoEndpointVectorLeaksItsPassword(t *testing.T) {
	for _, in := range []string{
		"https://u:pa/ss@host", "https://u:pa?ss@host", "https://u:pa#ss@host",
		"https://u:p@ss@host/b", "https://AKIA:secret@minio.internal:9000/b",
	} {
		out, _ := endpointWithoutUserinfo(in)
		for _, secret := range []string{"pa/ss", "pa?ss", "pa#ss", "p@ss", "secret"} {
			if strings.Contains(in, secret) && strings.Contains(out, secret) {
				t.Errorf("endpointWithoutUserinfo(%q) = %q, which still carries %q", in, out, secret)
			}
		}
	}
}

// The --endpoint on medium add and edit goes through the same cut, and an
// endpoint it cannot read is held back the same way: a placeholder, so the
// line says it will not run as printed instead of quietly dropping the flag.
func TestIssue1252_MediumAddHoldsBackAnEndpointItCannotRead(t *testing.T) {
	line := Echo(Action{Method: "POST", Route: "/storage-mediums",
		Body: []byte(`{"id":"cold","type":"s3","bucket":"b","endpoint":"https://u:pa/ss@minio.internal"}`)})
	if line.Gap != "" || !line.Placeholder {
		t.Fatalf("got gap %q placeholder %v, want a placeholder line", line.Gap, line.Placeholder)
	}
	if s := line.Shell(); strings.Contains(s, "pa/ss") || !strings.Contains(s, "--endpoint <") {
		t.Errorf("medium add line %q leaks the endpoint or drops its slot", s)
	}
}

// An S3 endpoint is cut because the s3 manifest declares it a url field, and
// a request that names no backend (an edit of a declared destination) still
// gets its `endpoint` cut by id, so leaving the backend out cannot leak it.
func TestIssue1252_ConfigureCutsAURLFieldWithOrWithoutTheBackend(t *testing.T) {
	for _, body := range []string{
		`{"backend":"s3","fields":[{"field":"endpoint","value":"https://AKIA:secret@minio.internal"}],"credentials":{}}`,
		`{"fields":[{"field":"endpoint","value":"https://AKIA:secret@minio.internal"}],"credentials":{}}`,
	} {
		line := Echo(Action{Method: "PUT", Route: "/storage-mediums/{id}/configuration", Params: map[string]string{"id": "cold"}, Body: []byte(body)})
		if s := line.Shell(); strings.Contains(s, "secret") || !strings.Contains(s, "endpoint=https://minio.internal") {
			t.Errorf("body %s echoes %q", body, s)
		}
	}
}
