package revalidate

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/state"
)

// This file is the driver's bounds: how much of the deployment a scrub is
// allowed to be doing at once, how it spreads out, and how it backs off.
//
// Money and bandwidth are refusals, not warnings. Each bound here is a named
// constant with a default, enforced where the work starts, and a test drives
// each one over its line. The operator set a set's cadence when everything
// was free, so a thousand sets that all became due at one instant must not
// be a thousand simultaneous full reads.

const (
	// DefaultGlobalConcurrency is how many copies one process scrubs at the
	// same moment, across every backup set. A scrub that re-hashes local
	// disk competes with the backups it exists to protect, so the default is
	// small. It is the settings' own number
	// (assurance.max_concurrent_full_reads), because a scrub read is a full
	// read and the card names one ceiling for all of them.
	DefaultGlobalConcurrency = config.DefaultMaxConcurrentReads

	// DefaultPerDestinationConcurrency is how many of those may be reading
	// the same destination (the local disk, or one storage medium). One
	// medium is one set of credentials, one rate limit and one egress meter,
	// and hammering it with every slot is how a scrub earns a provider's
	// throttling. It is assurance.max_per_destination's default.
	DefaultPerDestinationConcurrency = config.DefaultMaxPerDestination

	// DefaultJitterFraction spreads due times: a pass that finishes at T
	// schedules the next at T + interval + up to this fraction of the
	// interval, so a thousand copies checked together are not all due
	// together again.
	DefaultJitterFraction = state.ScrubJitterFraction

	// DefaultBackoffBase and DefaultBackoffCap bound the retry of a copy
	// that could not be checked or failed: base, then double each time the
	// streak grows, capped. The cap is also clamped to the set's own
	// interval, because a backoff longer than the cadence would make a
	// struggling copy be checked LESS often than a healthy one.
	DefaultBackoffBase = 5 * time.Minute
	DefaultBackoffCap  = 6 * time.Hour

	// DefaultLeaseTTL is how long one scrub of one copy may hold its lease.
	// It is long, because a content scrub of a large local file is long,
	// and it is only the ceiling on how long a crashed holder makes its
	// copy unscrubbable.
	DefaultLeaseTTL = 2 * time.Hour
)

// Bounds is the full set of knobs a driver runs under. The zero value means
// "the defaults above", so a caller that sets nothing gets the bounded
// behaviour and never an unbounded one.
type Bounds struct {
	GlobalConcurrency         int
	PerDestinationConcurrency int
	JitterFraction            float64
	BackoffBase, BackoffCap   time.Duration
	LeaseTTL                  time.Duration

	// Rand supplies the jitter, in [0, 1). Nil means math/rand/v2. It is a
	// seam so a test can assert the spread rather than hope for it.
	Rand func() float64
}

// withDefaults fills every zero field. A negative value is a configuration
// mistake and is replaced rather than honoured: a negative concurrency would
// otherwise deadlock the limiter, and a negative jitter would schedule a
// copy to be due in the past.
func (b Bounds) withDefaults() Bounds {
	if b.GlobalConcurrency <= 0 {
		b.GlobalConcurrency = DefaultGlobalConcurrency
	}
	if b.PerDestinationConcurrency <= 0 {
		b.PerDestinationConcurrency = DefaultPerDestinationConcurrency
	}
	if b.JitterFraction <= 0 {
		b.JitterFraction = DefaultJitterFraction
	}
	if b.BackoffBase <= 0 {
		b.BackoffBase = DefaultBackoffBase
	}
	if b.BackoffCap <= 0 {
		b.BackoffCap = DefaultBackoffCap
	}
	if b.LeaseTTL <= 0 {
		b.LeaseTTL = DefaultLeaseTTL
	}
	if b.Rand == nil {
		b.Rand = rand.Float64
	}
	return b
}

// jitter returns a duration in [0, JitterFraction*interval).
func (b Bounds) jitter(interval time.Duration) time.Duration {
	r := b.Rand()
	if r < 0 {
		r = 0
	}
	if r >= 1 {
		r = 0.999999
	}
	return time.Duration(r * b.JitterFraction * float64(interval))
}

// NextDueAfterCheck is when a copy whose check just ran to a verdict (a pass,
// an archived copy, a pass that checked nothing) is next owed one: one
// interval on, plus jitter.
func (b Bounds) NextDueAfterCheck(now time.Time, interval time.Duration) time.Time {
	b = b.withDefaults()
	return now.Add(interval + b.jitter(interval))
}

// NextDueAfterFailure is when a copy that failed, or could not be checked,
// is retried. streak is the number of failures in a row INCLUDING this one.
//
// The delay doubles from BackoffBase with the streak and is capped at the
// smaller of BackoffCap and the set's interval, with jitter on top. It never
// exceeds the interval, so a failing copy is looked at at least as often as
// a healthy one, which is the property that makes a transient blip recover
// quickly and a persistent failure keep being re-examined.
func (b Bounds) NextDueAfterFailure(now time.Time, interval time.Duration, streak int) time.Time {
	b = b.withDefaults()
	ceiling := b.BackoffCap
	if interval > 0 && interval < ceiling {
		ceiling = interval
	}
	delay := b.BackoffBase
	for i := 1; i < streak && delay < ceiling; i++ {
		delay *= 2
	}
	if delay > ceiling {
		delay = ceiling
	}
	return now.Add(delay + b.jitter(delay))
}

// Limits is the shared admission control for scrub reads: one global cap and
// one cap per destination. A single *Limits is shared by every Run in a
// process (every backup set, the scheduler tick and a manual scrub in the
// same process), which is what makes the cap global.
//
// It is a counting semaphore and not a worker pool because the units of work
// are already goroutines owned by the caller, and admission is the only
// thing this has to decide.
type Limits struct {
	global chan struct{}
	perDst int

	mu   sync.Mutex
	dest map[string]chan struct{}
}

// NewLimits builds the limiter from b (defaults filled in).
func NewLimits(b Bounds) *Limits {
	b = b.withDefaults()
	return &Limits{
		global: make(chan struct{}, b.GlobalConcurrency),
		perDst: b.PerDestinationConcurrency,
		dest:   map[string]chan struct{}{},
	}
}

func (l *Limits) destSem(dst string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.dest[dst]
	if !ok {
		s = make(chan struct{}, l.perDst)
		l.dest[dst] = s
	}
	return s
}

// Acquire blocks until a global slot and a slot on dst are both free, or ctx
// ends. The per-destination slot is taken first so that a pile of work
// waiting on one slow medium does not hold global slots that another medium
// could be using: a waiter holds the scarcer, narrower resource first and
// the shared one only once it can actually run.
func (l *Limits) Acquire(ctx context.Context, dst string) (release func(), err error) {
	if l == nil {
		return func() {}, nil
	}
	d := l.destSem(dst)
	select {
	case d <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("revalidate: waiting for a slot on %q: %w", dst, ctx.Err())
	}
	select {
	case l.global <- struct{}{}:
	case <-ctx.Done():
		<-d
		return nil, fmt.Errorf("revalidate: waiting for a scrub slot: %w", ctx.Err())
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-l.global
			<-d
		})
	}, nil
}
