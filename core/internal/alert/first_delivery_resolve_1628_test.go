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

// #1628: a first Deliver that is still on its way when its alert resolves
// must be waited on, and the receiver must end up closed for it. Before this
// the pass only queued a resolve for an entry that was already delivered, so
// a first delivery landing after the pass left a firing alert nothing closed
// (it stayed until the receiver's own expiry).
//
// The receiver is a scripted RefreshingSink that records what it saw in the
// order it saw it and holds first deliveries at a gate, the way a slow
// connect does. The dispatcher is the real one, on the real clock.
//
// Falsify: drop the wait in refreshAndResolve, or drop the in-flight case in
// plan, or drop the gone check before a first send.

type firstSink struct {
	every   time.Duration
	gate    chan struct{} // first deliveries block here until it is closed
	failing bool          // first deliveries fail after the gate opens

	started atomic.Int32
	mu      sync.Mutex
	events  []seen
}

func (s *firstSink) note(kind, scope string) {
	s.mu.Lock()
	s.events = append(s.events, seen{kind, scope})
	s.mu.Unlock()
}

func (s *firstSink) Deliver(ctx context.Context, a alert.Alert) error {
	s.started.Add(1)
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.failing {
		return errors.New("receiver refused")
	}
	s.note("deliver", a.Scope)
	return nil
}

func (s *firstSink) RefreshInterval() time.Duration { return s.every }
func (s *firstSink) Refresh(_ context.Context, a alert.Alert, _ time.Time) error {
	s.note("refresh", a.Scope)
	return nil
}
func (s *firstSink) Resolve(_ context.Context, a alert.Alert, _ time.Time) error {
	s.note("resolve", a.Scope)
	return nil
}

func (s *firstSink) count(kind, scope string) int {
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

// stillFiring lists every scope whose last event at the receiver is a
// deliver or a refresh: the receiver holds it open.
func (s *firstSink) stillFiring() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := map[string]string{}
	var order []string
	for _, e := range s.events {
		if _, ok := last[e.scope]; !ok {
			order = append(order, e.scope)
		}
		last[e.scope] = e.kind
	}
	var open []string
	for _, sc := range order {
		if last[sc] != "resolve" {
			open = append(open, sc)
		}
	}
	return open
}

func (s *firstSink) order(scope string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.events {
		if e.scope == scope {
			out = append(out, e.kind)
		}
	}
	return out
}

// The reported sequence: a first Deliver is held at the receiver, the pass
// resolves the alert, the delivery lands. The resolve waits for it and goes
// out after it.
func TestResolveWaitsForTheFirstDeliveryInFlight_1628(t *testing.T) {
	sink := &firstSink{every: time.Minute, gate: make(chan struct{})}
	d := alert.NewDispatcher(sink, nil)
	c := conds(1)
	scope := c[0].Scope

	first := make(chan struct{})
	go func() { defer close(first); d.Observe(context.Background(), c, nil, time.Now()) }()
	waitFor(t, "the first delivery to be out", 5*time.Second, func() bool { return sink.started.Load() >= 1 })

	resolved := make(chan struct{})
	go func() { defer close(resolved); d.Observe(context.Background(), nil, nil, time.Now()) }()
	time.Sleep(200 * time.Millisecond)
	if n := sink.count("resolve", scope); n != 0 {
		t.Errorf("the resolve went out while the first delivery was still in flight")
	}
	close(sink.gate)
	for _, ch := range []chan struct{}{first, resolved} {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatal("a pass never returned")
		}
	}
	if got := sink.order(scope); len(got) != 2 || got[0] != "deliver" || got[1] != "resolve" {
		t.Errorf("the receiver saw %v, want [deliver resolve]", got)
	}
	if open := sink.stillFiring(); len(open) > 0 {
		t.Errorf("the receiver is left firing for %v", open)
	}
}

// Twenty new alerts, 16 first deliveries out (held) and 4 queued behind the
// cap, then a pass resolves all 20 and the gate opens. No scope may end the
// run firing at the receiver, and a scope that was delivered is closed once.
func TestFirstDeliveriesPastTheCapEndClosed_1628(t *testing.T) {
	sink := &firstSink{every: time.Minute, gate: make(chan struct{})}
	d := alert.NewDispatcher(sink, nil)
	all := conds(20)

	first := make(chan struct{})
	go func() { defer close(first); d.Observe(context.Background(), all, nil, time.Now()) }()
	waitFor(t, "16 first deliveries held", 5*time.Second, func() bool { return sink.started.Load() >= 16 })

	resolved := make(chan struct{})
	go func() { defer close(resolved); d.Observe(context.Background(), nil, nil, time.Now()) }()
	time.Sleep(200 * time.Millisecond)
	close(sink.gate)
	for _, ch := range []chan struct{}{first, resolved} {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatal("a pass never returned")
		}
	}
	if open := sink.stillFiring(); len(open) > 0 {
		t.Errorf("%d alerts were left firing at the receiver after they resolved: %v", len(open), open)
	}
	for _, c := range all {
		del, res := sink.count("deliver", c.Scope), sink.count("resolve", c.Scope)
		if del != res {
			t.Errorf("%s was delivered %d times and resolved %d times, want equal", c.Scope, del, res)
		}
		if res > 1 {
			t.Errorf("%s was resolved %d times, want at most once", c.Scope, res)
		}
	}
}

// A first delivery that fails is not closed: the receiver never saw it, so a
// resolve would close something it does not know. Guard, green on main too.
func TestResolveIsNotSentForAFirstDeliveryThatFailed_1628(t *testing.T) {
	sink := &firstSink{every: time.Minute, gate: make(chan struct{}), failing: true}
	d := alert.NewDispatcher(sink, nil)
	c := conds(1)

	first := make(chan struct{})
	go func() { defer close(first); d.Observe(context.Background(), c, nil, time.Now()) }()
	waitFor(t, "the first delivery to be out", 5*time.Second, func() bool { return sink.started.Load() >= 1 })
	resolved := make(chan struct{})
	go func() { defer close(resolved); d.Observe(context.Background(), nil, nil, time.Now()) }()
	time.Sleep(100 * time.Millisecond)
	close(sink.gate)
	<-first
	<-resolved
	if got := sink.order(c[0].Scope); len(got) != 0 {
		t.Errorf("the receiver saw %v for a delivery that failed, want nothing", got)
	}
}
