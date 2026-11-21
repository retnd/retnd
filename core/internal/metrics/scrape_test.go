package metrics_test

import (
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/metrics"
)

// EPIC O, O1.1 (#899): the refusal counter's label is a closed vocabulary.

func TestScrapeRejected_AllThreeReasonsRenderFromTheStartAndCountOnlyTheirOwn(t *testing.T) {
	t.Parallel()

	var s metrics.Scrape
	if got := s.RenderRejected(); !strings.Contains(got, `retnd_scrape_rejected_total{reason="disabled"} 0`) ||
		!strings.Contains(got, `retnd_scrape_rejected_total{reason="unauthenticated"} 0`) ||
		!strings.Contains(got, `retnd_scrape_rejected_total{reason="method"} 0`) {
		t.Fatalf("a fresh counter must render all three samples at zero:\n%s", got)
	}
	s.ObserveRejected(metrics.ScrapeRejectedUnauthenticated)
	s.ObserveRejected(metrics.ScrapeRejectedUnauthenticated)
	s.ObserveRejected(metrics.ScrapeRejectedMethod)
	got := s.RenderRejected()
	if !strings.Contains(got, `{reason="unauthenticated"} 2`) || !strings.Contains(got, `{reason="method"} 1`) || !strings.Contains(got, `{reason="disabled"} 0`) {
		t.Errorf("the counts are wrong:\n%s", got)
	}
}

// The mutation this row exists for: a build that labels the counter with a
// value the request carried. Here that is a reason the vocabulary does not
// hold, which must be refused and must not become a series.
func TestScrapeRejected_AValueOutsideTheVocabularyNeverBecomesALabel(t *testing.T) {
	t.Parallel()

	var s metrics.Scrape
	for _, planted := range secretsAndPaths {
		if s.ObserveRejected(planted) {
			t.Errorf("%q was accepted as a reason", planted)
		}
	}
	out := s.RenderRejected()
	for _, planted := range secretsAndPaths {
		if strings.Contains(out, planted) {
			t.Errorf("%q reached the exposition", planted)
		}
	}
	// The positive control: the same rule over the same labels the other
	// families are held to finds exactly one label name here, and it is reason.
	names := map[string]bool{}
	for _, m := range labelPairs.FindAllStringSubmatch(out, -1) {
		names[m[1]] = true
	}
	if len(names) != 1 || !names["reason"] {
		t.Errorf("label names = %v, want exactly reason", names)
	}
}
