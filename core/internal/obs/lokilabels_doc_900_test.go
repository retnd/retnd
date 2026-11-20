package obs

import (
	"os"
	"strings"
	"testing"
)

// The four stream labels (job, instance, level, schema) are only four on a
// real Loki when its config sets limits_config.discover_service_name to an
// empty list. Loki 3.2.1 otherwise adds service_name to every stream, and the
// command-line flag does not stop it (measured by #902's bundle smoke).
// core/tests/logpipeline cannot see this: its receiver stands in for Loki and
// adds nothing. So the requirement is written in docs/observability.md's
// label section, and this holds the sentence there.

const observabilityDoc = "../../../docs/observability.md"

// labelSectionProblems lists what the label section of the doc fails to say.
func labelSectionProblems(doc string) []string {
	const heading = "## Labels: four, and nothing else"
	start := strings.Index(doc, heading)
	if start < 0 {
		return []string{"there is no " + heading + " section"}
	}
	section := doc[start+len(heading):]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	var problems []string
	for _, want := range []string{"discover_service_name: []", "service_name", "limits_config"} {
		if !strings.Contains(section, want) {
			problems = append(problems, "the label section does not say "+want)
		}
	}
	return problems
}

func TestTheDocSaysLokiNeedsDiscoverServiceNameOff_900(t *testing.T) {
	raw, err := os.ReadFile(observabilityDoc)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range labelSectionProblems(string(raw)) {
		t.Errorf("%s: %s. The four labels only hold on a Loki whose config sets limits_config.discover_service_name: [], and nothing else in the tree can check that", observabilityDoc, p)
	}
}

// The guard's own control: the section without the sentence is caught, and
// the sentence moved out of the section is caught too.
func TestTheDocSaysLokiNeedsDiscoverServiceNameOff_900_WouldCatchItsRemoval(t *testing.T) {
	const good = "## Labels: four, and nothing else\n\nSet `discover_service_name: []` under `limits_config`, or Loki adds `service_name`.\n\n## Next\n"
	if got := labelSectionProblems(good); len(got) != 0 {
		t.Fatalf("the correct section is refused, so the reds below prove nothing: %q", got)
	}
	if got := labelSectionProblems("## Labels: four, and nothing else\n\nJust the four.\n\n## Next\n"); len(got) == 0 {
		t.Error("a label section without the requirement was not caught")
	}
	if got := labelSectionProblems("## Labels: four, and nothing else\n\nJust the four.\n\n## Next\n\nSet `discover_service_name: []` under `limits_config`, or Loki adds `service_name`.\n"); len(got) == 0 {
		t.Error("the requirement written under another heading was taken as the label section's")
	}
}
