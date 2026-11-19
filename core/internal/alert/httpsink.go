package alert

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/obs"
)

// The two webhook dialects. Both are built in Go from an Alert's five
// fields, and neither is templatable, ever: a template is an injection
// surface and makes redaction untestable.
const (
	DialectGeneric      = "generic"
	DialectAlertmanager = "alertmanager"
)

// maxWebhookResponse bounds how much of a receiver's answer is read before
// it is discarded.
const maxWebhookResponse = 64 << 10

// HTTPSink delivers an Alert as an HTTP POST. It is the one Sink a
// deployment selects with observability.alerts.delivery: webhook.
//
// Transport guards, none of them optional:
//   - redirects are never followed; a 3xx is a delivery failure and its
//     Location is not read, let alone logged;
//   - the response body is bounded and discarded;
//   - TLS verification is on and no knob turns it off;
//   - the URL (https, or http only when allow_insecure_http) is checked
//     again here, so a caller that skipped config.Load still cannot
//     deliver to userinfo or a non-HTTP scheme.
//
// Private, loopback and link-local targets are allowed on purpose (see
// config.CheckWebhookURL).
//
// The URL is a secret. It is read from its source for each request, revealed
// only to build that request, and no error or log line this type produces contains it: a
// failure reports the host and the status code, nothing else.
type HTTPSink struct {
	// source reads the URL. It is called at every delivery attempt and
	// never cached, so a rotated secret is used by the next alert and a
	// reference that stops reading is noticed at once.
	source            func(context.Context) (obs.Secret, error)
	allowInsecureHTTP bool
	dialect           string
	ttl               time.Duration
	refresh           time.Duration
	client            *http.Client
	// status is what the last read of the URL found. A pointer, so the
	// struct stays copyable and Format can keep its value receiver (which
	// is what stops fmt walking a dereferenced sink).
	status *atomic.Int32
}

// What the last attempt to read the URL found. Unknown is "not tried yet".
const (
	URLStatusUnknown      = "unknown"
	URLStatusOK           = "ok"
	URLStatusUnresolvable = "unresolvable"
	URLStatusInvalid      = "invalid"
)

var (
	_ Sink           = (*HTTPSink)(nil)
	_ RefreshingSink = (*HTTPSink)(nil)
)

