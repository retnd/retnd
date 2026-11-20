package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
)

// #1567, the first case: several alerts, a slow receiver, and a configuration
// right at the 30s rule. Refreshes used to go out one at a time, so alerts
// first sent in the same pass fell due together and the last of them landed
// after Alertmanager had already expired it (the review's throwaway test
// landed the third refresh 10s late). This is that test kept: the real config
// load, the real HTTP sink, the real refresh loop on the real clock, three
// alerts, refresh_interval 1s, resolve_ttl 31s, and a receiver that takes 20s
// to answer every request.
//
// What it asserts is what Alertmanager would see, per alert: no request
// arrives already expired, and each one arrives before the endsAt of the one
// before it, so the alert never lapses while the condition is still true.
//
// Falsify: send the due refreshes one at a time again (a plain loop over
// refreshOne in Dispatcher.Refresh, and over deliver in Observe).

// slowAlertmanager records what an Alertmanager would: when each alert
// arrived and the endsAt it carried. It answers after delay, or as soon as
// the request is abandoned.
type slowAlertmanager struct {
	srv   *httptest.Server
	delay time.Duration
	mu    sync.Mutex
	got   map[string][]amArrival
}

type amArrival struct {
	at     time.Time
	endsAt time.Time
}

func newSlowAlertmanager(t *testing.T, delay time.Duration) *slowAlertmanager {
	t.Helper()
	am := &slowAlertmanager{delay: delay, got: map[string][]amArrival{}}
	am.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		at := time.Now()
		body, _ := io.ReadAll(q.Body)
		var alerts []struct {
			Labels map[string]string `json:"labels"`
			EndsAt time.Time         `json:"endsAt"`
		}
		if err := json.Unmarshal(body, &alerts); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		am.mu.Lock()
		for _, a := range alerts {
			key := a.Labels["alertname"] + " " + a.Labels["scope"]
			am.got[key] = append(am.got[key], amArrival{at: at, endsAt: a.EndsAt})
		}
		am.mu.Unlock()
		select {
		case <-time.After(am.delay):
		case <-q.Context().Done():
		}
	}))
	t.Cleanup(am.srv.Close)
	return am
}

func (am *slowAlertmanager) arrivals() map[string][]amArrival {
	am.mu.Lock()
	defer am.mu.Unlock()
	out := make(map[string][]amArrival, len(am.got))
	for k, v := range am.got {
		out[k] = append([]amArrival(nil), v...)
		sort.Slice(out[k], func(i, j int) bool { return out[k][i].at.Before(out[k][j].at) })
	}
	return out
}

// lapses reports every moment an alert was expired in Alertmanager between
// start and end: a request that arrived already past its own endsAt, a request
// that arrived after the previous one's endsAt, and an alert whose last endsAt
// fell before end.
func (am *slowAlertmanager) lapses(keys []string, start, end time.Time) []string {
	var out []string
	got := am.arrivals()
	for _, key := range keys {
		seen := got[key]
		if len(seen) == 0 {
			out = append(out, fmt.Sprintf("%s never reached the receiver", key))
			continue
		}
		for i, a := range seen {
			if !a.endsAt.After(a.at) {
				out = append(out, fmt.Sprintf("%s: request %d arrived %s after start already expired (endsAt %s after start)",
					key, i+1, a.at.Sub(start).Round(time.Second), a.endsAt.Sub(start).Round(time.Second)))
			}
			if i > 0 && a.at.After(seen[i-1].endsAt) {
				out = append(out, fmt.Sprintf("%s: request %d arrived %s after start, %s after the alert expired",
					key, i+1, a.at.Sub(start).Round(time.Second), a.at.Sub(seen[i-1].endsAt).Round(time.Second)))
			}
		}
		if last := seen[len(seen)-1]; last.endsAt.Before(end) {
			out = append(out, fmt.Sprintf("%s: expired %s after start and was not refreshed by %s",
				key, last.endsAt.Sub(start).Round(time.Second), end.Sub(start).Round(time.Second)))
		}
	}
	return out
}

func TestAlertRefresh_ThreeAlertsAndASlowReceiverNeverLapse_1567(t *testing.T) {
	t.Parallel()
	const (
		receiverDelay = 20 * time.Second
		window        = 50 * time.Second
	)
	am := newSlowAlertmanager(t, receiverDelay)
	configPath := writeAlertingConfigFile(t, true)
	r := &webhookReceiver{srv: am.srv}
	writeWebhookBlock(t, configPath, r, "webhook")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "dialect: generic", "dialect: alertmanager\n      refresh_interval: 1s\n      resolve_ttl: 31s", 1))
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	svc, cleanup, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	if !svc.EnableAlerts(nil) {
		t.Fatal("alerting did not come on")
	}
	d := svc.state.Load().inner.Alerts
	if d.RefreshEvery() != time.Second {
		t.Fatalf("the dispatcher refreshes every %s, want the configured 1s", d.RefreshEvery())
	}

	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); svc.runAlertRefreshes(ctx) }()

	// Three conditions true in the same pass, as a destination outage that
	// fails every set at once would make them. The pass runs where the
	// scheduler would run it, beside the refresh loop and not in front of it.
	conditions := []alert.Condition{
		{Kind: alert.StaleBackup, Scope: "nas/photos", Detail: "no good backup"},
		{Kind: alert.StaleBackup, Scope: "nas/docs", Detail: "no good backup"},
		{Kind: alert.StaleBackup, Scope: "nas/mail", Detail: "no good backup"},
	}
	keys := make([]string, len(conditions))
	for i, c := range conditions {
		keys[i] = c.Kind.String() + " " + c.Scope
	}
	start := time.Now()
	passDone := make(chan struct{})
	go func() { defer close(passDone); d.Observe(ctx, conditions, nil, start) }()

	time.Sleep(window)
	end := time.Now()
	cancel()
	<-loopDone
	<-passDone

	for _, l := range am.lapses(keys, start, end) {
		t.Error(l)
	}
}
