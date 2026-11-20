package alert_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
)

// #1567, the second case: a configuration reload while a refresh is failing.
// The refresh used to move the entry's sent stamp forward as soon as it was
// planned and roll it back only if the attempt failed. A reload in between
// copied the forward stamp, so the rebuilt dispatcher believed the alert had
// just been re-sent and waited a whole interval, and with resolve_ttl under
// twice the interval the alert expired in Alertmanager while still true.
//
// The real clock and a refresh loop shaped like service.runAlertRefreshes
// (wake on NextRefresh, at least every second, read the current dispatcher on
// every turn, the way a reload swaps it under the loop). The receiver is a
// scripted RefreshingSink rather than an HTTP server because config validation
// and NewHTTPSink both hold an HTTP sink to resolve_ttl >= refresh + 30s, and
// this case needs resolve_ttl under twice the refresh interval: at that scale
// the test would run for a minute and a half. The dispatcher, the part that
// was wrong, is the real one.
//
// Falsify: let AdoptState copy an in-flight refresh's planned stamp again.

// scriptedSink is a RefreshingSink that records the endsAt of every send it
// accepted and can fail refreshes slowly, as a receiver that is timing out
// does.
type scriptedSink struct {
	every, ttl time.Duration
	failAfter  time.Duration // >0: every refresh fails after this long
	started    chan struct{} // receives once per refresh attempt, if set

	mu       sync.Mutex
	accepted []time.Time // the endsAt of every send this sink accepted
	refresh  []time.Time // when each accepted refresh arrived
}

func (s *scriptedSink) Deliver(_ context.Context, a alert.Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accepted = append(s.accepted, a.ObservedAt.Add(s.ttl))
	return nil
}

func (s *scriptedSink) RefreshInterval() time.Duration { return s.every }

func (s *scriptedSink) Refresh(ctx context.Context, _ alert.Alert, now time.Time) error {
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	if s.failAfter > 0 {
		select {
		case <-time.After(s.failAfter):
		case <-ctx.Done():
		}
		return errors.New("receiver timed out")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accepted = append(s.accepted, now.Add(s.ttl))
	s.refresh = append(s.refresh, time.Now())
	return nil
}

func (s *scriptedSink) Resolve(context.Context, alert.Alert, time.Time) error { return nil }

func (s *scriptedSink) firstRefresh() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.refresh) == 0 {
		return time.Time{}, false
	}
	return s.refresh[0], true
}

func (s *scriptedSink) lastEndsAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted[len(s.accepted)-1]
}

// refreshLoop is service.runAlertRefreshes over a dispatcher a reload can
// swap: it reads cur on every turn.
func refreshLoop(ctx context.Context, cur *atomic.Pointer[alert.Dispatcher]) {
	for {
		wait := min(max(cur.Load().NextRefresh(time.Now()), 10*time.Millisecond), time.Second)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		cur.Load().Refresh(ctx, time.Now())
	}
}

func TestDispatcherReloadDuringAFailingRefreshKeepsTheLastSentTime_1567(t *testing.T) {
	const (
		every     = 2 * time.Second
		ttl       = 3200 * time.Millisecond // under twice the interval
		failAfter = 800 * time.Millisecond
	)
	oldSink := &scriptedSink{every: every, ttl: ttl, failAfter: failAfter, started: make(chan struct{}, 1)}
	newSink := &scriptedSink{every: every, ttl: ttl}

	old := alert.NewDispatcher(oldSink, nil)
	start := time.Now()
	if got := old.Observe(context.Background(), stale, nil, start); len(got) != 1 {
		t.Fatalf("the first pass delivered %d alerts, want 1", len(got))
	}
	expires := oldSink.lastEndsAt() // what Alertmanager holds until a refresh lands
	if !expires.Equal(start.Add(ttl)) {
		t.Fatalf("the first delivery's endsAt is %s after the pass, want %s", expires.Sub(start), ttl)
	}

	var cur atomic.Pointer[alert.Dispatcher]
	cur.Store(old)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); refreshLoop(ctx, &cur) }()

	// The first refresh is due at start+every and fails slowly. The reload
	// lands in the middle of it, the way hot reload builds a dispatcher over
	// the new sink and hands it what the old one knew.
	select {
	case <-oldSink.started:
	case <-time.After(every + 2*time.Second):
		t.Fatal("the old dispatcher never attempted its refresh")
	}
	time.Sleep(failAfter / 4)
	next := alert.NewDispatcher(newSink, nil)
	next.AdoptState(old)
	cur.Store(next)

	time.Sleep(time.Until(start.Add(2*every + every/4)))
	cancel()
	<-loopDone

	landed, ok := newSink.firstRefresh()
	if !ok {
		t.Fatalf("the rebuilt dispatcher sent no refresh in %s; the alert expired %s after the first delivery", 2*every+every/4, ttl)
	}
	if !landed.Before(expires) {
		t.Errorf("the rebuilt dispatcher's first refresh landed %s after the first delivery, %s after the alert expired at %s: it took the failing attempt's stamp for a send",
			landed.Sub(start).Round(10*time.Millisecond), landed.Sub(expires).Round(10*time.Millisecond), ttl)
	}
}
