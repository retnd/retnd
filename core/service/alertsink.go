package service

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/secretref"
)

// EPIC O, FR-101: which one alert.Sink a configuration delivers through.
//
// observability.alerts.delivery is a selection and never a fan-out. This is
// the only function that constructs a sink, it constructs at most one per
// call, and the platform sink and the webhook sink are never both built:
// internal/alert's package gate keeps the dispatcher side structurally
// single, and the construction counter below (sinkBuilt) keeps this side
// honest.

// alertSinkFor builds the single sink cfg selects, or returns nil, nil when
// there is nothing to deliver through (the platform offers no notification
// capability and delivery is "platform"). An error means the selected
// channel could not be built, and it never carries the webhook URL.
func (b *BackupService) alertSinkFor(ctx context.Context, cfg *config.Config) (alert.Sink, error) {
	var sink alert.Sink
	switch a := cfg.Observability.Alerts; a.Delivery {
	case config.ObservabilityDeliveryWebhook:
		w := a.Webhook
		ref := secretref.Ref{File: w.URLRef.File, Env: w.URLRef.Env, Command: w.URLRef.Command}
		// The URL is read at every delivery (custody checked each time, the
		// value never kept), so a rotated file is used by the next alert and
		// an unreadable one is a state the sink reports, never a silence.
		hs, err := alert.NewHTTPSink(func(ctx context.Context) (obs.Secret, error) {
			return secretref.Resolve(ctx, ref)
		}, w.Dialect, w.ResolveTTL, w.RefreshInterval, w.AllowInsecureHTTP)
		if err != nil {
			return nil, fmt.Errorf("observability.alerts.webhook: %w", err)
		}
		sink = hs
	default:
		if b.alertSink == nil {
			return nil, nil
		}
		sink = sinkAdapter{sink: b.alertSink}
	}
	if b.sinkBuilt != nil {
		b.sinkBuilt(sink)
	}
	return sink, nil
}

// carryAlerts decides alerting on a Service built by a configuration hot
// reload, replacing the old "adopt the dispatcher, or enable from the stored
// platform sink" rule.
//
// While the delivery selection is unchanged the previous dispatcher is
// carried as it always was (AdoptAlerts). When it changed, the one new sink
// is built and a dispatcher over it adopts the previous dispatcher's firing
// map and delivered flags, so exactly one sink exists per configuration and
// a condition that was already delivered is not announced again just
// because the channel moved. A webhook whose URL file changes content under
// an unchanged block is not re-read here: that takes a PATCH or a restart.
func (b *BackupService) carryAlerts(ctx context.Context, prev, next *app.Service) {
	if prev.Alerts != nil && reflect.DeepEqual(prev.Config.Observability.Alerts, next.Config.Observability.Alerts) &&
		next.AdoptAlerts(prev.Alerts) {
		return
	}
	sink, err := b.alertSinkFor(ctx, next.Config)
	if err != nil {
		b.logger.Error(ctx, "alert-sink", err)
		return
	}
	if next.EnableAlerts(sink) {
		next.Alerts.AdoptState(prev.Alerts)
		// So the settings read is true from the first moment, not after the
		// first alert.
		next.Alerts.Probe(ctx)
	}
}

// runAlertRefreshes re-sends delivered conditions to a receiver that
// forgets them (the Alertmanager dialect). Observe runs at the poll
// interval, which is usually far longer than a refresh interval, so
// refreshing only from the alerting pass would let the alert self-resolve
// between passes. It decides nothing: Dispatcher.Refresh reads the firing
// map the last pass left, and does nothing for a sink that does not refresh.
// The interval is re-read each round because a reload can switch the
// channel under a running loop.
func (b *BackupService) runAlertRefreshes(ctx context.Context) {
	for {
		wait := time.Minute
		if every := b.state.Load().inner.Alerts.RefreshEvery(); every > 0 {
			wait = max(every/4, time.Second)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		b.state.Load().inner.Alerts.Refresh(ctx, time.Now())
	}
}
