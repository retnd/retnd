package alert_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/obs"
)

// EPIC O, O1.3 (FR-101). Every receiver here is a local httptest server:
// nothing in this file contacts a real webhook, Slack or Alertmanager.

var t0 = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

type recorded struct {
	path string
	body []byte
}

// receiver records every request it gets and answers with status.
type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []recorded
	status int
}

func newReceiver(t *testing.T, status int) *receiver {
	t.Helper()
	r := &receiver{status: status}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		b, _ := io.ReadAll(q.Body)
		r.mu.Lock()
		r.got = append(r.got, recorded{path: q.URL.Path, body: b})
		st := r.status
		r.mu.Unlock()
		w.WriteHeader(st)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// fixedURL is a URL source that always answers u.
func fixedURL(u string) func(context.Context) (obs.Secret, error) {
	return func(context.Context) (obs.Secret, error) { return obs.NewSecret(u), nil }
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func (r *receiver) last(t *testing.T) recorded {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.got) == 0 {
		t.Fatal("the receiver saw no request")
	}
	return r.got[len(r.got)-1]
}

func (r *receiver) sink(t *testing.T, dialect string) *alert.HTTPSink {
	t.Helper()
	s, err := alert.NewHTTPSink(fixedURL(r.srv.URL+"/hook"), dialect, 10*time.Minute, 4*time.Minute, true)
	if err != nil {
		t.Fatalf("NewHTTPSink: %v", err)
	}
	return s
}

var stale = []alert.Condition{{Kind: alert.StaleBackup, Scope: "nas/photos", Detail: "no good backup for 3 days"}}

// O-09: generic delivery is observe-once, in both directions.
func TestWebhookGenericIsObserveOnce(t *testing.T) {
	r := newReceiver(t, 200)
	d := alert.NewDispatcher(r.sink(t, alert.DialectGeneric), nil)
	ctx := context.Background()

	d.Observe(ctx, stale, nil, t0)
	if r.count() != 1 {
		t.Fatalf("first observation sent %d requests, want 1", r.count())
	}
	for _, off := range []time.Duration{4 * time.Minute, time.Hour, 6 * time.Hour} {
		d.Observe(ctx, stale, nil, t0.Add(off))
		d.Refresh(ctx, t0.Add(off))
	}
	if r.count() != 1 {
		t.Fatalf("a persisting condition produced %d requests, want silence after the first", r.count())
	}
	d.Observe(ctx, nil, nil, t0.Add(7*time.Hour))
	if r.count() != 1 {
		t.Fatalf("generic mode sent %d requests on resolve, want none", r.count())
	}
	// Control: permanent silence would pass everything above.
	d.Observe(ctx, stale, nil, t0.Add(8*time.Hour))
	if r.count() != 2 {
		t.Fatalf("a recurrence after a resolve sent %d requests in total, want 2", r.count())
	}

	var obj map[string]any
	if err := json.Unmarshal(r.last(t).body, &obj); err != nil {
		t.Fatalf("body is not one JSON object: %v", err)
	}
	if len(obj) != 6 {
		t.Fatalf("generic body has keys %v, want exactly kind, scope, title, message, observed_at, schema", obj)
	}
	want := map[string]any{"kind": "STALE_BACKUP", "scope": "nas/photos", "message": "no good backup for 3 days",
		"observed_at": t0.Add(8 * time.Hour).Format(time.RFC3339), "schema": float64(1)}
	for k, v := range want {
		if obj[k] != v {
			t.Errorf("%s = %v, want %v", k, obj[k], v)
		}
	}
	if obj["title"] == "" || obj["title"] == nil {
		t.Error("title is empty")
	}
}

type amAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
}

func decodeAM(t *testing.T, b []byte) amAlert {
	t.Helper()
	var arr []amAlert
	if err := json.Unmarshal(b, &arr); err != nil || len(arr) != 1 {
		t.Fatalf("body is not a one-element Alertmanager v2 array: %v %s", err, b)
	}
	return arr[0]
}

