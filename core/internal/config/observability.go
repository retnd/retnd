package config

import "time"

// The `observability:` block (EPIC O, #898; FR-103). One new top-level key
// holds all three integrations, so a rolled-back build meets one unknown
// key under Load's KnownFields(true) and not several. The existing
// `alerts:` block is neither extended nor moved.
//
// Global only. A scrape endpoint, a log stream and an alert channel are
// properties of a deployment and not of a backup set, so there is no
// per-set form: a per-set log level could not be honoured by one
// process-wide writer, and a per-set scrape credential means nothing.
//
// The three sub-blocks are declared here, all at once, so the three issues
// that fill them in (#899 metrics, #900 logs, #901 alerts) add behaviour and
// validation inside their own sub-struct and never a top-level key. A
// configuration that never writes the key resolves the defaults below, which
// are everything off, and omitempty on the block keeps a re-marshal from
// injecting it.

// The words and defaults of the block.
const (
	DefaultMetricsMinRebuildInterval = 15 * time.Second
	MinMetricsMinRebuildInterval     = time.Second

	ObservabilityLogLevelDebug = "debug"
	ObservabilityLogLevelInfo  = "info"
	ObservabilityLogLevelWarn  = "warn"
	ObservabilityLogLevelError = "error"

	ObservabilityDeliveryPlatform = "platform"
	ObservabilityDeliveryWebhook  = "webhook"

	ObservabilityDialectGeneric      = "generic"
	ObservabilityDialectAlertmanager = "alertmanager"

	DefaultWebhookRefreshInterval = 4 * time.Minute
	DefaultWebhookResolveTTL      = 10 * time.Minute

	// ScrapeTokenMintCommand is the command a refusal names, so an operator
	// who pasted a token into config.yaml is told how to get a real one.
	ScrapeTokenMintCommand = "retnd settings observability rotate-scrape-token"
)

// ObservabilityBlock is the `observability:` block as the operator wrote
// it. Nil on Config means they wrote nothing.
type ObservabilityBlock struct {
	Metrics *ObservabilityMetricsBlock `yaml:"metrics,omitempty"`
	Logs    *ObservabilityLogsBlock    `yaml:"logs,omitempty"`
	Alerts  *ObservabilityAlertsBlock  `yaml:"alerts,omitempty"`
}

// ObservabilityMetricsBlock is `observability.metrics`.
type ObservabilityMetricsBlock struct {
	Enabled            *bool     `yaml:"enabled,omitempty"`
	MinRebuildInterval *Duration `yaml:"min_rebuild_interval,omitempty"`
	// Token exists only so that a plaintext scrape token is refused by
	// name, with the command that mints one properly, rather than by an
	// anonymous unknown-field error. It is never read, never resolved and
	// never echoed: a field that merely goes unread is a field a future
	// reader wires up (matrix row O-03).
	Token string `yaml:"token,omitempty"`
}

// ObservabilityLogsBlock is `observability.logs`.
type ObservabilityLogsBlock struct {
	Level string `yaml:"level,omitempty"`
}

// ObservabilityAlertsBlock is `observability.alerts`.
type ObservabilityAlertsBlock struct {
	Delivery string                `yaml:"delivery,omitempty"`
	Webhook  *ObservabilityWebhook `yaml:"webhook,omitempty"`
}

// ObservabilityWebhook is `observability.alerts.webhook`. The URL is a
// credential (chat webhook URLs carry their secret in the path), so it is
// only ever a reference to where it lives, never a value.
type ObservabilityWebhook struct {
	Dialect           string                  `yaml:"dialect,omitempty"`
	URLRef            *ObservabilitySecretRef `yaml:"url_ref,omitempty"`
	AllowInsecureHTTP *bool                   `yaml:"allow_insecure_http,omitempty"`
	RefreshInterval   *Duration               `yaml:"refresh_interval,omitempty"`
	ResolveTTL        *Duration               `yaml:"resolve_ttl,omitempty"`
}

// ObservabilitySecretRef names where a secret lives: exactly the file, env
// and command shape the rest of this file uses, never a field to paste one
// into.
type ObservabilitySecretRef struct {
	File    string   `yaml:"file,omitempty"`
	Env     string   `yaml:"env,omitempty"`
	Command []string `yaml:"command,omitempty"`
}

// Observability is the resolved block, filled by Validate. Every consumer
// reads this and never the pointer on Config.
type Observability struct {
	Metrics ObservabilityMetrics
	Logs    ObservabilityLogs
	Alerts  ObservabilityAlerts
}

// ObservabilityMetrics is the resolved `observability.metrics`.
type ObservabilityMetrics struct {
	// Enabled is off by default. Disabled, /metrics answers 404.
	Enabled bool
	// MinRebuildInterval is how long a built health report is reused, so N
	// scrapers cost one journal read per interval.
	MinRebuildInterval time.Duration
}

// ObservabilityLogs is the resolved `observability.logs`.
type ObservabilityLogs struct {
	// Level is the configured floor. The environment still wins over it.
	Level string
}

