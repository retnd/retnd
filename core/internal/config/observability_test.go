package config

import (
	"strings"
	"testing"
	"time"
)

// EPIC O (#898): the observability block resolves to everything off when
// nobody wrote it, validates what they did write, and refuses a plaintext
// scrape token by name without echoing it.

func resolveObservabilityBlock(t *testing.T, b *ObservabilityBlock) (Observability, error) {
	t.Helper()
	c := &Config{ObservabilityBlock: b}
	v := &validator{}
	c.resolveObservability(v)

	return c.Observability, v.err()
}

func TestObservability_AnAbsentBlockResolvesEverythingOff(t *testing.T) {
	got, err := resolveObservabilityBlock(t, nil)
	if err != nil {
		t.Fatalf("an absent block must validate: %v", err)
	}
	want := Observability{
		Metrics: ObservabilityMetrics{MinRebuildInterval: 15 * time.Second},
		Logs:    ObservabilityLogs{Level: "info"},
		Alerts: ObservabilityAlerts{
			Delivery: "platform",
			Webhook:  ObservabilityWebhookResolved{Dialect: "generic", RefreshInterval: 4 * time.Minute, ResolveTTL: 10 * time.Minute},
		},
	}
	if got.Metrics != want.Metrics || got.Logs != want.Logs || got.Alerts.Delivery != want.Alerts.Delivery ||
		got.Alerts.Webhook.Dialect != want.Alerts.Webhook.Dialect ||
		got.Alerts.Webhook.RefreshInterval != want.Alerts.Webhook.RefreshInterval ||
		got.Alerts.Webhook.ResolveTTL != want.Alerts.Webhook.ResolveTTL || got.Alerts.Webhook.AllowInsecureHTTP {
		t.Errorf("defaults = %+v, want %+v", got, want)
	}
}

func TestObservability_APlaintextScrapeTokenIsRefusedByNameAndNeverEchoed(t *testing.T) {
	const secret = "rsc_not-a-real-token-but-must-never-be-echoed"
	_, err := resolveObservabilityBlock(t, &ObservabilityBlock{Metrics: &ObservabilityMetricsBlock{Token: secret}})
	if err == nil {
		t.Fatal("a config carrying observability.metrics.token must be refused")
	}
	if !strings.Contains(err.Error(), ScrapeTokenMintCommand) {
		t.Errorf("the refusal %q does not name the command that mints one", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the refusal echoed the token: %q", err)
	}
}

func TestObservability_EachBadValueIsRefusedWithItsOwnPath(t *testing.T) {
	short := Duration(time.Millisecond)
	same := Duration(10 * time.Minute)
	for want, b := range map[string]*ObservabilityBlock{
		"observability.metrics.min_rebuild_interval":    {Metrics: &ObservabilityMetricsBlock{MinRebuildInterval: &short}},
		"observability.logs.level":                      {Logs: &ObservabilityLogsBlock{Level: "chatty"}},
		"observability.alerts.delivery":                 {Alerts: &ObservabilityAlertsBlock{Delivery: "carrier-pigeon"}},
		"observability.alerts.webhook.dialect":          {Alerts: &ObservabilityAlertsBlock{Webhook: &ObservabilityWebhook{Dialect: "slack"}}},
		"observability.alerts.webhook.refresh_interval": {Alerts: &ObservabilityAlertsBlock{Webhook: &ObservabilityWebhook{RefreshInterval: &same}}},
	} {
		_, err := resolveObservabilityBlock(t, b)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want a refusal naming %s, got %v", want, err)
		}
	}
}
