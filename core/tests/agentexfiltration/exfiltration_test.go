package agentexfiltration_test

import (
	"os"
	"testing"

	"github.com/retnd/retnd/core/tests/agentcorpus"
)

// The exfiltration corpus (#1352, FR-98, UV.16): the canary corpus
// (core/tests/encryptedretention/canary_test.go) extended to the agent
// surface. A deployment with a managed key, an external reference, SMTP
// settings, a workflow secret and a storage credential has each value (plus
// a recipient, an identity, a post-quantum identity, a bundle, a passphrase
// and a token) planted where the API could return it, under the field names
// the projection denies. Every surface an agent has is then read over the
// stdio transport (the HTTP row is in apps/common/webhost) and scanned in
// every rendering, plus the key shapes, so a field somebody adds next
// month is held by the shape and not only by the name.
//
// One surface, one row: a leak planted into one surface turns that
// surface's row red and no other, and every row keeps a control proving its
// detector can see a planted value.
//
// The scanning rules are the canary corpus's own (renderings, the age
// identity shape); the planted values are generated at run time and no key
// or canary value is in this file.
//
// Written against the spec's names table (section 12); not run.

func TestMain(m *testing.M) {
	code := m.Run()
	agentcorpus.CleanBuild()
	os.Exit(code)
}

func capture(t *testing.T, plan agentcorpus.Plan) (surfaces []agentcorpus.Surface, stderr string) {
	t.Helper()
	api := agentcorpus.NewAPI(t, plan)
	conn := agentcorpus.StartStdio(t, agentcorpus.BuildRetnd(t), agentcorpus.WriteConfig(t), api.Server.URL, agentcorpus.Token())
	surfaces = agentcorpus.Walk(t, conn)
	return surfaces, conn.StderrText()
}

// requireData fails a row that read nothing: a projection that refuses every
// call carries no canary and proves nothing.
func requireData(t *testing.T, surfaces []agentcorpus.Surface) {
	t.Helper()
	answered := 0
	for _, s := range surfaces {
		if s.Kind == "tool" && s.Error == nil {
			answered++
		}
	}
	if answered < 5 {
		t.Fatalf("only %d read tools answered; a row built on refusals proves nothing", answered)
	}
}

func TestNoKeyMaterialOnAnyAgentSurface_1352(t *testing.T) {
	for _, c := range agentcorpus.Canaries() {
		t.Run(c.Slug, func(t *testing.T) {
			surfaces, stderr := capture(t, agentcorpus.Plan{Canaries: []agentcorpus.Canary{c}})
			requireData(t, surfaces)
			for _, s := range surfaces {
				if leaks := agentcorpus.Leaks(s.Raw, []string{c.Value}); len(leaks) > 0 {
					t.Errorf("stdio: %s carries %s (%v):\n%s", s.Name, c.Origin, leaks, s.Raw)
				}
			}
			if leaks := agentcorpus.Leaks([]byte(stderr), []string{c.Value}); len(leaks) > 0 {
				t.Errorf("stdio: standard error carries %s (%v):\n%s", c.Origin, leaks, stderr)
			}
		})
	}
}

// The shapes alone, in a free-text field no denylist names: an identity, a
// recipient and a PEM block inside an activity message.
func TestNoKeyShapeInFreeTextOnAnyAgentSurface_1352(t *testing.T) {
	cs := agentcorpus.Canaries()
	text := agentcorpus.PEMBlock() + " " + cs[0].Value + " " + cs[1].Value + " " + cs[2].Value
	surfaces, _ := capture(t, agentcorpus.Plan{Plants: map[string]string{"activity": text, "steplog": text, "validator": text}})
	requireData(t, surfaces)
	for _, s := range surfaces {
		if leaks := agentcorpus.Leaks(s.Raw, agentcorpus.CanaryValues(cs[:3])); len(leaks) > 0 {
			t.Errorf("stdio: %s carries key material planted in free text (%v)", s.Name, leaks)
		}
	}
}

// Every canary at once, as a deployment with all of them would answer.
func TestNoKeyMaterialOnAnyAgentSurface_1352_AllAtOnce(t *testing.T) {
	cs := agentcorpus.Canaries()
	surfaces, stderr := capture(t, agentcorpus.Plan{Canaries: cs})
	requireData(t, surfaces)
	values := agentcorpus.CanaryValues(cs)
	for _, s := range surfaces {
		if leaks := agentcorpus.Leaks(s.Raw, values); len(leaks) > 0 {
			t.Errorf("stdio: %s carries material (%v)", s.Name, leaks)
		}
	}
	if leaks := agentcorpus.Leaks([]byte(stderr), values); len(leaks) > 0 {
		t.Errorf("stdio: standard error carries material (%v)", leaks)
	}
}

// The control: the detector sees every canary in every rendering, the PQ
// identity shape and a PEM block, and a value planted into one captured
// surface turns that surface's row red and no other.
func TestNoKeyMaterialOnAnyAgentSurface_1352_WouldCatchOne(t *testing.T) {
	cs := agentcorpus.Canaries()
	values := agentcorpus.CanaryValues(cs)

	for _, c := range cs {
		for i, r := range agentcorpus.Renderings(c.Value) {
			if !agentcorpus.Carries([]byte("prefix "+r+" suffix"), []string{c.Value}) {
				t.Errorf("the detector is blind to %s in rendering %d", c.Slug, i)
			}
		}
	}
	for name, shape := range map[string]string{"pq identity": cs[1].Value, "identity": cs[0].Value, "pem": agentcorpus.PEMBlock()} {
		if !agentcorpus.Carries([]byte(shape), nil) {
			t.Errorf("the shape detector does not see a planted %s with no value to compare against", name)
		}
	}

	surfaces, _ := capture(t, agentcorpus.Plan{})
	requireData(t, surfaces)
	for _, s := range surfaces {
		if agentcorpus.Carries(s.Raw, values) {
			t.Fatalf("the clean capture of %s already carries material, so the control has no baseline", s.Name)
		}
	}
	for i := range surfaces {
		if surfaces[i].Kind != "tool" {
			continue
		}
		planted := append(append([]byte(nil), surfaces[i].Raw...), []byte(" "+cs[2].Value)...)
		for j, s := range surfaces {
			raw := s.Raw
			if i == j {
				raw = planted
			}
			got := agentcorpus.Carries(raw, values)
			if got != (i == j) {
				t.Errorf("planting a recipient into %s: the row for %s answered %v", surfaces[i].Name, s.Name, got)
			}
		}
		break
	}
}
