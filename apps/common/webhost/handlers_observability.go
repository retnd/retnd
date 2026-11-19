package webhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// EPIC O's settings surface (#898; FR-103):
//
//	GET    /api/v1/settings/observability
//	PATCH  /api/v1/settings/observability
//	POST   /api/v1/settings/observability/scrape-token
//
// A sub-resource of /settings for the reasons /settings/workflow and
// /settings/assurance are: the block is its own thing with its own
// identities, and the settings page's poll does not want it.
//
// None of the three is an agent tool. The mint is on the agent deny list, so
// no agent token can mint a scrape credential, and the other two are named
// refusals in core/agenttools.
//
// Nothing here can carry a secret out. The read says whether a credential
// exists and shows its eight-character display prefix, and the webhook URL
// (a credential in its own right) is reported as a boolean.

// ObservabilityAccess is what the three routes need from core/service.
// *service.BackupService satisfies it; NewRouter finds it by asserting it on
// the backend, the way it finds AgentAccess, so a backend that cannot keep the
// block has no observability settings.
type ObservabilityAccess interface {
	ObservabilitySettings(ctx context.Context) (service.ObservabilitySettings, error)
	UpdateObservabilitySettings(ctx context.Context, req service.UpdateObservabilitySettingsRequest) (service.ObservabilitySettings, error)
}

func (h *handlers) observabilityAccess(w http.ResponseWriter) (ObservabilityAccess, bool) {
	if h.observability == nil {
		writeError(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", "this deployment cannot keep observability settings")

		return nil, false
	}

	return h.observability, true
}

// maxObservabilityBodyBytes bounds a PATCH body. Eight scalars never need
// more than this.
const maxObservabilityBodyBytes = 16 << 10

// getObservabilitySettings is GET /api/v1/settings/observability.
func (h *handlers) getObservabilitySettings(w http.ResponseWriter, r *http.Request) {
	obs, ok := h.observabilityAccess(w)
	if !ok {
		return
	}
	settings, err := obs.ObservabilitySettings(r.Context())
	if err != nil {
		h.internalError(w, r, "INTERNAL", "failed to read the observability settings", err)

		return
	}

	writeJSON(w, http.StatusOK, toObservabilityBody(settings))
}

// updateObservabilitySettings is PATCH /api/v1/settings/observability.
//
// CSRF and no destructive gate, the tier PATCH /settings sits in: it rewrites
// one block of config.yaml and hot-reloads, opens no storage and cannot reach
// a backup datum.
func (h *handlers) updateObservabilitySettings(w http.ResponseWriter, r *http.Request) {
	obs, ok := h.observabilityAccess(w)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxObservabilityBodyBytes)

	var body apicontract.UpdateObservabilitySettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDecodeError(w, err, maxObservabilityBodyBytes)

		return
	}

	req := service.UpdateObservabilitySettingsRequest{
		MetricsEnabled:           body.MetricsEnabled,
		LogsLevel:                body.LogsLevel,
		AlertsDelivery:           body.AlertsDelivery,
		WebhookDialect:           body.WebhookDialect,
		WebhookAllowInsecureHTTP: body.WebhookAllowInsecureHttp,
	}
	for _, f := range []struct {
		in    *int64
		out   **time.Duration
		field string
	}{
		{body.MetricsMinRebuildIntervalSeconds, &req.MetricsMinRebuildInterval, "metrics_min_rebuild_interval_seconds"},
		{body.WebhookRefreshIntervalSeconds, &req.WebhookRefreshInterval, "webhook_refresh_interval_seconds"},
		{body.WebhookResolveTtlSeconds, &req.WebhookResolveTTL, "webhook_resolve_ttl_seconds"},
	} {
		d, err := checkedSecondsPointerToDurationFromInt64(f.in, f.field)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())

			return
		}
		*f.out = d
	}

	settings, err := obs.UpdateObservabilitySettings(r.Context(), req)
	switch {
	case errors.Is(err, service.ErrInvalidRequest):
		// Safe to echo, on core/service's own guarantee: the text is built
		// from config's field descriptions and the caller's own values, and
		// the only secret-shaped thing in this block is a REFERENCE.
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case errors.Is(err, service.ErrConfigNotFileBacked):
		h.internalError(w, r, "INTERNAL", "this deployment has no configuration file to persist to", err)
	case err != nil:
		h.internalError(w, r, "INTERNAL", "failed to write the observability settings", err)
	default:
		writeJSON(w, http.StatusOK, toObservabilityBody(settings))
	}
}

// mintScrapeToken is POST /api/v1/settings/observability/scrape-token.
//
// Until the scrape credential's store lands it answers NOT_CONFIGURED, which
// is true: this deployment cannot yet keep one.
func (h *handlers) mintScrapeToken(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeError(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", "this deployment cannot keep a scrape credential")
}

func toObservabilityBody(s service.ObservabilitySettings) apicontract.ObservabilitySettingsResponse {
	var created *string
	if !s.Metrics.ScrapeToken.CreatedAt.IsZero() {
		t := s.Metrics.ScrapeToken.CreatedAt.UTC().Format(time.RFC3339)
		created = &t
	}

	return apicontract.ObservabilitySettingsResponse{
		Metrics: apicontract.ObservabilityMetricsSettings{
			Enabled:                   s.Metrics.Enabled,
			MinRebuildIntervalSeconds: int64(s.Metrics.MinRebuildInterval / time.Second),
			ScrapeToken: apicontract.ScrapeTokenState{
				Exists:    s.Metrics.ScrapeToken.Exists,
				Prefix:    s.Metrics.ScrapeToken.Prefix,
				CreatedAt: created,
			},
		},
		Logs: apicontract.ObservabilityLogsSettings{
			Level:        s.Logs.Level,
			LevelInForce: s.Logs.LevelInForce,
			LevelSource:  s.Logs.LevelSource,
		},
		Alerts: apicontract.ObservabilityAlertsSettings{
			Delivery: s.Alerts.Delivery,
			Webhook: apicontract.ObservabilityWebhookSettings{
				Dialect:                s.Alerts.Webhook.Dialect,
				URLSet:                 s.Alerts.Webhook.URLSet,
				AllowInsecureHttp:      s.Alerts.Webhook.AllowInsecureHTTP,
				RefreshIntervalSeconds: int64(s.Alerts.Webhook.RefreshInterval / time.Second),
				ResolveTtlSeconds:      int64(s.Alerts.Webhook.ResolveTTL / time.Second),
			},
		},
	}
}
