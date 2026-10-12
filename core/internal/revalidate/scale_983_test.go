package revalidate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// EPIC Q, #983: a thousand copies all due at the same instant, on the real
// scrub driver.
//
// The day this describes is a rebuilt catalog, or a first upgrade, or a
// scheduler switched on for a fleet that never had one. What has to hold is
// two things at once. The pass must stay inside DefaultScrubMaxPerCycle and
// DEFER the rest to later passes (a refusal would mean the 51st copy is never
// scrubbed). And the reads that pass makes must never exceed the host's two
// ceilings, globally or against any one destination.
//
// Everything here is the product's own code: the journal is a real SQLite
// file, the selection is SelectDue, the workers and the limiter are Run's, and
// the per-pass number comes from config.Assurance.Revalidation, which is what
// the serve scheduler asks. The only double is the storage medium, and its job
// is to take a little time per call and to count how many calls are in flight
// at once, which is the one thing the driver's own code cannot tell me about
// itself.
//
// SCRUB_SCALE_COPIES overrides the count and SCRUB_SCALE_OUT writes the
// measurement as JSON for docs/perf/epic-q-recovery.md.

const scaleDestinations = 3

// peakStore counts the checks in flight against it, in total and per medium.
type peakStore struct {
	delay time.Duration

	mu          sync.Mutex
	inFlight    int
	peak        int
	perInFlight map[string]int
	perPeak     map[string]int
	calls       int
}

func newPeakStore(delay time.Duration) *peakStore {
	return &peakStore{delay: delay, perInFlight: map[string]int{}, perPeak: map[string]int{}}
}

func (s *peakStore) StatObject(ctx context.Context, m transport.Medium, key string) (transport.ObjectInfo, error) {
	s.mu.Lock()
	s.calls++
	s.inFlight++
	s.perInFlight[m.ID]++
	if s.inFlight > s.peak {
		s.peak = s.inFlight
	}
	if s.perInFlight[m.ID] > s.perPeak[m.ID] {
		s.perPeak[m.ID] = s.perInFlight[m.ID]
	}
	s.mu.Unlock()

	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
	}

	s.mu.Lock()
	s.inFlight--
	s.perInFlight[m.ID]--
	s.mu.Unlock()
	return transport.ObjectInfo{Key: key, Size: scaleSize}, nil
}

func (s *peakStore) OpenObject(context.Context, transport.Medium, string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("an unattended pass never downloads a medium copy")
}

func (s *peakStore) ObjectChecksum(context.Context, transport.Medium, string, transport.HashAlgorithm) (transport.ChecksumAttestation, error) {
	return transport.ChecksumAttestation{}, transport.NewError(transport.UnsupportedCapability, "object_checksum", fmt.Errorf("not attested"))
}

const scaleSize = 5

type threeMediums struct{}

func (threeMediums) MediumFor(id string) (transport.Medium, bool) {
	for i := 0; i < scaleDestinations; i++ {
		if id == scaleMedium(i) {
			return transport.Medium{ID: id, Type: transport.MediumTypeS3, Bucket: "b-" + id}, true
		}
	}
	return transport.Medium{}, false
}

func scaleMedium(i int) string { return "offsite-" + strconv.Itoa(i%scaleDestinations) }

// scaleRun is what one run of the whole backlog measured.
type scaleRun struct {
	Copies          int      `json:"copies"`
	PerPassBound    int      `json:"per_pass_bound"`
	Passes          int      `json:"passes"`
	SelectedPerPass []int    `json:"selected_per_pass"`
	DueAfterPass1   int      `json:"due_after_pass_1"`
	Checked         int      `json:"checked"`
	GlobalCeiling   int      `json:"global_read_ceiling"`
	DestCeiling     int      `json:"per_destination_read_ceiling"`
	PeakGlobal      int      `json:"peak_reads_in_flight"`
	PeakPerDest     int      `json:"peak_reads_in_flight_per_destination"`
	StoreCalls      int      `json:"existence_checks_made"`
	WallMS          int64    `json:"wall_ms"`
	SetupMS         int64    `json:"setup_ms"`
	Host            hostInfo `json:"host"`
}

type hostInfo struct {
	GOOS, GOARCH string
	CPUs         int
	Go           string
}

