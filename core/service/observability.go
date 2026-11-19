package service

import (
	"context"
	"fmt"
	"time"

	"github.com/retnd/retnd/core/internal/config"
)

// EPIC O's settings surface (#898; FR-103): the deployment-wide
// `observability:` block as this process is running it, and the patch that
// rewrites it.
//
// The shape of the whole block is fixed here, once, so the three issues that
// fill in its sub-blocks (metrics #899, logs #900, alerts #901) add
// behaviour and never a field or a rename. Every knob is global: a scrape
// endpoint, a log stream and an alert channel are properties of a
// deployment and not of a backup set.

// Where the log floor in force comes from. The environment outranks the
// configured floor (obs.LevelFromEnv), and the read says which one won so
// "why is it not debug" is never a guess.
const (
	LogLevelSourceConfig      = "config"
	LogLevelSourceEnvironment = "environment"
)

// ObservabilitySettings is the resolved block.
type ObservabilitySettings struct {
	Metrics ObservabilityMetricsSettings
	Logs    ObservabilityLogsSettings
	Alerts  ObservabilityAlertsSettings
}

// ObservabilityMetricsSettings is `observability.metrics`.
type ObservabilityMetricsSettings struct {
	Enabled            bool
	MinRebuildInterval time.Duration
	// ScrapeToken says whether a credential exists and when it was minted,
	// never any part of it beyond the display-only prefix.
	ScrapeToken ScrapeTokenState
}

// ScrapeTokenState is what a read may say about the scrape credential.
type ScrapeTokenState struct {
	Exists    bool
	Prefix    string
	CreatedAt time.Time
}

// ObservabilityLogsSettings is `observability.logs`.
type ObservabilityLogsSettings struct {
	// Level is the configured floor; LevelInForce is the one the process
	// is using, and LevelSource says which of the two won.
	Level        string
	LevelInForce string
	LevelSource  string
}

// ObservabilityAlertsSettings is `observability.alerts`.
type ObservabilityAlertsSettings struct {
	Delivery string
	Webhook  ObservabilityWebhookSettings
}

// ObservabilityWebhookSettings is `observability.alerts.webhook`. The URL is
// a credential, so a read says only whether one is set.
type ObservabilityWebhookSettings struct {
	Dialect           string
	URLSet            bool
	AllowInsecureHTTP bool
	RefreshInterval   time.Duration
	ResolveTTL        time.Duration
}

// UpdateObservabilitySettingsRequest patches the block. Nil leaves a field
// alone; a request that names nothing is refused.
type UpdateObservabilitySettingsRequest struct {
	MetricsEnabled            *bool
	MetricsMinRebuildInterval *time.Duration
	LogsLevel                 *string
	AlertsDelivery            *string
	WebhookDialect            *string
	WebhookAllowInsecureHTTP  *bool
	WebhookRefreshInterval    *time.Duration
	WebhookResolveTTL         *time.Duration
}

func (r UpdateObservabilitySettingsRequest) isEmpty() bool {
	return r.MetricsEnabled == nil && r.MetricsMinRebuildInterval == nil && r.LogsLevel == nil &&
		r.AlertsDelivery == nil && r.WebhookDialect == nil && r.WebhookAllowInsecureHTTP == nil &&
		r.WebhookRefreshInterval == nil && r.WebhookResolveTTL == nil
}

// ObservabilitySettings reports the block as this process is running it.
func (b *BackupService) ObservabilitySettings(_ context.Context) (ObservabilitySettings, error) {
	return observabilitySettingsOf(b.state.Load().inner.Config), nil
}

