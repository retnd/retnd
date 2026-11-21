package config

import (
	"context"
	"time"

	"github.com/retnd/retnd/core/internal/secretref"
)

// validateWebhookDelivery is #901's half of resolveObservability (EPIC O,
// FR-101): what Validate can say about a webhook target before anything is
// delivered. It runs only when delivery is "webhook".
//
// The webhook URL is a secret, so the block holds a reference to it and
// never the value, and Validate does not carry what it reads: it resolves
// the reference only to judge the URL's shape (CheckWebhookURL) and drops
// the result. A reference it cannot read here (a file that is not
// provisioned yet, a command, which Validate never runs) is not a Validate
// error: reloading a configuration must not become impossible because a
// secret file moved, and the sink reports an unreadable reference when it is
// built. What it can read and judge wrong is refused, in words that never
// contain any of it.
func validateWebhookDelivery(v *validator, p string, a ObservabilityAlerts) {
	if a.Delivery != ObservabilityDeliveryWebhook {
		return
	}
	ref := secretref.Ref{File: a.Webhook.URLRef.File, Env: a.Webhook.URLRef.Env, Command: a.Webhook.URLRef.Command}
	if err := ref.Validate(); err != nil {
		v.addf("%s.alerts.webhook.url_ref: delivery is webhook, so exactly one of file, env or command must say where the URL is kept", p)
		return
	}
	if len(ref.Command) > 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	target, err := secretref.Resolve(ctx, ref)
	if err != nil {
		return
	}
	if err := CheckWebhookURL(target.Reveal(), a.Webhook.AllowInsecureHTTP); err != nil {
		v.addf("%s.alerts.webhook.url_ref: %v", p, err)
	}
}