// violations is the list of promises this run broke, empty when none was.
func (r scaleRun) violations() []string {
	var v []string
	for i, n := range r.SelectedPerPass {
		if n > r.PerPassBound {
			v = append(v, fmt.Sprintf("pass %d took %d copies, the per-pass bound is %d", i+1, n, r.PerPassBound))
		}
	}
	if r.PeakGlobal > r.GlobalCeiling {
		v = append(v, fmt.Sprintf("%d reads were in flight at once, the global ceiling is %d", r.PeakGlobal, r.GlobalCeiling))
	}
	if r.PeakPerDest > r.DestCeiling {
		v = append(v, fmt.Sprintf("%d reads were in flight against one destination, the per-destination ceiling is %d", r.PeakPerDest, r.DestCeiling))
	}
	if r.Checked != r.Copies {
		v = append(v, fmt.Sprintf("%d of %d copies were ever checked: a bound that defers must still get to all of them", r.Checked, r.Copies))
	}
	return v
}

// runBacklog seeds `copies` copies due now, spread over three destinations,
// and runs scheduled passes under cfg and bounds until nothing is due.
func runBacklog(t *testing.T, copies int, cfg config.Revalidation, bounds Bounds, limits *Limits) scaleRun {
	t.Helper()
	ctx := context.Background()
	setupStart := time.Now()
	j := openJournal(t)
	set, err := model.NewBackupSetID("production", "postgres-primary")
	if err != nil {
		t.Fatal(err)
	}
	long := time.Now().UTC().Add(-90 * 24 * time.Hour)
	content := []byte("hello")
	for i := 0; i < copies; i++ {
		a := artifactNamed(t, fmt.Sprintf("backlog-%04d.dump", i))
		completeArtifact(t, j, a, content, long)
		moveToMedium(t, j, a, scaleMedium(i), content, long)
	}
	setup := time.Since(setupStart)

	store := newPeakStore(2 * time.Millisecond)
	// Each pass runs a minute after the last. A copy checked in pass N is next
	// due a cadence (a week) later, so the backlog is worked off by passes and
	// never by one pass looping back to a copy it has already done.
	clock := time.Now().UTC()
	deps := Deps{
		Journal: j, Store: store, Mediums: threeMediums{}, Limits: limits, Bounds: bounds,
		Now: func() time.Time { return clock },
	}
	gl, dst := limits.Caps()
	run := scaleRun{
		Copies: copies, PerPassBound: cfg.MaxPerCycle, GlobalCeiling: gl, DestCeiling: dst,
		Host: hostInfo{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, CPUs: runtime.NumCPU(), Go: runtime.Version()},
	}

	start := time.Now()
	maxPasses := copies/max(cfg.MaxPerCycle, 1) + 5
	if cfg.MaxPerCycle > copies {
		maxPasses = 5
	}
	for pass := 1; pass <= maxPasses; pass++ {
		rep, err := Run(ctx, deps, set, cfg)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if rep.Selected == 0 {
			break
		}
		run.Passes++
		run.SelectedPerPass = append(run.SelectedPerPass, rep.Selected)
		if pass == 1 {
			recs, err := j.ListByBackupSet(ctx, set)
			if err != nil {
				t.Fatal(err)
			}
			unbounded := cfg
			unbounded.MaxPerCycle = 1 << 30
			run.DueAfterPass1 = len(SelectDue(recs, unbounded, clock))
		}
		clock = clock.Add(time.Minute)
	}
	run.WallMS = time.Since(start).Milliseconds()
	run.SetupMS = setup.Milliseconds()

	recs, err := j.ListByBackupSet(ctx, set)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		for _, p := range rec.ActiveMediumPlacements() {
			if p.LastCheckedAt != nil && p.AssuranceState == state.AssurancePassed {
				run.Checked++
			}
		}
	}
	store.mu.Lock()
	run.PeakGlobal, run.StoreCalls = store.peak, store.calls
	for _, p := range store.perPeak {
		if p > run.PeakPerDest {
			run.PeakPerDest = p
		}
	}
	store.mu.Unlock()
	return run
}

func scaleCopies(t *testing.T) int {
	if v := os.Getenv("SCRUB_SCALE_COPIES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("SCRUB_SCALE_COPIES=%q", v)
		}
		return n
	}
	return 1050
}

