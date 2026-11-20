package alert

import (
	"context"
	"fmt"
	"time"
)

// RefreshingSink is a Sink whose receiver forgets an alert unless it is
// re-sent. Alertmanager is the case: it expires an alert after its own
// resolve timeout, so a single push of a condition that is still true turns
// into a false all-clear a few minutes later.
//
// The Dispatcher stays the only thing that decides what is alert-worthy.
// This interface adds no decision: while a condition it already delivered
// is still observed it asks the sink to say so again every
// RefreshInterval, and when the condition stops being observed it asks the
// sink to close the alert once. A plain Sink never sees either call, so
// generic and platform delivery stay observe-once.
type RefreshingSink interface {
	Sink

	// RefreshInterval is how often a still-observed, already-delivered
	// condition is re-sent. It is strictly shorter than the receiver's
	// expiry, which config validation enforces.
	RefreshInterval() time.Duration

	// Refresh re-sends a still-observed condition. a.ObservedAt is when
	// the occurrence started; now is the clock the new expiry counts from.
	Refresh(ctx context.Context, a Alert, now time.Time) error

	// Resolve closes the alert, effective now.
	Resolve(ctx context.Context, a Alert, now time.Time) error
}

func (d *Dispatcher) refreshing() RefreshingSink {
	if d == nil {
		return nil
	}
	// An interval of zero (or less) means the sink does not refresh. It must
	// never read as "due on every pass".
	if rs, ok := d.sink.(RefreshingSink); ok && rs.RefreshInterval() > 0 {
		return rs
	}
	return nil
}

// RefreshEvery is how often this dispatcher's sink wants a still-observed
// condition re-sent, or zero when its sink is observe-once.
func (d *Dispatcher) RefreshEvery() time.Duration {
	if rs := d.refreshing(); rs != nil {
		return rs.RefreshInterval()
	}
	return 0
}

func (d *Dispatcher) refreshes() bool { return d.refreshing() != nil }

// NextRefresh is how long a refresh loop may sleep from now: until the
// earliest delivered condition is due its re-send, and never longer than one
// refresh interval, since a condition delivered while the loop sleeps is due
// no sooner than that. It is zero for a dispatcher that does not refresh.
//
// The loop has to wake when a re-send is due, not on a tick near it. A tick
// of a quarter interval (what this replaced) re-sent up to a quarter
// interval late, and with resolve_ttl close above refresh_interval the
// receiver expired the alert in that gap and fired it again: the false
// all-clear the refresh exists to prevent (#1518's review).
func (d *Dispatcher) NextRefresh(now time.Time) time.Duration {
	rs := d.refreshing()
	if rs == nil {
		return 0
	}
	every := rs.RefreshInterval()
	wait := every
	d.mu.Lock()
	for _, entry := range d.firing {
		if !entry.delivered {
			continue
		}
		if w := entry.sentAt.Add(every).Sub(now); w < wait {
			wait = w
		}
	}
	d.mu.Unlock()
	return max(wait, 0)
}

// refreshDue reports whether a delivered entry is due its refresh. It is
// called with d.mu held. The sentAt stamp moves forward as soon as the
// attempt is planned, so two concurrent passes cannot both re-send it; a
// failed attempt rolls it back (refreshOne) so the next pass tries again.
func (d *Dispatcher) refreshDue(entry *firingCondition, now time.Time) (attempt, bool) {
	rs := d.refreshing()
	if rs == nil || now.Sub(entry.sentAt) < rs.RefreshInterval() {
		return attempt{}, false
	}
	entry.sentAt = now
	return attempt{entry: entry, alert: entry.alert, refresh: true}, true
}

// refreshOne makes one bounded refresh attempt. A failure is logged and
// the entry stays delivered: the first delivery is what the operator was
// told about, and the refresh is tried again a quarter interval later (the
// stamp is moved back so the entry falls due then, which is when a loop
// sleeping on NextRefresh wakes for it).
func (d *Dispatcher) refreshOne(ctx context.Context, a attempt, now time.Time) {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	err := d.refreshing().Refresh(ctx, a.alert, now)
	d.noteDestination(ctx, err)
	if err != nil {
		d.logger.Error(ctx, "alert-delivery",
			fmt.Errorf("refreshing the %s alert for %s: %w", a.alert.Kind, a.alert.Scope, err))
		every := d.refreshing().RefreshInterval()
		d.mu.Lock()
		a.entry.sentAt = now.Add(every/4 - every)
		d.mu.Unlock()
	}
}

// refreshAndResolve closes every alert a pass found resolved. It is best
// effort by design: the receiver expires the alert on its own at endsAt,
// so a lost resolve costs a few minutes, never a false alarm.
func (d *Dispatcher) refreshAndResolve(ctx context.Context, now time.Time, resolved []Alert) {
	rs := d.refreshing()
	if rs == nil {
		return
	}
	for _, a := range resolved {
		cctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
		err := rs.Resolve(cctx, a, now)
		d.noteDestination(cctx, err)
		if err != nil {
			d.logger.Error(cctx, "alert-delivery",
				fmt.Errorf("resolving the %s alert for %s: %w", a.Kind, a.Scope, err))
		}
		cancel()
	}
}

