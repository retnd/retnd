package service

import (
	"os"
	"strings"
	"testing"
)

// #900 review, B2: docs/observability.md said the settings read reports
// `RETND_DEBUG`, `LOG_LEVEL` or `config` as where the log level came from.
// The read reports LogLevelSourceConfig or LogLevelSourceEnvironment and
// nothing else (logsSettingsOf; the contract's level_source enum), so a
// script matching the doc's `LOG_LEVEL` never matched. This holds the
// sentence that names the reported values to the constants.

const observabilityDocPath = "../../docs/observability.md"

// levelSourceDocProblems lists what the level section's "report" sentence
// gets wrong about the values the read returns.
func levelSourceDocProblems(doc string) []string {
	const heading = "## The log level"
	start := strings.Index(doc, heading)
	if start < 0 {
		return []string{"there is no " + heading + " section"}
	}
	section := doc[start+len(heading):]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	var sentence string
	for _, para := range strings.Split(section, "\n") {
		if strings.Contains(para, "retnd settings observability") && strings.Contains(para, "report") {
			sentence = para
			break
		}
	}
	if sentence == "" {
		return []string{"the level section has no line saying what `retnd settings observability` reports"}
	}
	var problems []string
	for _, v := range []string{LogLevelSourceConfig, LogLevelSourceEnvironment} {
		if !strings.Contains(sentence, "`"+v+"`") {
			problems = append(problems, "it does not name `"+v+"`, which the read returns")
		}
	}
	for _, wrong := range []string{"`RETND_DEBUG`", "`RM_DEBUG`", "`LOG_LEVEL`"} {
		if strings.Contains(sentence, wrong) {
			problems = append(problems, "it names "+wrong+" as a reported value, which the read never returns")
		}
	}
	return problems
}

func TestTheDocNamesTheLevelSourcesTheReadReturns_900(t *testing.T) {
	raw, err := os.ReadFile(observabilityDocPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range levelSourceDocProblems(string(raw)) {
		t.Errorf("%s, the sentence about where the level came from: %s", observabilityDocPath, p)
	}
}

// The guard's control: the old sentence is refused and names what is wrong,
// and the right one passes.
func TestTheDocNamesTheLevelSourcesTheReadReturns_900_WouldCatchTheOldSentence(t *testing.T) {
	const old = "## The log level\n\n`GET /api/v1/settings/observability` and `retnd settings observability` report which of the three is in force (`RETND_DEBUG`, `LOG_LEVEL` or `config`).\n\n## Next\n"
	got := strings.Join(levelSourceDocProblems(old), "\n")
	for _, want := range []string{"`environment`", "`RETND_DEBUG`", "`LOG_LEVEL`"} {
		if !strings.Contains(got, want) {
			t.Errorf("the old sentence was not refused for %s: %q", want, got)
		}
	}
	const good = "## The log level\n\n`GET /api/v1/settings/observability` and `retnd settings observability` report where the level in force came from: `config` or `environment`.\n\n## Next\n"
	if got := levelSourceDocProblems(good); len(got) != 0 {
		t.Errorf("the right sentence is refused, so the control proves nothing: %q", got)
	}
}