func TestAThousandDueCopiesDeferPastTheScrubBoundAndTheReadCeilingsAreNeverExceeded(t *testing.T) {
	copies := scaleCopies(t)
	// What the serve scheduler asks for a set with no revalidation block.
	cfg := config.Assurance{}.Revalidation(config.Revalidation{})
	if cfg.MaxPerCycle != config.DefaultScrubMaxPerCycle {
		t.Fatalf("the derived per-pass bound is %d, want config.DefaultScrubMaxPerCycle (%d)", cfg.MaxPerCycle, config.DefaultScrubMaxPerCycle)
	}
	bounds := Bounds{}
	limits := NewLimits(bounds)

	run := runBacklog(t, copies, cfg, bounds, limits)

	if v := run.violations(); len(v) != 0 {
		t.Fatalf("the backlog broke its bounds: %v\n%+v", v, run)
	}
	wantPasses := (copies + cfg.MaxPerCycle - 1) / cfg.MaxPerCycle
	if run.Passes != wantPasses {
		t.Errorf("the backlog took %d passes, want %d (the copies divided by the per-pass bound)", run.Passes, wantPasses)
	}
	if run.SelectedPerPass[0] != cfg.MaxPerCycle {
		t.Errorf("pass 1 took %d copies, want exactly the bound %d", run.SelectedPerPass[0], cfg.MaxPerCycle)
	}
	if want := copies - cfg.MaxPerCycle; run.DueAfterPass1 != want {
		t.Errorf("%d copies were still due after pass 1, want %d: the rest has to be deferred, not dropped and not done early", run.DueAfterPass1, want)
	}
	// The positive control for the measurement itself: the harness can see
	// concurrency (it reached the ceiling), so "never exceeded" is a reading
	// and not the absence of one.
	if run.PeakGlobal != run.GlobalCeiling {
		t.Errorf("peak concurrency %d never reached the ceiling %d: the measurement cannot see an overrun", run.PeakGlobal, run.GlobalCeiling)
	}
	if run.StoreCalls != copies {
		t.Errorf("%d existence checks for %d copies", run.StoreCalls, copies)
	}
	t.Logf("%d copies: %d passes of at most %d; peak reads %d (ceiling %d), peak per destination %d (ceiling %d); %d ms for the passes on %s/%s with %d CPUs",
		copies, run.Passes, cfg.MaxPerCycle, run.PeakGlobal, run.GlobalCeiling, run.PeakPerDest, run.DestCeiling, run.WallMS,
		run.Host.GOOS, run.Host.GOARCH, run.Host.CPUs)
	if out := os.Getenv("SCRUB_SCALE_OUT"); out != "" {
		b, _ := json.MarshalIndent(run, "", "  ")
		if err := os.WriteFile(out, append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The planted controls. Each one runs the same backlog with one bound taken
// away and requires the harness to report exactly that overrun. A harness
// that cannot fail is not measuring anything.
func TestTheBacklogHarnessCatchesAPerPassBoundThatIsNotThere(t *testing.T) {
	cfg := config.Assurance{}.Revalidation(config.Revalidation{})
	cfg.MaxPerCycle = 1 << 20 // the bound removed
	bounds := Bounds{}
	run := runBacklog(t, 120, cfg, bounds, NewLimits(bounds))
	cfg.MaxPerCycle = config.DefaultScrubMaxPerCycle // the line it is judged against
	run.PerPassBound = cfg.MaxPerCycle
	v := run.violations()
	if len(v) == 0 {
		t.Fatalf("120 copies in one pass and no violation reported: %+v", run)
	}
	t.Logf("caught: %v", v)
}

func TestTheBacklogHarnessCatchesAReadCeilingThatIsNotThere(t *testing.T) {
	cfg := config.Assurance{}.Revalidation(config.Revalidation{})
	bounds := Bounds{GlobalConcurrency: 12, PerDestinationConcurrency: 12}
	// Workers and limiter both run at 12, but the line they are judged
	// against is the product default.
	run := runBacklog(t, 150, cfg, bounds, NewLimits(bounds))
	run.GlobalCeiling, run.DestCeiling = config.DefaultMaxConcurrentReads, config.DefaultMaxPerDestination
	v := run.violations()
	if len(v) == 0 {
		t.Fatalf("12 reads at once against a ceiling of %d and no violation reported: %+v", config.DefaultMaxConcurrentReads, run)
	}
	t.Logf("caught: %v", v)
}