// Refresh re-sends every delivered condition that is due, off the firing
// map the last Observe left. It exists because Observe runs at the poll
// interval, which is usually far longer than a refresh interval, and a
// condition that is not re-sent in time is exactly the false all-clear.
// It decides nothing new: a condition that has resolved since the last
// Observe is refreshed until the next one closes it, which errs towards
// telling the receiver the alert is still true.
func (d *Dispatcher) Refresh(ctx context.Context, now time.Time) {
	if d == nil || !d.refreshes() {
		return
	}
	d.mu.Lock()
	var due []attempt
	for _, entry := range d.firing {
		if !entry.delivered {
			continue
		}
		if a, ok := d.refreshDue(entry, now); ok {
			due = append(due, a)
		}
	}
	d.mu.Unlock()
	for _, a := range due {
		d.refreshOne(ctx, a, now)
	}
}

// AdoptState carries prev's de-duplication state (which conditions are
// firing, which were delivered, and when) onto d, which may deliver through
// a different sink. It is the reload seam for a changed delivery channel:
// the one new sink gets a dispatcher that already knows what the operator
// has been told, so a still-true condition is not announced again just
// because the channel changed. d must not yet be in use.
func (d *Dispatcher) AdoptState(prev *Dispatcher) {
	if d == nil || prev == nil || d == prev {
		return
	}
	prev.mu.Lock()
	copied := make(map[string]*firingCondition, len(prev.firing))
	for k, v := range prev.firing {
		c := *v
		copied[k] = &c
	}
	// The destination state travels too, so a reference that stays broken
	// across a reload is not announced a second time, and one that the
	// reload fixed is recognised as a heal (which lifts the retry ladder).
	destination := prev.destination
	prev.mu.Unlock()
	d.mu.Lock()
	d.firing = copied
	d.destination = destination
	d.mu.Unlock()
}

// FiringSnapshot reports each firing condition's Subject key and whether
// it was delivered, for a test that has to see the map survive a reload.
func (d *Dispatcher) FiringSnapshot() map[string]bool {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]bool, len(d.firing))
	for k, v := range d.firing {
		out[k] = v.delivered
	}
	return out
}

// URLStatusSink is a Sink that can say whether the destination it delivers
// to can be read at all, so an unreadable one is a visible state and not a
// silence. HTTPSink is the only one.
type URLStatusSink interface {
	Sink
	Probe(ctx context.Context) error
	URLStatus() string
}

// Probe asks the sink to check its destination without sending anything, so
// the status is true between alerts as well as at them. A change of state
// reaches the activity feed through noteDestination.
func (d *Dispatcher) Probe(ctx context.Context) {
	if d == nil {
		return
	}
	if s, ok := d.sink.(URLStatusSink); ok {
		d.noteDestination(ctx, s.Probe(ctx))
	}
}

// noteDestination compares what the sink now says about its destination
// with what this dispatcher last reported, and speaks only on a change: one
// EventAlertDestination line when the destination stops being usable, and
// one when it heals. err is the call that just ran; it is the reason given
// for a bad state and is ignored otherwise (a receiver answering 500 is a
// delivery failure, logged by deliver, not a destination that cannot be
// read).
//
// A heal also lifts the retry ladder off every condition still waiting to
// be delivered. The ladder exists to stop hammering a receiver that is down,
// and these attempts never reached one: holding the alert for up to an hour
// after the reference reads again would be a second silence on top of the
// first. The firing map is otherwise untouched, so nothing is re-announced.
func (d *Dispatcher) noteDestination(ctx context.Context, err error) {
	s, ok := d.sink.(URLStatusSink)
	if !ok {
		return
	}
	status := s.URLStatus()
	if status == URLStatusUnknown {
		return
	}
	d.mu.Lock()
	prev := d.destination
	if status == prev {
		d.mu.Unlock()
		return
	}
	d.destination = status
	healed := status == URLStatusOK && (prev == URLStatusUnresolvable || prev == URLStatusInvalid)
	if healed {
		for _, entry := range d.firing {
			if !entry.delivered {
				entry.attempts = 0
				entry.lastAttempt = time.Time{}
			}
		}
	}
	d.mu.Unlock()

	switch {
	case healed:
		d.logger.AlertDestination(ctx, URLStatusOK, "")
	case status != URLStatusOK:
		detail := status
		if err != nil {
			detail = err.Error()
		}
		d.logger.AlertDestination(ctx, status, detail)
	}
}

// DestinationStatus is what the last read of the sink's destination found, or the
// empty string when the sink has no such notion (the platform sink).
func (d *Dispatcher) DestinationStatus() string {
	if d == nil {
		return ""
	}
	if s, ok := d.sink.(URLStatusSink); ok {
		return s.URLStatus()
	}
	return ""
}
