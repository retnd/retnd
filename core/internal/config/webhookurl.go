package config

import (
	"errors"
	"net/url"
	"time"
)

// CheckWebhookURL is the one place a webhook alert target is judged,
// shared by Load (so a bad one is refused before anything runs) and by the
// sink that delivers to it (so a caller that skipped Load cannot skip the
// rule).
//
// It refuses a non-HTTP scheme, plain http unless allowInsecureHTTP is set,
// a URL with no host, and a URL carrying userinfo. It does not refuse a
// private, loopback or link-local host, on purpose: the common correct
// target is http://alertmanager:9093 on a compose network, and a blanket
// private-range block would break the example bundle this feature ships.
//
// The URL is a credential (a Slack, Teams or Discord webhook carries its
// secret in the path), and url.Parse echoes its input in its errors, so
// none of the errors returned here contain any part of raw.
func CheckWebhookURL(raw string, allowInsecureHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("the webhook URL does not parse")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowInsecureHTTP {
			return errors.New("the webhook URL uses plain http; use https, or set allow_insecure_http for a trusted private network")
		}
	default:
		return errors.New("the webhook URL scheme must be https (or http with allow_insecure_http)")
	}
	if u.Hostname() == "" {
		return errors.New("the webhook URL has no host")
	}
	if u.User != nil {
		return errors.New("the webhook URL must not carry userinfo (user:password@host)")
	}
	return nil
}

// WebhookDeliveryBound is how long one alert delivery attempt may take
// (internal/alert's deliveryTimeout is this value). A refresh leaves when it
// is due and can take this long to land, so resolve_ttl has to clear
// refresh_interval by at least this much, or a slow receiver answers after it
// has already expired the alert (#1518's review).
const WebhookDeliveryBound = 30 * time.Second
