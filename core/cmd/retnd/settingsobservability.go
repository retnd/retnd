package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/service"
)

// `settings observability`: the deployment-wide scrape endpoint, log floor
// and alert channel (EPIC O, #898; FR-103).
//
//	settings observability                      the resolved block
//	settings observability patch [flags]        change it
//	settings observability rotate-scrape-token  mint the Prometheus credential
//
// It sits under `settings` for the reason `settings workflow` does: it
// edits a block of config.yaml and reports it RESOLVED, which a
// re-serialization of the file cannot (the file omits what it inherits). A
// write goes through openConfigWriteRoute, `settings patch`'s door, so
// beside a serving engine it is handed over PATCH /settings/observability
// and takes effect at once.
//
// Every setting is global: a scrape endpoint, a log stream and an alert
// channel are properties of a deployment, not of a backup set, so there is
// no `backup-set` twin.

// settingsObservabilityOperand is the word that turns `settings` into this
// command.
const settingsObservabilityOperand = "observability"

// observabilityPatchFlagNames is every flag that only means something under
// `patch`.
var observabilityPatchFlagNames = []string{
	"metrics-enabled", "metrics-min-rebuild-interval", "logs-level", "alerts-delivery",
	"webhook-dialect", "webhook-allow-insecure-http", "webhook-refresh-interval", "webhook-resolve-ttl",
}

// cmdSettingsObservability is every `settings observability ...` form.
func cmdSettingsObservability(args []string) int {
	fs, cfgPath := newFlagSet("settings observability")
	asJSON := fs.Bool("json", false, "emit the API's ObservabilitySettingsResponse instead of text")
	metricsEnabled := fs.Bool("metrics-enabled", false, "patch only: observability.metrics.enabled, whether GET /metrics answers (off answers 404). Pass =false to turn it off")
	metricsInterval := fs.Duration("metrics-min-rebuild-interval", 0, "patch only: observability.metrics.min_rebuild_interval, how long a built health report is reused. At least 1s")
	logsLevel := fs.String("logs-level", "", "patch only: observability.logs.level, the configured log floor: debug, info, warn or error")
	alertsDelivery := fs.String("alerts-delivery", "", "patch only: observability.alerts.delivery, platform or webhook")
	webhookDialect := fs.String("webhook-dialect", "", "patch only: observability.alerts.webhook.dialect, generic or alertmanager")
	webhookInsecure := fs.Bool("webhook-allow-insecure-http", false, "patch only: observability.alerts.webhook.allow_insecure_http. Pass =false to refuse plain http again")
	webhookRefresh := fs.Duration("webhook-refresh-interval", 0, "patch only: observability.alerts.webhook.refresh_interval, how often a still-observed condition is re-sent (alertmanager dialect). Below the resolve ttl")
	webhookTTL := fs.Duration("webhook-resolve-ttl", 0, "patch only: observability.alerts.webhook.resolve_ttl, how long a sent alert lives at the receiver (alertmanager dialect)")

	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) == 0 || operands[0] != settingsObservabilityOperand {
		return usageError(`settings observability: expected "observability" as the first argument`)
	}
	rest := operands[1:]
	ctx := context.Background()

	switch {
	case len(rest) == 0:
		if named := visitedFlags(fs, "config", "json"); len(named) > 0 {
			return usageError("settings observability: --%s changes nothing on a read; `settings observability patch --%s ...` is the form that writes", strings.Join(named, ", --"), named[0])
		}
		svc, cleanup, err := openBackupService(ctx, *cfgPath, readsConfig)
		defer cleanup()
		if err != nil {
			return fail(err)
		}
		s, err := svc.ObservabilitySettings(ctx)
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			return printWorkflowJSON(observabilitySettingsWire(s))
		}
		printObservabilitySettings(s)

		return 0

	case rest[0] == "patch":
		if len(rest) != 1 {
			return usageError("settings observability patch takes no argument; %q is one", rest[1])
		}
		var req service.UpdateObservabilitySettingsRequest
		named := false
		fs.Visit(func(fl *flag.Flag) {
			named = named || contains(observabilityPatchFlagNames, fl.Name)
			switch fl.Name {
			case "metrics-enabled":
				req.MetricsEnabled = metricsEnabled
			case "metrics-min-rebuild-interval":
				req.MetricsMinRebuildInterval = metricsInterval
			case "logs-level":
				req.LogsLevel = logsLevel
			case "alerts-delivery":
				req.AlertsDelivery = alertsDelivery
			case "webhook-dialect":
				req.WebhookDialect = webhookDialect
			case "webhook-allow-insecure-http":
				req.WebhookAllowInsecureHTTP = webhookInsecure
			case "webhook-refresh-interval":
				req.WebhookRefreshInterval = webhookRefresh
			case "webhook-resolve-ttl":
				req.WebhookResolveTTL = webhookTTL
			}
		})
		if !named {
			return usageError("settings observability patch: name at least one of --%s; a patch that changes nothing would rewrite and reload the configuration to no effect",
				strings.Join(observabilityPatchFlagNames, ", --"))
		}
		route, cleanup, err := openConfigWriteRoute(ctx, *cfgPath)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		logStartup(ctx, logger(), app.BuildVersionInfo(version, commit))
		s, err := route.UpdateObservabilitySettings(ctx, req)
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			return printWorkflowJSON(observabilitySettingsWire(s))
		}
		printObservabilitySettings(s)

		return 0

	case rest[0] == "rotate-scrape-token":
		if len(rest) != 1 {
			return usageError("settings observability rotate-scrape-token takes no argument; %q is one", rest[1])
		}
		if named := visitedFlags(fs, "config", "json"); len(named) > 0 {
			return usageError("settings observability rotate-scrape-token: --%s belongs to `patch`", named[0])
		}

		// Through the same door as `patch`: beside a serving engine the engine
		// mints, so the credential lands where the endpoint verifies it, and
		// with nothing serving it is written here, to be read when one starts.
		route, cleanup, err := openConfigWriteRoute(ctx, *cfgPath)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		logStartup(ctx, logger(), app.BuildVersionInfo(version, commit))
		minted, err := route.MintScrapeToken(ctx)
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			return printWorkflowJSON(apicontract.MintedScrapeToken{
				Token:       minted.Token,
				ScrapeToken: observabilitySettingsWire(service.ObservabilitySettings{Metrics: service.ObservabilityMetricsSettings{ScrapeToken: minted.State}}).Metrics.ScrapeToken,
			})
		}
		// The one place the plaintext is ever printed. It is not stored, so a
		// lost credential is minted again and never recovered.
		fmt.Println(minted.Token)
		fmt.Fprintln(os.Stderr, "scrape token minted: shown once, and only a digest is kept. Any earlier token stopped working. Put it in Prometheus's credentials_file.")

		return 0
	}

	return usageError("settings observability has no %q form; it has show, patch and rotate-scrape-token", rest[0])
}

