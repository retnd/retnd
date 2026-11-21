package alert_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
)

// #1607: an alert that was resolved must not be re-fired by a refresh that was
// still queued (past the 16-send cap) or in flight when the pass resolved it.
// Alertmanager treats any later post for the alert as a new firing, and keeps
// it until the expiry runs out.
//
// The receiver here is a scripted RefreshingSink that records what it saw in
// the order the receiver saw it, and can hold refreshes at a gate the way a
// slow connect does. The dispatcher and the refresh loop (refreshLoop, in
// refresh_reload_1567_test.go) are the real ones, on the real clock.
//
// Falsify: drop the gone check in refreshOne, or the wait in refreshAndResolve.

type seen struct{ kind, scope string }

type gatedSink struct {
	every time.Duration
	gate  chan struct{} // refreshes block here until it is closed, when set

	started atomic.Int32
	begun   []string // scopes whose refresh has begun, under mu

	mu       sync.Mutex
	events   []seen
	attempts []time.Time   // when each refresh attempt began
	ended    []time.Time   // when each refresh attempt returned
	failFor  time.Duration // >0: every refresh fails after this long
}

func (s *gatedSink) note(kind, scope string) {
	s.mu.Lock()
	s.events = append(s.events, seen{kind, scope})
	s.mu.Unlock()
}

func (s *gatedSink) Deliver(_ context.Context, a alert.Alert) error {
	s.note("deliver", a.Scope)
	return nil
}

func (s *gatedSink) RefreshInterval() time.Duration { return s.every }

func (s *gatedSink) Refresh(ctx context.Context, a alert.Alert, _ time.Time) error {
	s.started.Add(1)
	s.mu.Lock()
	s.begun = append(s.begun, a.Scope)
	s.mu.Unlock()
	s.mu.Lock()
	s.attempts = append(s.attempts, time.Now())
	gate, failFor := s.gate, s.failFor
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if failFor > 0 {
		time.Sleep(failFor)
		s.mu.Lock()
		s.ended = append(s.ended, time.Now())
		s.mu.Unlock()
		return errors.New("receiver timed out")
	}
	s.note("refresh", a.Scope)
	return nil
}

func (s *gatedSink) Resolve(_ context.Context, a alert.Alert, _ time.Time) error {
	s.note("resolve", a.Scope)
	return nil
}

func (s *gatedSink) count(kind, scope string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.kind == kind && e.scope == scope {
			n++
		}
	}
	return n
}

// afterResolve lists every scope the receiver saw a refresh or a deliver for
// after it had seen that scope resolved.
func (s *gatedSink) afterResolve() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	resolved := map[string]bool{}
	var bad []string
	for _, e := range s.events {
		switch {
		case e.kind == "resolve":
			resolved[e.scope] = true
		case resolved[e.scope]:
			bad = append(bad, e.kind+" "+e.scope)
		}
	}
	return bad
}

func conds(n int) []alert.Condition {
	out := make([]alert.Condition, n)
	for i := range out {
		out[i] = alert.Condition{Kind: alert.StaleBackup, Scope: fmt.Sprintf("set-%02d", i), Detail: "stale"}
	}
	return out
}

func waitFor(t *testing.T, what string, within time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", within, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startLoop(t *testing.T, d *alert.Dispatcher) {
	t.Helper()
	var cur atomic.Pointer[alert.Dispatcher]
	cur.Store(d)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); refreshLoop(ctx, &cur) }()
	t.Cleanup(func() { cancel(); <-done })
}

// The reported sequence: 20 alerts fall due together, 16 refreshes are out
// (held at the gate, as on a slow connect) and 4 are queued behind the cap.
// A pass resolves all 20. Once the gate opens, no scope may see a refresh
// after its resolve.
func TestRefreshQueuedPastTheCapNeverFollowsAResolve_1607(t *testing.T) {
	sink := &gatedSink{every: 150 * time.Millisecond, gate: make(chan struct{})}
	d := alert.NewDispatcher(sink, nil)
	all := conds(20)
	if got := d.Observe(context.Background(), all, nil, time.Now()); len(got) != 20 {
		t.Fatalf("delivered %d alerts, want 20", len(got))
	}
	startLoop(t, d)
	waitFor(t, "16 refreshes held at the gate", 5*time.Second, func() bool { return sink.started.Load() >= 16 })

	// First resolve only the 4 that are queued: their resolve goes out now,
	// while the gate still holds, so the order is the same on every run
	// instead of depending on which scope the resolve loop reaches first.
	sink.mu.Lock()
	out := map[string]bool{}
	var still []alert.Condition
	for _, sc := range sink.begun {
		out[sc] = true
	}
	for _, c := range all {
		if out[c.Scope] {
			still = append(still, c)
		}
	}
	sink.mu.Unlock()
	if len(still) != 16 {
		t.Fatalf("%d refreshes were out, want 16", len(still))
	}
	d.Observe(context.Background(), still, nil, time.Now())
	for _, c := range all {
		if want := map[bool]int{true: 0, false: 1}[out[c.Scope]]; sink.count("resolve", c.Scope) != want {
			t.Fatalf("%s has %d resolves with the gate shut, want %d", c.Scope, sink.count("resolve", c.Scope), want)
		}
	}
	resolved := make(chan struct{})
	go func() {
		defer close(resolved)
		d.Observe(context.Background(), nil, nil, time.Now())
	}()
	time.Sleep(200 * time.Millisecond) // the in-flight ones' resolves wait on the gate
	close(sink.gate)
	select {
	case <-resolved:
	case <-time.After(10 * time.Second):
		t.Fatal("the resolving pass never returned")
	}
	time.Sleep(600 * time.Millisecond) // the loop drains what it had queued

	if bad := sink.afterResolve(); len(bad) > 0 {
		t.Errorf("the receiver saw %d sends after the alert was resolved, each one re-fires it: %v", len(bad), bad)
	}
	for _, c := range all {
		if n := sink.count("resolve", c.Scope); n != 1 {
			t.Errorf("%s was resolved %d times, want exactly once", c.Scope, n)
		}
	}
}