// O-10: a still-observed condition is re-sent strictly before resolve_ttl,
// on a frozen clock, and a resolved one is sent once with endsAt <= now.
func TestWebhookAlertmanagerRefreshesBeforeResolveTTL(t *testing.T) {
	r := newReceiver(t, 200)
	d := alert.NewDispatcher(r.sink(t, alert.DialectAlertmanager), nil)
	ctx := context.Background()
	ttl := 10 * time.Minute

	d.Observe(ctx, stale, nil, t0)
	first := decodeAM(t, r.last(t).body)
	if first.Labels["alertname"] != "STALE_BACKUP" || first.Labels["scope"] != "nas/photos" {
		t.Fatalf("labels = %v, want alertname and the verbatim scope", first.Labels)
	}
	if _, bad := first.Labels["backup_set"]; bad {
		t.Fatal("the dialect carries labels.scope, not a backup_set label")
	}
	if first.Annotations["description"] != "no good backup for 3 days" || first.Annotations["summary"] == "" {
		t.Fatalf("annotations = %v", first.Annotations)
	}
	if !first.StartsAt.Equal(t0) || !first.EndsAt.Equal(t0.Add(ttl)) {
		t.Fatalf("startsAt %v endsAt %v, want %v and %v", first.StartsAt, first.EndsAt, t0, t0.Add(ttl))
	}

	// Advance a frozen clock a minute at a time. The second request must
	// arrive before the clock reaches ObservedAt + resolve_ttl.
	for now := t0.Add(time.Minute); r.count() < 2; now = now.Add(time.Minute) {
		if !now.Before(t0.Add(ttl)) {
			t.Fatalf("the clock reached resolve_ttl with %d request(s): the alert would self-resolve while the condition is still true", r.count())
		}
		d.Observe(ctx, stale, nil, now)
		d.Refresh(ctx, now)
	}
	refreshed := decodeAM(t, r.last(t).body)
	if !refreshed.StartsAt.Equal(t0) {
		t.Errorf("a refresh moved startsAt to %v, want the occurrence start %v", refreshed.StartsAt, t0)
	}
	if !refreshed.EndsAt.After(first.EndsAt) {
		t.Errorf("a refresh did not extend endsAt: %v then %v", first.EndsAt, refreshed.EndsAt)
	}

	// Resolve: exactly one more request, endsAt = now.
	before := r.count()
	resolvedAt := t0.Add(7 * time.Minute)
	d.Observe(ctx, nil, nil, resolvedAt)
	if r.count() != before+1 {
		t.Fatalf("resolve sent %d requests, want exactly 1", r.count()-before)
	}
	if got := decodeAM(t, r.last(t).body); got.EndsAt.After(resolvedAt) {
		t.Errorf("resolved endsAt = %v, want <= %v", got.EndsAt, resolvedAt)
	}
	d.Observe(ctx, nil, nil, resolvedAt.Add(time.Minute))
	d.Refresh(ctx, resolvedAt.Add(time.Hour))
	if r.count() != before+1 {
		t.Fatal("a resolved condition kept producing requests")
	}
}

func TestWebhookAlertmanagerRefreshIntervalMustBeBelowTTL(t *testing.T) {
	u := fixedURL("https://alertmanager.example.invalid/api/v2/alerts")
	for _, refresh := range []time.Duration{10 * time.Minute, 11 * time.Minute, 0} {
		if _, err := alert.NewHTTPSink(u, alert.DialectAlertmanager, 10*time.Minute, refresh, false); err == nil {
			t.Errorf("refresh %v against ttl 10m was accepted", refresh)
		}
	}
	if _, err := alert.NewHTTPSink(u, alert.DialectAlertmanager, 10*time.Minute, 4*time.Minute, false); err != nil {
		t.Errorf("4m against 10m was refused: %v", err)
	}
}