func observabilitySettingsWire(s service.ObservabilitySettings) apicontract.ObservabilitySettingsResponse {
	var created *string
	if !s.Metrics.ScrapeToken.CreatedAt.IsZero() {
		t := s.Metrics.ScrapeToken.CreatedAt.UTC().Format(time.RFC3339)
		created = &t
	}

	return apicontract.ObservabilitySettingsResponse{
		Metrics: apicontract.ObservabilityMetricsSettings{
			Enabled:                   s.Metrics.Enabled,
			MinRebuildIntervalSeconds: secs(s.Metrics.MinRebuildInterval),
			ScrapeToken:               apicontract.ScrapeTokenState{Exists: s.Metrics.ScrapeToken.Exists, Prefix: s.Metrics.ScrapeToken.Prefix, CreatedAt: created},
		},
		Logs: apicontract.ObservabilityLogsSettings{Level: s.Logs.Level, LevelInForce: s.Logs.LevelInForce, LevelSource: s.Logs.LevelSource},
		Alerts: apicontract.ObservabilityAlertsSettings{
			Delivery: s.Alerts.Delivery,
			Webhook: apicontract.ObservabilityWebhookSettings{
				Dialect:                s.Alerts.Webhook.Dialect,
				URLSet:                 s.Alerts.Webhook.URLSet,
				AllowInsecureHttp:      s.Alerts.Webhook.AllowInsecureHTTP,
				RefreshIntervalSeconds: secs(s.Alerts.Webhook.RefreshInterval),
				ResolveTtlSeconds:      secs(s.Alerts.Webhook.ResolveTTL),
			},
		},
	}
}

func printObservabilitySettings(s service.ObservabilitySettings) {
	fmt.Println("observability (deployment-wide):")
	fmt.Printf("  metrics: enabled=%t  min_rebuild_interval=%s\n", s.Metrics.Enabled, s.Metrics.MinRebuildInterval)
	if s.Metrics.ScrapeToken.Exists {
		fmt.Printf("  scrape token: minted %s (prefix %s)\n", s.Metrics.ScrapeToken.CreatedAt.UTC().Format(time.RFC3339), s.Metrics.ScrapeToken.Prefix)
	} else {
		fmt.Printf("  scrape token: none minted; `%s settings observability rotate-scrape-token` makes one\n", cliecho.Binary)
	}
	fmt.Printf("  logs: level=%s  in force=%s (from %s)\n", s.Logs.Level, s.Logs.LevelInForce, s.Logs.LevelSource)
	fmt.Printf("  alerts: delivery=%s\n", s.Alerts.Delivery)
	w := s.Alerts.Webhook
	fmt.Printf("  alerts webhook: dialect=%s  url set=%t  allow_insecure_http=%t  refresh_interval=%s  resolve_ttl=%s\n",
		w.Dialect, w.URLSet, w.AllowInsecureHTTP, w.RefreshInterval, w.ResolveTTL)
}
