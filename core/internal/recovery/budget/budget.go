package budget

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/retnd/retnd/core/internal/config"
)

// Name identifies one budget. The names are stable: they appear in
// refusals, in the log and in the mapping to exit codes.
type Name string

const (
	// ConcurrentReads bounds full reads of retained copies running at once,
	// across every destination. An unbounded sweep is the I/O cost that
	// revalidate's MaxPerCycle already exists to bound.
	ConcurrentReads Name = "concurrent_reads"

	// DestinationReads bounds the same thing against one destination, so a
	// thousand copies on one slow bucket cannot take every slot.
	DestinationReads Name = "destination_reads"

	// StagingReserve is the free space below which a recovery or a test
	// refuses to start. A short file that looks complete is worse than a
	// refusal.
	StagingReserve Name = "staging_reserve"

	// DownloadStreams bounds browser downloads in progress, the concurrent
	// ceiling on the new byte-streaming machinery in the web process.
	DownloadStreams Name = "download_streams"
)

// All lists every budget, in the order they are documented.
var All = []Name{ConcurrentReads, DestinationReads, StagingReserve, DownloadStreams}

// exitCodes gives each budget the exit code a CLI refusal carries. They sit
// above the four codes the usage text publishes (0 to 3) and in the same
// 10 to 40 block `retnd recover` uses, and StagingReserve deliberately
// shares its number with recovery's below-reserve refusal because they are
// the same refusal. 34, 35 and 37 are not used: they belonged to three budgets
// (work per scheduler pass, captured validator output, scheduled egress) that
// nothing in the product read, and an exit code nothing emits is a promise
// nobody keeps.
var exitCodes = map[Name]int{
	StagingReserve:   18,
	ConcurrentReads:  32,
	DestinationReads: 33,
	DownloadStreams:  36,
}

// ExitCode is the exit code a refusal of budget n carries.
func ExitCode(n Name) int { return exitCodes[n] }

// ErrOverBudget is what every Refusal matches.
var ErrOverBudget = errors.New("budget: over budget")

// Refusal is what a budget says when it is asked to spend past its line.
type Refusal struct {
	Budget Name

	// Limit is the line, in the budget's own unit (reads, copies, bytes).
	Limit int64

	// Destination names the destination a per-destination budget was
	// refused for, and is empty for the rest.
	Destination string
}

func (r *Refusal) Error() string {
	switch r.Budget {
	case ConcurrentReads:
		return fmt.Sprintf("budget: %d full reads are already running, which is the most this host runs at once; try again when one finishes", r.Limit)
	case DestinationReads:
		return fmt.Sprintf("budget: %d full reads of %q are already running, which is the most one destination is asked for at once; try again when one finishes", r.Limit, r.Destination)
	case DownloadStreams:
		return fmt.Sprintf("budget: %d downloads are already in progress, which is the most this process streams at once; try again when one finishes", r.Limit)
	case StagingReserve:
		return fmt.Sprintf("budget: staging this would take the staging disk below its %d byte free-space reserve", r.Limit)
	}
	return fmt.Sprintf("budget: %s is over its limit of %d", r.Budget, r.Limit)
}

// Is makes every Refusal match ErrOverBudget.
func (r *Refusal) Is(target error) bool { return target == ErrOverBudget }

// Limits is every number in this package. Zero in a field means "take the
// default"; WithDefaults fills it.
type Limits struct {
	MaxConcurrentReads     int
	MaxReadsPerDestination int
	StagingReserveBytes    int64
	MaxDownloadStreams     int
}

// The defaults. The two read ceilings ARE the operator-facing defaults of
// assurance.max_concurrent_full_reads and assurance.max_per_destination:
// these names are those constants (config.DefaultMaxConcurrentReads and
// config.DefaultMaxPerDestination), not copies pinned equal by a test, so
// the settings card and the engine cannot say two different numbers. Each is
// sized for the smallest host this product runs on (a home NAS with a handful
// of spinning disks), and a host that has more raises them.
//
// The staging reserve and the download ceiling have no operator-facing
// setting, so the numbers are written here and only here.
const (
	DefaultMaxConcurrentReads     = config.DefaultMaxConcurrentReads
	DefaultMaxReadsPerDestination = config.DefaultMaxPerDestination
	DefaultStagingReserveBytes    = 1 << 30 // 1 GiB
	DefaultMaxDownloadStreams     = 4
)

// Defaults returns the default limits.
func Defaults() Limits {
	return Limits{
		MaxConcurrentReads:     DefaultMaxConcurrentReads,
		MaxReadsPerDestination: DefaultMaxReadsPerDestination,
		StagingReserveBytes:    DefaultStagingReserveBytes,
		MaxDownloadStreams:     DefaultMaxDownloadStreams,
	}
}

// WithDefaults returns l with every zero field replaced by its default. A
// negative field is left alone for Validate to refuse: a negative limit is a
// mistake, and quietly treating it as "default" would hide it.
func (l Limits) WithDefaults() Limits {
	d := Defaults()
	if l.MaxConcurrentReads == 0 {
		l.MaxConcurrentReads = d.MaxConcurrentReads
	}
	if l.MaxReadsPerDestination == 0 {
		l.MaxReadsPerDestination = d.MaxReadsPerDestination
	}
	if l.StagingReserveBytes == 0 {
		l.StagingReserveBytes = d.StagingReserveBytes
	}
	if l.MaxDownloadStreams == 0 {
		l.MaxDownloadStreams = d.MaxDownloadStreams
	}
	return l
}