// O-12: what must be refused is refused, and a private target is not.
func TestWebhookURLGuards(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		insecure  bool
	}{
		{"file scheme", "file:///etc/passwd", true},
		{"ftp scheme", "ftp://hooks.example.invalid/x", true},
		{"userinfo", "https://user:pw@hooks.example.invalid/x", false},
		{"userinfo no password", "https://tok@hooks.example.invalid/x", false},
		{"plain http without ack", "http://hooks.example.invalid/x", false},
		{"no host", "https:///x", false},
		{"not a URL", "://", false},
	} {
		if err := config.CheckWebhookURL(tc.url, tc.insecure); err == nil {
			t.Errorf("%s: config.CheckWebhookURL accepted %q", tc.name, tc.url)
		} else if strings.Contains(err.Error(), "hooks.example.invalid") || strings.Contains(err.Error(), "pw@") {
			t.Errorf("%s: the refusal echoes the URL: %v", tc.name, err)
		}
		// The URL is resolved at each delivery, so the guard is judged there
		// too, and it must refuse before any request is made.
		sink, err := alert.NewHTTPSink(fixedURL(tc.url), alert.DialectGeneric, 0, 0, tc.insecure)
		if err != nil {
			t.Fatalf("%s: NewHTTPSink: %v", tc.name, err)
		}
		derr := sink.Deliver(context.Background(), alert.Alert{Kind: alert.StaleBackup, Scope: "s", ObservedAt: t0})
		if derr == nil {
			t.Errorf("%s: Deliver accepted %q", tc.name, tc.url)
		} else if strings.Contains(derr.Error(), "pw@") || strings.Contains(derr.Error(), "hooks.example.invalid") {
			t.Errorf("%s: the delivery error echoes the URL: %v", tc.name, derr)
		}
	}
	// The negative control that keeps the guard honest: the bundle's own
	// private target, with the acknowledgement, is allowed.
	for _, ok := range []string{"http://alertmanager:9093/api/v2/alerts", "http://169.254.169.254/x", "http://127.0.0.1:9093/", "https://hooks.example.invalid/x"} {
		if err := config.CheckWebhookURL(ok, true); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}