func observabilitySettingsOf(cfg *config.Config) ObservabilitySettings {
	o := cfg.Observability

	return ObservabilitySettings{
		Metrics: ObservabilityMetricsSettings{
			Enabled:            o.Metrics.Enabled,
			MinRebuildInterval: o.Metrics.MinRebuildInterval,
		},
		Logs: ObservabilityLogsSettings{
			Level:        o.Logs.Level,
			LevelInForce: o.Logs.Level,
			LevelSource:  LogLevelSourceConfig,
		},
		Alerts: ObservabilityAlertsSettings{
			Delivery: o.Alerts.Delivery,
			Webhook: ObservabilityWebhookSettings{
				Dialect:           o.Alerts.Webhook.Dialect,
				URLSet:            o.Alerts.Webhook.URLRef.File != "" || o.Alerts.Webhook.URLRef.Env != "" || len(o.Alerts.Webhook.URLRef.Command) > 0,
				AllowInsecureHTTP: o.Alerts.Webhook.AllowInsecureHTTP,
				RefreshInterval:   o.Alerts.Webhook.RefreshInterval,
				ResolveTTL:        o.Alerts.Webhook.ResolveTTL,
			},
		},
	}
}

// UpdateObservabilitySettings patches the block and hot-reloads this
// service. Every rule about what a field may hold is config.Validate's and
// comes back as ErrInvalidRequest with that package's own sentence.
func (b *BackupService) UpdateObservabilitySettings(_ context.Context, req UpdateObservabilitySettingsRequest) (ObservabilitySettings, error) {
	if b.configPath == "" {
		return ObservabilitySettings{}, ErrConfigNotFileBacked
	}
	if req.isEmpty() {
		return ObservabilitySettings{}, fmt.Errorf("%w: this patch changes nothing; name at least one setting of the observability block", ErrInvalidRequest)
	}

	b.configMu.Lock()
	defer b.configMu.Unlock()

	// Re-read from disk, the "always read fresh" discipline every
	// configuration write in this package shares.
	cfg, err := config.Load(b.configPath)
	if err != nil {
		return ObservabilitySettings{}, fmt.Errorf("service: re-reading configuration: %w", err)
	}

	if cfg.ObservabilityBlock == nil {
		cfg.ObservabilityBlock = &config.ObservabilityBlock{}
	}
	blk := cfg.ObservabilityBlock
	if req.MetricsEnabled != nil || req.MetricsMinRebuildInterval != nil {
		if blk.Metrics == nil {
			blk.Metrics = &config.ObservabilityMetricsBlock{}
		}
		if req.MetricsEnabled != nil {
			blk.Metrics.Enabled = req.MetricsEnabled
		}
		if req.MetricsMinRebuildInterval != nil {
			d := config.Duration(*req.MetricsMinRebuildInterval)
			blk.Metrics.MinRebuildInterval = &d
		}
	}
	if req.LogsLevel != nil {
		blk.Logs = &config.ObservabilityLogsBlock{Level: *req.LogsLevel}
	}
	if req.AlertsDelivery != nil || req.WebhookDialect != nil || req.WebhookAllowInsecureHTTP != nil ||
		req.WebhookRefreshInterval != nil || req.WebhookResolveTTL != nil {
		if blk.Alerts == nil {
			blk.Alerts = &config.ObservabilityAlertsBlock{}
		}
		if req.AlertsDelivery != nil {
			blk.Alerts.Delivery = *req.AlertsDelivery
		}
		if req.WebhookDialect != nil || req.WebhookAllowInsecureHTTP != nil ||
			req.WebhookRefreshInterval != nil || req.WebhookResolveTTL != nil {
			if blk.Alerts.Webhook == nil {
				blk.Alerts.Webhook = &config.ObservabilityWebhook{}
			}
			w := blk.Alerts.Webhook
			if req.WebhookDialect != nil {
				w.Dialect = *req.WebhookDialect
			}
			if req.WebhookAllowInsecureHTTP != nil {
				w.AllowInsecureHTTP = req.WebhookAllowInsecureHTTP
			}
			if req.WebhookRefreshInterval != nil {
				d := config.Duration(*req.WebhookRefreshInterval)
				w.RefreshInterval = &d
			}
			if req.WebhookResolveTTL != nil {
				d := config.Duration(*req.WebhookResolveTTL)
				w.ResolveTTL = &d
			}
		}
	}

	if err := b.persistConfig(cfg); err != nil {
		return ObservabilitySettings{}, err
	}

	return observabilitySettingsOf(b.state.Load().inner.Config), nil
}