// Validate refuses limits that cannot mean anything.
func (l Limits) Validate() error {
	switch {
	case l.MaxConcurrentReads < 1:
		return fmt.Errorf("budget: the concurrent full-read limit must be at least 1, got %d", l.MaxConcurrentReads)
	case l.MaxReadsPerDestination < 1:
		return fmt.Errorf("budget: the per-destination full-read limit must be at least 1, got %d", l.MaxReadsPerDestination)
	case l.MaxReadsPerDestination > l.MaxConcurrentReads:
		return fmt.Errorf("budget: the per-destination limit (%d) is above the global one (%d), so it could never bind", l.MaxReadsPerDestination, l.MaxConcurrentReads)
	case l.StagingReserveBytes < 0:
		return fmt.Errorf("budget: the staging reserve cannot be negative, got %d", l.StagingReserveBytes)
	case l.MaxDownloadStreams < 1:
		return fmt.Errorf("budget: the download ceiling must be at least 1, got %d", l.MaxDownloadStreams)
	}
	return nil
}

// Gate is a counting limiter. Acquire refuses when it is full, which is a
// refusal at the moment of spend: a caller that waited would be a queue with
// no bound of its own. AcquireWait is for the one kind of caller whose work is
// already bounded, a scheduler pass, where waiting is a queue of known size.
type Gate struct {
	name  Name
	limit int
	dest  string
	slots chan struct{}

	mu   sync.Mutex
	peak int
}

// NewGate returns a gate that admits limit holders at once.
func NewGate(name Name, limit int) *Gate {
	if limit < 1 {
		limit = 1
	}
	return &Gate{name: name, limit: limit, slots: make(chan struct{}, limit)}
}

func (g *Gate) admitted() func() {
	g.mu.Lock()
	if n := len(g.slots); n > g.peak {
		g.peak = n
	}
	g.mu.Unlock()

	var once sync.Once
	return func() { once.Do(func() { <-g.slots }) }
}

// Acquire takes a slot or refuses. The release function is safe to call
// more than once.
func (g *Gate) Acquire() (release func(), err error) {
	select {
	case g.slots <- struct{}{}:
		return g.admitted(), nil
	default:
		return nil, &Refusal{Budget: g.name, Limit: int64(g.limit), Destination: g.dest}
	}
}

// AcquireWait takes a slot, waiting for one to free, or returns the context's
// error. A waiter that gives up holds nothing.
func (g *Gate) AcquireWait(ctx context.Context) (release func(), err error) {
	select {
	case g.slots <- struct{}{}:
		return g.admitted(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// InFlight is how many slots are held now.
func (g *Gate) InFlight() int { return len(g.slots) }

// Peak is the most slots that were ever held at once.
func (g *Gate) Peak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peak
}

// Reads bounds full reads, globally and per destination.
type Reads struct {
	global  *Gate
	perDest int

	mu   sync.Mutex
	dest map[string]*Gate
}

// NewReads returns a limiter for l, which should already be validated.
func NewReads(l Limits) *Reads {
	return &Reads{
		global:  NewGate(ConcurrentReads, l.MaxConcurrentReads),
		perDest: l.MaxReadsPerDestination,
		dest:    map[string]*Gate{},
	}
}

// Acquire admits one full read of a copy on destination, or refuses naming
// the budget that is full. The per-destination check runs first: a refusal
// there is the more specific thing to tell the caller, and it leaves the
// global slot for a read of somewhere else.
func (r *Reads) Acquire(destination string) (release func(), err error) {
	dg := r.destGate(destination)
	releaseDest, err := dg.Acquire()
	if err != nil {
		return nil, err
	}
	releaseGlobal, err := r.global.Acquire()
	if err != nil {
		releaseDest()
		return nil, err
	}
	return func() {
		releaseGlobal()
		releaseDest()
	}, nil
}

// AcquireWait is Acquire for a caller that waits instead of being refused:
// the per-destination slot, then the global one, each by waiting. Both orders
// of holding are the same for every caller, so two waiters cannot each hold
// what the other needs.
func (r *Reads) AcquireWait(ctx context.Context, destination string) (release func(), err error) {
	dg := r.destGate(destination)
	releaseDest, err := dg.AcquireWait(ctx)
	if err != nil {
		return nil, err
	}
	releaseGlobal, err := r.global.AcquireWait(ctx)
	if err != nil {
		releaseDest()
		return nil, err
	}
	return func() {
		releaseGlobal()
		releaseDest()
	}, nil
}

func (r *Reads) destGate(destination string) *Gate {
	r.mu.Lock()
	defer r.mu.Unlock()
	dg, ok := r.dest[destination]
	if !ok {
		dg = NewGate(DestinationReads, r.perDest)
		dg.dest = destination
		r.dest[destination] = dg
	}
	return dg
}

// Peaks reports the most full reads that ever ran at once, overall and for
// each destination. It is how a measurement shows the ceiling held.
func (r *Reads) Peaks() (global int, perDestination map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	perDestination = make(map[string]int, len(r.dest))
	for d, g := range r.dest {
		perDestination[d] = g.Peak()
	}
	return r.global.Peak(), perDestination
}