// One refresh already in flight when the pass resolves: the resolve waits for
// it, so the receiver sees refresh then resolve and the alert ends closed.
func TestResolveWaitsForTheRefreshInFlight_1607(t *testing.T) {
	sink := &gatedSink{every: 100 * time.Millisecond, gate: make(chan struct{})}
	d := alert.NewDispatcher(sink, nil)
	c := conds(1)
	d.Observe(context.Background(), c, nil, time.Now())
	startLoop(t, d)
	waitFor(t, "the refresh to be out", 5*time.Second, func() bool { return sink.started.Load() >= 1 })

	done := make(chan struct{})
	go func() { defer close(done); d.Observe(context.Background(), nil, nil, time.Now()) }()
	time.Sleep(150 * time.Millisecond)
	if n := sink.count("resolve", c[0].Scope); n != 0 {
		t.Errorf("the resolve went out while a refresh for the same alert was still in flight")
	}
	close(sink.gate)
	<-done
	time.Sleep(300 * time.Millisecond)
	if bad := sink.afterResolve(); len(bad) > 0 {
		t.Errorf("the receiver saw %v after the resolve", bad)
	}
	if n := sink.count("resolve", c[0].Scope); n != 1 {
		t.Errorf("resolved %d times, want once", n)
	}
}

// Resolving twice is harmless, and an alert that comes back after a resolve is
// a new occurrence: it is delivered again and refreshed again (the stale
// generation must not suppress it).
func TestResolveIsOnceAndARecurrenceRefreshesAgain_1607(t *testing.T) {
	sink := &gatedSink{every: 100 * time.Millisecond}
	d := alert.NewDispatcher(sink, nil)
	c := conds(1)
	scope := c[0].Scope
	d.Observe(context.Background(), c, nil, time.Now())
	d.Observe(context.Background(), nil, nil, time.Now())
	d.Observe(context.Background(), nil, nil, time.Now())
	if n := sink.count("resolve", scope); n != 1 {
		t.Fatalf("resolved %d times over two empty passes, want 1", n)
	}
	d.Observe(context.Background(), c, nil, time.Now())
	if n := sink.count("deliver", scope); n != 2 {
		t.Fatalf("the recurrence was delivered %d times in all, want 2", n)
	}
	startLoop(t, d)
	waitFor(t, "a refresh of the recurrence", 5*time.Second, func() bool { return sink.count("refresh", scope) >= 1 })
}

// A receiver that always fails is not hit back to back: the gap between one
// attempt and the next grows (to a cap) and is counted from when the last
// attempt ended, not from when it began.
func TestFailingReceiverIsRetriedWithGrowingGaps_1607(t *testing.T) {
	sink := &gatedSink{every: 400 * time.Millisecond, failFor: 120 * time.Millisecond}
	d := alert.NewDispatcher(sink, nil)
	d.Observe(context.Background(), conds(1), nil, time.Now())
	startLoop(t, d)
	waitFor(t, "five failed attempts", 30*time.Second, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.ended) >= 5
	})
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var gaps []time.Duration
	for i := 1; i < 5; i++ {
		gaps = append(gaps, sink.attempts[i].Sub(sink.ended[i-1]))
	}
	// First retry waits at least a quarter interval after the failure ended.
	if gaps[0] < 90*time.Millisecond {
		t.Errorf("first retry came %s after the failure, want at least a quarter interval (100ms): gaps %v", gaps[0], gaps)
	}
	// Then the gap grows until it reaches the interval.
	for i := 1; i < len(gaps); i++ {
		if gaps[i] < gaps[i-1]+40*time.Millisecond && gaps[i-1] < 380*time.Millisecond {
			t.Errorf("retry gaps %v do not grow (gap %d is not larger than gap %d)", gaps, i, i-1)
			break
		}
	}
}