// O-12: a 3xx is a failure, is never followed, and its Location reaches no
// log line and no error.
func TestWebhookNeverFollowsRedirects(t *testing.T) {
	var metadataHits atomic.Int32
	metadata := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { metadataHits.Add(1) }))
	t.Cleanup(metadata.Close)
	location := metadata.URL + "/latest/meta-data/iam"
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		http.Redirect(w, q, location, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	sink, err := alert.NewHTTPSink(fixedURL(redirector.URL+"/hook"), alert.DialectGeneric, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	d := alert.NewDispatcher(sink, obs.New(&logs, obs.LevelDebug))
	delivered := d.Observe(context.Background(), stale, nil, t0)

	if len(delivered) != 0 {
		t.Fatal("a 302 counted as delivered")
	}
	if metadataHits.Load() != 0 {
		t.Fatal("the redirect target was contacted")
	}
	if strings.Contains(logs.String(), location) || strings.Contains(logs.String(), "meta-data") {
		t.Fatalf("the Location reached a log line: %s", logs.String())
	}
	if err := sink.Deliver(context.Background(), alert.Alert{Kind: alert.StaleBackup, Scope: "s", ObservedAt: t0}); err == nil ||
		!strings.Contains(err.Error(), "302") || strings.Contains(err.Error(), "meta-data") {
		t.Fatalf("error = %v, want one naming status 302 and not the Location", err)
	}
	if logs.Len() == 0 {
		t.Fatal("the failure was not logged at all, so the leak check above proves nothing")
	}
}

// O-12: TLS verification cannot be switched off, so a server whose
// certificate the system does not trust fails.
func TestWebhookTLSVerificationIsOn(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	sink, err := alert.NewHTTPSink(fixedURL(srv.URL+"/hook"), alert.DialectGeneric, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Deliver(context.Background(), alert.Alert{Kind: alert.StaleBackup, Scope: "s", ObservedAt: t0}); err == nil {
		t.Fatal("delivered to a server with an untrusted certificate")
	}
	if hits.Load() != 0 {
		t.Fatal("the request reached the handler")
	}
}

// The response is bounded: a receiver streaming without end cannot make the
// sink read it all.
func TestWebhookResponseIsBounded(t *testing.T) {
	var written atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		chunk := make([]byte, 32<<10)
		for q.Context().Err() == nil {
			n, err := w.Write(chunk)
			written.Add(int64(n))
			if err != nil || written.Load() > 64<<20 {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	sink, _ := alert.NewHTTPSink(fixedURL(srv.URL), alert.DialectGeneric, 0, 0, true)
	if err := sink.Deliver(context.Background(), alert.Alert{Kind: alert.StaleBackup, Scope: "s", ObservedAt: t0}); err != nil {
		t.Fatalf("a 200 with a huge body failed: %v", err)
	}
	if got := written.Load(); got > 8<<20 {
		t.Fatalf("the receiver wrote %d bytes before the sink stopped reading", got)
	}
}

// O-13: the URL is a credential. Neither an HTTP failure nor a refused
// connection puts it in an error or a log line; both name host and status.
func TestWebhookURLNeverLeaksIntoErrorsOrLogs(t *testing.T) {
	const secretPath = "/services/T0AAAA/B0BBBB/XXsecretsegmentXX"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	url := srv.URL + secretPath
	host := strings.TrimPrefix(srv.URL, "http://")

	sink, err := alert.NewHTTPSink(fixedURL(url), alert.DialectGeneric, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	d := alert.NewDispatcher(sink, obs.New(&logs, obs.LevelDebug))
	d.Observe(context.Background(), stale, nil, t0)
	status := sink.Deliver(context.Background(), alert.Alert{Kind: alert.StaleBackup, Scope: "s", ObservedAt: t0})
	srv.Close() // now the connection itself is refused
	refused := sink.Deliver(context.Background(), alert.Alert{Kind: alert.StaleBackup, Scope: "s", ObservedAt: t0})

	if status == nil || !strings.Contains(status.Error(), "500") || !strings.Contains(status.Error(), host) {
		t.Fatalf("status error = %v, want host and 500", status)
	}
	if refused == nil || !strings.Contains(refused.Error(), host) {
		t.Fatalf("refused error = %v, want the host", refused)
	}
	for name, text := range map[string]string{
		"status error": status.Error(), "refused error": refused.Error(), "logs": logs.String(),
		"%v": strings.Join([]string{sprint(sink), sprint(*sink)}, " "),
	} {
		if strings.Contains(text, "XXsecretsegmentXX") || strings.Contains(text, "T0AAAA") {
			t.Errorf("%s contains the webhook URL: %s", name, text)
		}
	}
	if !strings.Contains(logs.String(), "500") {
		t.Fatalf("the delivery failure was not logged, so the leak check proves nothing: %q", logs.String())
	}
}

// The reload seam's dispatcher half: a dispatcher over a new sink adopts
// what the previous one knew, so a delivered condition is not re-announced.
func TestDispatcherAdoptStateCarriesFiringAndDelivered(t *testing.T) {
	ctx := context.Background()
	oldSink, newSink := newReceiver(t, 200), newReceiver(t, 200)
	prev := alert.NewDispatcher(oldSink.sink(t, alert.DialectGeneric), nil)
	prev.Observe(ctx, stale, nil, t0)

	next := alert.NewDispatcher(newSink.sink(t, alert.DialectGeneric), nil)
	next.AdoptState(prev)
	if got := next.FiringSnapshot(); len(got) != 1 || !got[stale[0].Key()] {
		t.Fatalf("adopted firing map = %v, want the delivered condition", got)
	}
	next.Observe(ctx, stale, nil, t0.Add(time.Minute))
	if newSink.count() != 0 {
		t.Fatalf("the new channel re-announced a delivered condition (%d requests)", newSink.count())
	}
	fresh := append(stale, alert.Condition{Kind: alert.StaleBackup, Scope: "nas/docs", Detail: "x"})
	next.Observe(ctx, fresh, nil, t0.Add(2*time.Minute))
	if newSink.count() != 1 {
		t.Fatalf("a genuinely new condition sent %d requests, want 1", newSink.count())
	}
}

func sprint(v any) string { return fmt.Sprintf("%v %+v %#v", v, v, v) }

// Decision 4: the URL is resolved at every delivery attempt, so a rotated
// file is picked up by the next alert with no PATCH and no restart.
func TestWebhookResolvesTheURLAtEachDelivery(t *testing.T) {
	first, second := newReceiver(t, 200), newReceiver(t, 200)
	var mu sync.Mutex
	current := first.srv.URL + "/hook"
	sink, err := alert.NewHTTPSink(func(context.Context) (obs.Secret, error) {
		mu.Lock()
		defer mu.Unlock()
		return obs.NewSecret(current), nil
	}, alert.DialectGeneric, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	a := alert.Alert{Kind: alert.StaleBackup, Scope: "s", ObservedAt: t0}
	if err := sink.Deliver(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	current = second.srv.URL + "/hook"
	mu.Unlock()
	if err := sink.Deliver(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if first.count() != 1 || second.count() != 1 {
		t.Fatalf("deliveries went first=%d second=%d, want one each: a rotated URL must be used by the next alert", first.count(), second.count())
	}
}

// Decision 3: a URL that cannot be read is loud, keeps the firing map, is
// retried on the ladder, and heals itself.
func TestWebhookUnresolvableIsLoudKeepsTheMapAndHeals(t *testing.T) {
	r := newReceiver(t, 200)
	var mu sync.Mutex
	broken := true
	sink, err := alert.NewHTTPSink(func(context.Context) (obs.Secret, error) {
		mu.Lock()
		defer mu.Unlock()
		if broken {
			return obs.Secret{}, errors.New("the secret file does not exist")
		}
		return obs.NewSecret(r.srv.URL + "/hook"), nil
	}, alert.DialectGeneric, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	d := alert.NewDispatcher(sink, obs.New(&logs, obs.LevelDebug))
	ctx := context.Background()

	if got := d.DestinationStatus(); got != alert.URLStatusUnknown {
		t.Fatalf("status before anything was tried = %q, want %q", got, alert.URLStatusUnknown)
	}
	d.Observe(ctx, stale, nil, t0)
	if got := d.DestinationStatus(); got != alert.URLStatusUnresolvable {
		t.Fatalf("status = %q after a failed resolve, want %q", got, alert.URLStatusUnresolvable)
	}
	if !strings.Contains(logs.String(), "unresolvable") {
		t.Fatalf("a webhook that cannot be built said nothing in the log: %q", logs.String())
	}
	if snap := d.FiringSnapshot(); len(snap) != 1 || snap[stale[0].Key()] {
		t.Fatalf("firing map = %v, want the condition kept and undelivered", snap)
	}
	if r.count() != 0 {
		t.Fatal("a request was made without a URL")
	}

	// Still broken at the next pass inside the retry delay: stays loud, no request.
	d.Observe(ctx, stale, nil, t0.Add(time.Minute))
	if d.DestinationStatus() != alert.URLStatusUnresolvable || r.count() != 0 {
		t.Fatal("the state changed without a change in the reference")
	}

	// The activity feed hears about it once, on the change, not on every
	// pass: a line per poll would bury the one that matters.
	if got := destinationEvents(t, &logs); len(got) != 1 || got[0]["status"] != alert.URLStatusUnresolvable || got[0]["level"] != "ERROR" || got[0]["result"] != "error" {
		t.Fatalf("alert_destination events after two failing passes = %v, want one ERROR line saying unresolvable", got)
	}

	// A probe on its own (no condition) also reports, and heals.
	mu.Lock()
	broken = false
	mu.Unlock()
	d.Probe(ctx)
	if got := d.DestinationStatus(); got != alert.URLStatusOK {
		t.Fatalf("status after the reference became readable = %q, want %q", got, alert.URLStatusOK)
	}
	if got := destinationEvents(t, &logs); len(got) != 2 || got[1]["status"] != alert.URLStatusOK || got[1]["result"] != "success" {
		t.Fatalf("alert_destination events after the heal = %v, want a second line saying ok", got)
	}
	if strings.Contains(logs.String(), r.srv.URL) {
		t.Fatal("the URL reached the log")
	}

	// The held alert goes out on the first pass after the heal, not at the
	// next slot of a ladder that only grew because there was no URL.
	d.Observe(ctx, stale, nil, t0.Add(2*time.Minute))
	if r.count() != 1 {
		t.Fatalf("the first pass after the heal delivered %d requests, want 1", r.count())
	}
	if snap := d.FiringSnapshot(); !snap[stale[0].Key()] {
		t.Fatalf("firing map = %v, want the condition delivered after the heal", snap)
	}
	d.Probe(ctx)
	if got := destinationEvents(t, &logs); len(got) != 2 {
		t.Fatalf("a steady ok said something again: %v", got)
	}
}

// A first read that works is not a heal, so it says nothing.
func TestWebhookFirstGoodReadIsQuiet(t *testing.T) {
	r := newReceiver(t, 200)
	var logs bytes.Buffer
	d := alert.NewDispatcher(r.sink(t, alert.DialectGeneric), obs.New(&logs, obs.LevelDebug))
	d.Probe(context.Background())
	if d.DestinationStatus() != alert.URLStatusOK {
		t.Fatalf("status = %q, want ok", d.DestinationStatus())
	}
	if got := destinationEvents(t, &logs); len(got) != 0 {
		t.Fatalf("a destination that was never broken announced a heal: %v", got)
	}
}

// destinationEvents decodes every alert_destination line logged so far.
func destinationEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if m["event"] == obs.EventAlertDestination {
			out = append(out, m)
		}
	}
	return out
}
