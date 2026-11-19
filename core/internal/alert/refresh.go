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
	rs, _ := d.sink.(RefreshingSink)
	return rs
}

func (d *Dispatcher) refreshes() bool { return d.refreshing() != nil }

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
// told about, and the refresh is simply retried on the next pass.
func (d *Dispatcher) refreshOne(ctx context.Context, a attempt, now time.Time) {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	if err := d.refreshing().Refresh(ctx, a.alert, now); err != nil {
		d.logger.Error(ctx, "alert-delivery",
			fmt.Errorf("refreshing the %s alert for %s: %w", a.alert.Kind, a.alert.Scope, err))
		d.mu.Lock()
		a.entry.sentAt = time.Time{}
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
		if err := rs.Resolve(cctx, a, now); err != nil {
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
	prev.mu.Unlock()
	d.mu.Lock()
	d.firing = copied
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
