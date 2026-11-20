package metrics

import (
	"strings"
	"sync"
)

// The scrape endpoint's own counter (EPIC O, #899): retnd_scrape_rejected_total,
// the 47th family. It replaces the per-request action-log row a scrape must
// never write, so a refusal is visible to the operator without a 15 second
// scraper filling an append-only feed.
//
// Its one label, reason, comes from a closed three-word vocabulary and from
// nothing the request carried: not an address, not a header, not a method. The
// three words are every reason the endpoint refuses, and an unknown reason is
// dropped rather than minted into a new series, which is what keeps the
// cardinality a constant. labels_test.go holds that rule for every other
// family, and holds it here too.

// The reasons a scrape is refused.
const (
	ScrapeRejectedDisabled        = "disabled"
	ScrapeRejectedUnauthenticated = "unauthenticated"
	ScrapeRejectedMethod          = "method"
)

// scrapeRejectedReasons fixes the order the three samples render in.
var scrapeRejectedReasons = [...]string{ScrapeRejectedDisabled, ScrapeRejectedUnauthenticated, ScrapeRejectedMethod}

// Scrape counts refused scrapes. The zero value is ready to use.
type Scrape struct {
	mu       sync.Mutex
	rejected [len(scrapeRejectedReasons)]uint64
}

// ObserveRejected counts one refusal. A reason outside the closed vocabulary
// is dropped, and the caller is told, so a new reason has to be added here
// (and to the matrix) before it can become a series.
func (s *Scrape) ObserveRejected(reason string) bool {
	for i, r := range scrapeRejectedReasons {
		if r == reason {
			s.mu.Lock()
			s.rejected[i]++
			s.mu.Unlock()

			return true
		}
	}

	return false
}

// RenderRejected renders the counter as Prometheus text exposition. All three
// samples are always present, at zero until something is refused: unlike a
// workflow family, "nothing has been refused" is a true statement about this
// endpoint, and a rate() over a series that appears late loses its first
// increase.
func (s *Scrape) RenderRejected() string {
	s.mu.Lock()
	counts := s.rejected
	s.mu.Unlock()

	var b strings.Builder
	name := namePrefix + "scrape_rejected_total"
	writeHelp(&b, name, "Scrapes of the metrics endpoint that were refused, by reason: disabled, unauthenticated or method.")
	writeType(&b, name, "counter")
	for i, r := range scrapeRejectedReasons {
		b.WriteString(name + "{reason=" + quoteLabel(r) + "} " + formatFloat(float64(counts[i])) + "\n")
	}

	return b.String()
}