// NewHTTPSink builds the sink. source reads the URL (a secret) and is called
// at every delivery, so the sink holds no URL of its own: nothing to leak,
// nothing to go stale. resolveTTL is the Alertmanager dialect's endsAt offset
// and refreshInterval how often a still-observed condition is re-sent; both
// are ignored (and refresh is off) in the generic dialect.
func NewHTTPSink(source func(context.Context) (obs.Secret, error), dialect string, resolveTTL, refreshInterval time.Duration, allowInsecureHTTP bool) (*HTTPSink, error) {
	if dialect != DialectGeneric && dialect != DialectAlertmanager {
		return nil, fmt.Errorf("unknown webhook dialect %q", dialect)
	}
	if dialect == DialectAlertmanager && (refreshInterval <= 0 || refreshInterval >= resolveTTL) {
		return nil, errors.New("the webhook refresh interval must be positive and shorter than resolve_ttl")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &HTTPSink{
		source:            source,
		allowInsecureHTTP: allowInsecureHTTP,
		dialect:           dialect,
		ttl:               resolveTTL,
		refresh:           refreshInterval,
		status:            new(atomic.Int32),
		client: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// resolve reads the URL now and judges it. The returned secret is for the
// one request and is not kept; the host is the only part that may appear in
// an error. Either failure leaves the sink's status saying so.
func (w *HTTPSink) resolve(ctx context.Context) (target obs.Secret, host string, err error) {
	target, err = w.source(ctx)
	if err != nil {
		w.status.Store(statusUnresolvable)
		return obs.Secret{}, "", fmt.Errorf("the webhook URL is unresolvable (observability.alerts.webhook.url_ref): %w", err)
	}
	raw := target.Reveal()
	if err := config.CheckWebhookURL(raw, w.allowInsecureHTTP); err != nil {
		w.status.Store(statusInvalid)
		return obs.Secret{}, "", fmt.Errorf("the webhook URL is invalid: %w", err)
	}
	u, _ := url.Parse(raw)
	w.status.Store(statusOK)
	return target, u.Host, nil
}

const (
	statusOK int32 = iota + 1
	statusUnresolvable
	statusInvalid
)

// Probe reads and judges the URL without sending anything, so the status is
// true between alerts too.
func (w *HTTPSink) Probe(ctx context.Context) error {
	_, _, err := w.resolve(ctx)
	return err
}

// URLStatus says what the last read of the URL found.
func (w *HTTPSink) URLStatus() string {
	switch w.status.Load() {
	case statusOK:
		return URLStatusOK
	case statusUnresolvable:
		return URLStatusUnresolvable
	case statusInvalid:
		return URLStatusInvalid
	}
	return URLStatusUnknown
}

// Deliver sends the first notification for an occurrence.
func (w *HTTPSink) Deliver(ctx context.Context, a Alert) error {
	return w.post(ctx, a, a.ObservedAt.Add(w.ttl))
}

// RefreshInterval is zero in the generic dialect, which keeps observe-once.
func (w *HTTPSink) RefreshInterval() time.Duration {
	if w.dialect != DialectAlertmanager {
		return 0
	}
	return w.refresh
}

// Refresh re-sends a condition that is still true, with a new expiry.
func (w *HTTPSink) Refresh(ctx context.Context, a Alert, now time.Time) error {
	if w.dialect != DialectAlertmanager {
		return nil
	}
	return w.post(ctx, a, now.Add(w.ttl))
}

// Resolve closes the alert by sending it with endsAt = now.
func (w *HTTPSink) Resolve(ctx context.Context, a Alert, now time.Time) error {
	if w.dialect != DialectAlertmanager {
		return nil
	}
	return w.post(ctx, a, now)
}

func (w *HTTPSink) post(ctx context.Context, a Alert, endsAt time.Time) error {
	var body []byte
	switch w.dialect {
	case DialectAlertmanager:
		body, _ = json.Marshal([]map[string]any{{
			"labels":      map[string]string{"alertname": a.Kind.String(), "scope": a.Scope},
			"annotations": map[string]string{"summary": a.Title, "description": a.Message},
			"startsAt":    a.ObservedAt.UTC().Format(time.RFC3339),
			"endsAt":      endsAt.UTC().Format(time.RFC3339),
		}})
	default:
		body, _ = json.Marshal(struct {
			Kind       string `json:"kind"`
			Scope      string `json:"scope"`
			Title      string `json:"title"`
			Message    string `json:"message"`
			ObservedAt string `json:"observed_at"`
			Schema     int    `json:"schema"`
		}{a.Kind.String(), a.Scope, a.Title, a.Message, a.ObservedAt.UTC().Format(time.RFC3339), 1})
	}

	target, host, err := w.resolve(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.Reveal(), bytes.NewReader(body))
	if err != nil {
		return errors.New("the webhook request could not be built")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		// err is a *url.Error carrying the full URL, so it is classified
		// and dropped rather than wrapped.
		return fmt.Errorf("webhook delivery to %s failed: %s", host, classifyTransportError(err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxWebhookResponse))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return fmt.Errorf("webhook %s answered %d: redirects are never followed", host, resp.StatusCode)
	default:
		return fmt.Errorf("webhook %s answered %d", host, resp.StatusCode)
	}
}

func classifyTransportError(err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case isCertError(err):
		return "the server certificate was not accepted"
	case errors.As(err, &netErr):
		return "network error"
	}
	return "request failed"
}

func isCertError(err error) bool {
	var verify *tls.CertificateVerificationError
	return errors.As(err, &verify)
}

// Format keeps every fmt verb (%v, %+v, %#v) from walking the struct and
// printing the URL: fmt reaches unexported fields by reflection, where
// obs.Secret's own Format never runs.
func (w HTTPSink) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "HTTPSink{%s}", w.dialect)
}
