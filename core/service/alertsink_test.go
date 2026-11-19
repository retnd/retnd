package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/obs"
)

// EPIC O, O1.3 (FR-101): which one sink a configuration delivers through,
// and what a change of channel does to the firing map. The receiver is a
// local httptest server; nothing here contacts a real webhook.

// webhookReceiver counts the requests its handler sees.
type webhookReceiver struct {
	srv  *httptest.Server
	hits atomic.Int32
}

func newWebhookReceiver(t *testing.T) *webhookReceiver {
	t.Helper()
	r := &webhookReceiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		_, _ = io.Copy(io.Discard, q.Body)
		r.hits.Add(1)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// selectWebhook edits the config file the way an operator would: the URL
// goes in a 0600 file in a 0700 directory (the custody shape a url_ref
// needs), and the observability block names it.
func selectWebhook(t *testing.T, configPath string, r *webhookReceiver) {
	t.Helper()
	writeWebhookBlock(t, configPath, r, "webhook")
}

// writeWebhookBlock writes the observability block with delivery set as
// given, so a test can provision the webhook ahead of the switch.
func writeWebhookBlock(t *testing.T, configPath string, r *webhookReceiver, delivery string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	urlFile := filepath.Join(dir, "webhook-url")
	if err := os.WriteFile(urlFile, []byte(r.srv.URL+"/hook"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	block := "observability:\n  alerts:\n    delivery: " + delivery + "\n    webhook:\n      dialect: generic\n      allow_insecure_http: true\n      url_ref:\n        file: " + urlFile + "\n"
	if err := os.WriteFile(configPath, append(content, block...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sinkLog records every construction through the sinkBuilt seam.
type sinkLog struct {
	mu    sync.Mutex
	built []alert.Sink
}

func (l *sinkLog) add(s alert.Sink) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.built = append(l.built, s)
}

func (l *sinkLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.built)
}

// O-11: exactly one alert.Sink is constructed per configuration, on either
// delivery value, and the platform sink is not built beside the webhook.
// Counted through the construction seam, not read off the config.
func TestAlertSelection_ConstructsExactlyOneSink(t *testing.T) {
	t.Run("platform", func(t *testing.T) {
		configPath := writeAlertingConfigFile(t, true)
		svc, cleanup, err := Open(context.Background(), configPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cleanup() })
		var log sinkLog
		svc.sinkBuilt = log.add
		if !svc.EnableAlerts(&countingSink{}) {
			t.Fatal("platform delivery did not enable alerting")
		}
		if log.count() != 1 {
			t.Fatalf("built %d sinks, want exactly 1", log.count())
		}
		if _, ok := log.built[0].(sinkAdapter); !ok {
			t.Fatalf("platform delivery built %T, want the platform adapter", log.built[0])
		}
	})

	t.Run("webhook with a platform sink on offer", func(t *testing.T) {
		configPath := writeAlertingConfigFile(t, true)
		r := newWebhookReceiver(t)
		selectWebhook(t, configPath, r)
		svc, cleanup, err := Open(context.Background(), configPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cleanup() })
		var log sinkLog
		svc.sinkBuilt = log.add
		platform := &countingSink{}
		if !svc.EnableAlerts(platform) {
			t.Fatal("webhook delivery did not enable alerting")
		}
		if log.count() != 1 {
			t.Fatalf("built %d sinks, want exactly 1: selection, not fan-out", log.count())
		}
		if _, ok := log.built[0].(*alert.HTTPSink); !ok {
			t.Fatalf("webhook delivery built %T, want the HTTP sink", log.built[0])
		}
	})

	t.Run("webhook needs no platform capability", func(t *testing.T) {
		configPath := writeAlertingConfigFile(t, true)
		selectWebhook(t, configPath, newWebhookReceiver(t))
		svc, cleanup, err := Open(context.Background(), configPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cleanup() })
		if !svc.EnableAlerts(nil) {
			t.Fatal("a platform with no notification capability stopped the webhook")
		}
	})
}

// FR-101 reload seam: switching platform to webhook builds the one new sink,
// carries the firing map and delivered flags, does not re-announce, and the
// next new condition goes out through the new channel only.
func TestAlertSelection_SwitchingDeliveryCarriesTheFiringMap(t *testing.T) {
	configPath := writeAlertingConfigFile(t, true)
	r := newWebhookReceiver(t)
	writeWebhookBlock(t, configPath, r, "platform")
	svc, cleanup, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	platform := &countingSink{}
	if !svc.EnableAlerts(platform) {
		t.Fatal("platform delivery did not enable alerting")
	}
	ctx := context.Background()

	svc.state.Load().inner.RunCycle(ctx)
	waitStale()
	svc.state.Load().inner.AlertTick(ctx)
	if platform.count() != 1 {
		t.Fatalf("platform alerts = %d before the switch, want 1 (all: %v)", platform.count(), platform.kinds())
	}
	before := svc.state.Load().inner.Alerts.FiringSnapshot()
	if len(before) != 1 {
		t.Fatalf("firing map before the switch = %v, want the one delivered condition", before)
	}

	// The switch goes through UpdateObservabilitySettings, which is what
	// PATCH /settings/observability calls: it rewrites config.yaml and hot
	// reloads.
	var log sinkLog
	svc.sinkBuilt = log.add
	webhook := "webhook"
	if _, err := svc.UpdateObservabilitySettings(ctx, UpdateObservabilitySettingsRequest{AlertsDelivery: &webhook}); err != nil {
		t.Fatalf("UpdateObservabilitySettings: %v", err)
	}
	if log.count() != 1 {
		t.Fatalf("the switch built %d sinks, want exactly 1", log.count())
	}

	reloaded := svc.state.Load().inner
	after := reloaded.Alerts.FiringSnapshot()
	if len(after) != len(before) {
		t.Fatalf("firing map after the switch = %v, want %v carried", after, before)
	}
	for k, delivered := range before {
		if got, ok := after[k]; !ok || got != delivered {
			t.Fatalf("condition %q lost or its delivered flag changed across the switch: %v -> %v", k, before, after)
		}
	}

	reloaded.AlertTick(ctx)
	if r.hits.Load() != 0 || platform.count() != 1 {
		t.Fatalf("the switch re-announced a delivered condition (webhook %d, platform %d)", r.hits.Load(), platform.count())
	}

	// Positive control: the new channel is live, and only it.
	reloaded.Capacity = criticalThresholds
	reloaded.AlertTick(ctx)
	if r.hits.Load() != 1 {
		t.Fatalf("a new condition after the switch reached the webhook %d times, want 1", r.hits.Load())
	}
	if platform.count() != 1 {
		t.Fatalf("the platform sink got %d alerts after the switch, want it left at 1", platform.count())
	}
}

// An unchanged observability block must not rebuild the sink on every
// reload: a reload that builds a sink each time is a second sink per
// configuration.
func TestAlertSelection_AnUnchangedBlockBuildsNoNewSink(t *testing.T) {
	svc, _, _ := openAlertingService(t, true)
	var log sinkLog
	svc.sinkBuilt = log.add
	if _, err := svc.CreateBackupSet(context.Background(), validCreateReq(t, svc, "new-set")); err != nil {
		t.Fatal(err)
	}
	if log.count() != 0 {
		t.Fatalf("a reload with the alerts block unchanged built %d sinks, want 0", log.count())
	}
}

// waitStale lets the 1ms stale_after the alerting fixtures use outrun the
// cycle that just finished (the same floor alerts_test.go waits).
func waitStale() { time.Sleep(20 * time.Millisecond) }

// Decision 3: a webhook whose URL cannot be read must never be silent. The
// service says so through the settings read, keeps the firing map, and heals
// when the reference becomes readable.
func TestAlertSelection_AnUnreadableURLIsLoudAndHeals(t *testing.T) {
	secrets := filepath.Join(t.TempDir(), "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	urlFile := filepath.Join(secrets, "webhook-url")
	for name, ref := range map[string]string{
		"missing file":    "file: " + urlFile,
		"failing command": `command: ["/bin/false"]`,
	} {
		t.Run(name, func(t *testing.T) {
			_ = os.Remove(urlFile)
			configPath := writeAlertingConfigFile(t, true)
			content, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			block := "observability:\n  alerts:\n    delivery: webhook\n    webhook:\n      dialect: generic\n      allow_insecure_http: true\n      url_ref:\n        " + ref + "\n"
			if err := os.WriteFile(configPath, append(content, block...), 0o644); err != nil {
				t.Fatal(err)
			}
			svc, cleanup, err := Open(context.Background(), configPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cleanup() })
			ctx := context.Background()

			if !svc.EnableAlerts(nil) {
				t.Fatal("an unreadable URL switched alerting off: a webhook that cannot be built must stay on and say so")
			}
			status := func() string {
				s, err := svc.ObservabilitySettings(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return s.Alerts.Webhook.URLStatus
			}
			if got := status(); got != "unresolvable" {
				t.Fatalf("url status = %q right after wiring, want unresolvable", got)
			}

			svc.state.Load().inner.RunCycle(ctx)
			waitStale()
			svc.state.Load().inner.AlertTick(ctx)
			snap := svc.state.Load().inner.Alerts.FiringSnapshot()
			if len(snap) != 1 {
				t.Fatalf("firing map = %v, want the stale condition kept", snap)
			}
			for k, delivered := range snap {
				if delivered {
					t.Fatalf("condition %q reads delivered with no URL to deliver to", k)
				}
			}
			if got := status(); got != "unresolvable" {
				t.Fatalf("url status = %q after a pass, want unresolvable", got)
			}
			if got := destinationStates(t, svc); len(got) != 1 || got[0] != "unresolvable" {
				t.Fatalf("activity feed alert_destination states = %v, want exactly one unresolvable, said once and not per pass", got)
			}

			if name != "missing file" {
				return
			}
			receiver := newWebhookReceiver(t)
			if err := os.WriteFile(urlFile, []byte(receiver.srv.URL+"/hook"), 0o600); err != nil {
				t.Fatal(err)
			}
			svc.state.Load().inner.AlertTick(ctx)
			if got := status(); got != "ok" {
				t.Fatalf("url status = %q once the file exists, want ok: the state must heal itself", got)
			}
			if got := destinationStates(t, svc); len(got) != 2 || got[1] != "ok" {
				t.Fatalf("activity feed alert_destination states = %v, want the heal said once after the failure", got)
			}
			// The held condition went out in the same tick that healed.
			if receiver.hits.Load() != 1 {
				t.Fatalf("receiver got %d requests after the heal, want the held alert delivered once", receiver.hits.Load())
			}
		})
	}
}

// destinationStates is every alert_destination state the deployment's
// activity feed holds, oldest first.
func destinationStates(t *testing.T, svc *BackupService) []string {
	t.Helper()
	live, err := svc.LiveActivity(context.Background(), LiveActivityRequest{DeploymentOnly: true, Limit: liveActivityMaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	if live.Deployment == nil {
		return out
	}
	for _, e := range live.Deployment.Events {
		if e.Event != obs.EventAlertDestination {
			continue
		}
		v, _ := eventField(e, "status")
		out = append(out, v)
	}
	return out
}
