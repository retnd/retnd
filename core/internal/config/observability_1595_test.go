package config

import (
	"strings"
	"testing"
	"time"
)

// #1595: the report-age alert and panel assume the report is rebuilt within
// a couple of minutes, so the interval has a ceiling the rule can rely on.

func TestMetricsMinRebuildInterval_AboveTheCeilingIsRefusedNamingIt_1595(t *testing.T) {
	over := Duration(10 * time.Minute)
	_, err := resolveObservabilityBlock(t, &ObservabilityBlock{Metrics: &ObservabilityMetricsBlock{MinRebuildInterval: &over}})
	if err == nil {
		t.Fatal("a 10m min_rebuild_interval was accepted, so RetndHealthReportNotRefreshing flaps once per cycle")
	}
	if !strings.Contains(err.Error(), "observability.metrics.min_rebuild_interval") || !strings.Contains(err.Error(), "2m0s") {
		t.Errorf("the refusal must name the key and the ceiling, got: %v", err)
	}
}

func TestMetricsMinRebuildInterval_TheCeilingItselfAndTheCompatFixtureStillLoad_1595(t *testing.T) {
	for _, d := range []time.Duration{time.Second, 15 * time.Second, 30 * time.Second, 2 * time.Minute} {
		v := Duration(d)
		got, err := resolveObservabilityBlock(t, &ObservabilityBlock{Metrics: &ObservabilityMetricsBlock{MinRebuildInterval: &v}})
		if err != nil || got.Metrics.MinRebuildInterval != d {
			t.Errorf("%s: got %v, %v; want it accepted unchanged", d, got.Metrics.MinRebuildInterval, err)
		}
	}
}
