package agentinjection_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/tests/agentcorpus"
)

// The prompt-injection corpus (#1352, FR-98, UV.17). An instruction planted in
// each of the five origins the catalogue marks untrusted is read back through
// every surface an agent has, over the stdio transport here and over the HTTP
// transport in apps/common/webhost/agentadversarial_1352_test.go, and it must
// arrive only as data: inside the structured result's `untrusted` object,
// never in text a model reads as the server speaking, a description, a
// resource description or a prompt.
//
// Each row is one origin planted alone, so a hit names the origin that
// leaked. Each has a _WouldCatchOne control that plants the string into the
// text of a captured result and expects that row, and no other, to go red.
//
// Written against the spec's names table (section 12); not run.

func TestMain(m *testing.M) {
	code := m.Run()
	agentcorpus.CleanBuild()
	os.Exit(code)
}

// capture runs one deployment with plan and returns what an agent reading
// everything saw, with the stand-in API for what the agent sent.
func capture(t *testing.T, plan agentcorpus.Plan) ([]agentcorpus.Surface, *agentcorpus.API) {
	t.Helper()
	api := agentcorpus.NewAPI(t, plan)
	conn := agentcorpus.StartStdio(t, agentcorpus.BuildRetnd(t), agentcorpus.WriteConfig(t), api.Server.URL, agentcorpus.Token())
	return agentcorpus.Walk(t, conn), api
}

func onlyOrigin(o agentcorpus.Origin) agentcorpus.Plan {
	return agentcorpus.Plan{Plants: map[string]string{o.Slug: o.Marker()}}
}

func TestAnUntrustedStringArrivesOnlyAsData(t *testing.T) {
	for _, o := range agentcorpus.Origins {
		t.Run(o.Slug, func(t *testing.T) {
			surfaces, api := capture(t, onlyOrigin(o))
			marker := o.Marker()

			arrived := false
			for _, s := range surfaces {
				inside, leaked := agentcorpus.CheckData(s.Raw, marker)
				if leaked {
					t.Errorf("%s: the instruction planted in %s is outside `untrusted` on %s:\n%s", "stdio", o.Name, s.Name, s.Raw)
				}
				if s.Kind == "tool" && contains(o.Tools, s.Tool) && inside {
					arrived = true
				}
				if s.Kind != "tool" && inside {
					t.Errorf("%s: %s carries an `untrusted` object, which only a tool result has", "stdio", s.Name)
				}
			}
			// A row that saw nothing proves nothing: the string has to be
			// delivered, as data, by one of the tools that can carry it.
			if !arrived {
				t.Errorf("none of %v returned the instruction planted in %s inside `untrusted`; the row is vacuous", o.Tools, o.Name)
			}
			assertNothingStarted(t, api)
		})
	}
}

// All five at once is what a hostile deployment looks like, and the rows
// above would all stay green if one origin's handling only worked alone.
func TestAnUntrustedStringArrivesOnlyAsData_AllOriginsAtOnce(t *testing.T) {
	surfaces, api := capture(t, agentcorpus.Plant())
	for _, s := range surfaces {
		for _, o := range agentcorpus.Origins {
			if _, leaked := agentcorpus.CheckData(s.Raw, o.Marker()); leaked {
				t.Errorf("the instruction planted in %s is outside `untrusted` on %s", o.Name, s.Name)
			}
		}
	}
	assertNothingStarted(t, api)
}

// A planted instruction cannot create a confirmation, because a confirmation
// is created by a tool call under an operate or configure token and nothing
// here made one. The stand-in API is the engine's door: any request that was
// not a read, or any touch of the confirmation routes, is the agent having
// obeyed.
func TestAPlantedInstructionCreatesNoConfirmation(t *testing.T) {
	_, api := capture(t, agentcorpus.Plant())
	assertNothingStarted(t, api)
}

func assertNothingStarted(t *testing.T, api *agentcorpus.API) {
	t.Helper()
	if m := api.Mutations(); len(m) != 0 {
		t.Errorf("after the run the agent had sent %d requests that were not reads: %+v", len(m), m)
	}
	for _, r := range api.Requests() {
		if strings.Contains(r.Path, "/agent/confirmations") || (strings.Contains(r.Path, "/operations") && r.Method != "GET") {
			t.Errorf("the agent touched %s %s, so a confirmation could exist", r.Method, r.Path)
		}
	}
}

// The control for every row above: the same capture with the instruction
// moved into the result's text, which is the defect the row exists to catch.
// The origin's own row must see it and every other origin's must not.
func TestAnUntrustedStringArrivesOnlyAsData_WouldCatchOne(t *testing.T) {
	for _, o := range agentcorpus.Origins {
		t.Run(o.Slug, func(t *testing.T) {
			surfaces, _ := capture(t, onlyOrigin(o))
			var base *agentcorpus.Surface
			for i, s := range surfaces {
				if s.Kind == "tool" && contains(o.Tools, s.Tool) {
					if inside, _ := agentcorpus.CheckData(s.Raw, o.Marker()); inside {
						base = &surfaces[i]
						break
					}
				}
			}
			if base == nil {
				t.Fatalf("no captured tool result carries the instruction planted in %s, so there is nothing to move into the text", o.Name)
			}
			bad := intoText(t, base.Raw, o.Marker())
			for _, other := range agentcorpus.Origins {
				_, leaked := agentcorpus.CheckData(bad, other.Marker())
				switch {
				case other.Slug == o.Slug && !leaked:
					t.Errorf("%s row is blind: the instruction is in the text and CheckData does not see it", o.Slug)
				case other.Slug != o.Slug && leaked:
					t.Errorf("%s row went red for %s's string: one defect must turn one row red", other.Slug, o.Slug)
				}
			}
		})
	}
}

// A description built from data is the other way in, and the other control.
func TestAnUntrustedStringArrivesOnlyAsData_WouldCatchOne_Description(t *testing.T) {
	surfaces, _ := capture(t, agentcorpus.Plan{})
	for _, s := range surfaces {
		if s.Name != "tools/list" {
			continue
		}
		o := agentcorpus.Origins[0]
		var doc map[string]any
		if err := json.Unmarshal(s.Raw, &doc); err != nil {
			t.Fatal(err)
		}
		tools, _ := doc["tools"].([]any)
		if len(tools) == 0 {
			t.Fatal("tools/list is empty, so the catalogue is not served")
		}
		tools[0].(map[string]any)["description"] = "reads the set " + o.Marker()
		bad, _ := json.Marshal(doc)
		if _, leaked := agentcorpus.CheckData(bad, o.Marker()); !leaked {
			t.Error("a description built from a file name is not seen as a leak")
		}
		return
	}
	t.Fatal("no tools/list was captured")
}

// intoText puts the marker into the first text content of a tool result.
func intoText(t *testing.T, raw []byte, marker string) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	content, _ := doc["content"].([]any)
	if len(content) == 0 {
		content = []any{map[string]any{"type": "text", "text": ""}}
	}
	first, _ := content[0].(map[string]any)
	first["text"] = "result: " + marker
	content[0] = first
	doc["content"] = content
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