// ObservabilityAlerts is the resolved `observability.alerts`.
type ObservabilityAlerts struct {
	// Delivery is "platform" (today's behaviour) or "webhook". Exactly one
	// sink is ever built either way.
	Delivery string
	Webhook  ObservabilityWebhookResolved
}

// ObservabilityWebhookResolved is the resolved webhook sub-block.
type ObservabilityWebhookResolved struct {
	Dialect           string
	URLRef            ObservabilitySecretRef
	AllowInsecureHTTP bool
	RefreshInterval   time.Duration
	ResolveTTL        time.Duration
}

// IsZero reports whether the operator wrote nothing worth keeping, so a
// writer that rebuilds the block can drop it.
func (b ObservabilityBlock) IsZero() bool {
	return b.Metrics == nil && b.Logs == nil && b.Alerts == nil
}

// resolveObservability is phase 2 of Validate for the block.
func (c *Config) resolveObservability(v *validator) {
	var b ObservabilityBlock
	if c.ObservabilityBlock != nil {
		b = *c.ObservabilityBlock
	}
	const p = "observability"
	out := Observability{
		Metrics: ObservabilityMetrics{MinRebuildInterval: DefaultMetricsMinRebuildInterval},
		Logs:    ObservabilityLogs{Level: ObservabilityLogLevelInfo},
		Alerts: ObservabilityAlerts{
			Delivery: ObservabilityDeliveryPlatform,
			Webhook: ObservabilityWebhookResolved{
				Dialect:         ObservabilityDialectGeneric,
				RefreshInterval: DefaultWebhookRefreshInterval,
				ResolveTTL:      DefaultWebhookResolveTTL,
			},
		},
	}

	if m := b.Metrics; m != nil {
		if m.Token != "" {
			// The value is deliberately not interpolated.
			v.addf("%s.metrics.token: a scrape token is never written in config.yaml; run `%s` to mint one, which prints it once and keeps only a digest", p, ScrapeTokenMintCommand)
		}
		if m.Enabled != nil {
			out.Metrics.Enabled = *m.Enabled
		}
		if m.MinRebuildInterval != nil {
			d := m.MinRebuildInterval.Duration()
			if d < MinMetricsMinRebuildInterval {
				v.addf("%s.metrics.min_rebuild_interval: %s is below the %s floor", p, d, MinMetricsMinRebuildInterval)
			} else {
				out.Metrics.MinRebuildInterval = d
			}
		}
	}

	if l := b.Logs; l != nil {
		switch l.Level {
		case "":
		case ObservabilityLogLevelDebug, ObservabilityLogLevelInfo, ObservabilityLogLevelWarn, ObservabilityLogLevelError:
			out.Logs.Level = l.Level
		default:
			v.addf("%s.logs.level: %q is not one of %q, %q, %q or %q", p, l.Level,
				ObservabilityLogLevelDebug, ObservabilityLogLevelInfo, ObservabilityLogLevelWarn, ObservabilityLogLevelError)
		}
	}

	if a := b.Alerts; a != nil {
		switch a.Delivery {
		case "":
		case ObservabilityDeliveryPlatform, ObservabilityDeliveryWebhook:
			out.Alerts.Delivery = a.Delivery
		default:
			v.addf("%s.alerts.delivery: %q is not one of %q or %q", p, a.Delivery, ObservabilityDeliveryPlatform, ObservabilityDeliveryWebhook)
		}
		if w := a.Webhook; w != nil {
			switch w.Dialect {
			case "":
			case ObservabilityDialectGeneric, ObservabilityDialectAlertmanager:
				out.Alerts.Webhook.Dialect = w.Dialect
			default:
				v.addf("%s.alerts.webhook.dialect: %q is not one of %q or %q", p, w.Dialect, ObservabilityDialectGeneric, ObservabilityDialectAlertmanager)
			}
			if w.URLRef != nil {
				out.Alerts.Webhook.URLRef = *w.URLRef
			}
			if w.AllowInsecureHTTP != nil {
				out.Alerts.Webhook.AllowInsecureHTTP = *w.AllowInsecureHTTP
			}
			if w.RefreshInterval != nil {
				if d := w.RefreshInterval.Duration(); d <= 0 {
					v.addf("%s.alerts.webhook.refresh_interval: must be positive (got %s)", p, d)
				} else {
					out.Alerts.Webhook.RefreshInterval = d
				}
			}
			if w.ResolveTTL != nil {
				if d := w.ResolveTTL.Duration(); d <= 0 {
					v.addf("%s.alerts.webhook.resolve_ttl: must be positive (got %s)", p, d)
				} else {
					out.Alerts.Webhook.ResolveTTL = d
				}
			}
		}
	}
	if out.Alerts.Webhook.RefreshInterval >= out.Alerts.Webhook.ResolveTTL {
		v.addf("%s.alerts.webhook.refresh_interval: %s must be below resolve_ttl %s, or a refresh arrives after the receiver has already expired the alert",
			p, out.Alerts.Webhook.RefreshInterval, out.Alerts.Webhook.ResolveTTL)
	}

	validateWebhookDelivery(v, p, out.Alerts)

	c.Observability = out
}
