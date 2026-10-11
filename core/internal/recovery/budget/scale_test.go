package budget_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/recovery/budget"
	"github.com/retnd/retnd/core/internal/recovery/export"
)

// A thousand copies due at the same instant is the day a catalog is rebuilt
// or a scheduler is switched on for a fleet that never had one. This is the
// model of that day: the real selection, the real read budget and the real
// verifying engine, driven the way a scheduler would drive them, with the
// peaks read off the limiter rather than asserted from the outside.
//
// RECOVERY_BUDGET_COPIES overrides the count, RECOVERY_BUDGET_BYTES the size
// of each copy, and RECOVERY_BUDGET_OUT writes the measurement as JSON for
// docs/perf/epic-q-recovery.md.

type fakeCopy struct {
	id, dest string
	data     []byte
	sum      string
}

type copySource struct {
	c     *fakeCopy
	reads *atomic.Int64
}

func (s copySource) Open(context.Context) (io.ReadCloser, error) {
	s.reads.Add(1)
	return io.NopCloser(bytes.NewReader(s.c.data)), nil
}

type scaleResult struct {
	Copies            int     `json:"copies"`
	CopyBytes         int     `json:"copy_bytes"`
	PerPassBound      int     `json:"per_pass_bound"`
	Passes            int     `json:"passes"`
	MaxConsideredPass int     `json:"max_considered_in_a_pass"`
	GlobalCeiling     int     `json:"global_read_ceiling"`
	PeakGlobalReads   int     `json:"peak_global_reads"`
	PeakPerDest       int     `json:"peak_reads_on_one_destination"`
	DestCeiling       int     `json:"per_destination_ceiling"`
	Refusals          int64   `json:"budget_refusals_deferred_to_a_later_pass"`
	Seconds           float64 `json:"wall_seconds"`
	Host              string  `json:"host"`
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func runScale(t *testing.T, copies, size int) scaleResult {
	t.Helper()
	l := budget.Defaults()
	reads := budget.NewReads(l)
	staging := &export.Staging{Root: t.TempDir(), FreeBytes: func(string) (int64, error) { return 1 << 40, nil }}
	engine := &export.Engine{Staging: staging, Reads: reads}

	// Three destinations with very unequal shares, so the per-destination
	// ceiling has something to do: most of the fleet lives on one bucket.
	dests := []string{"bulk", "bulk", "bulk", "bulk", "mid", "small"}
	all := make([]*fakeCopy, copies)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var due []budget.Candidate
	for i := range all {
		data := make([]byte, size)
		for j := range data {
			data[j] = byte(i + j)
		}
		h := sha256.Sum256(data)
		all[i] = &fakeCopy{id: "set/" + strconv.Itoa(i), dest: dests[i%len(dests)], data: data, sum: hex.EncodeToString(h[:])}
		due = append(due, budget.Candidate{ID: all[i].id, Destination: all[i].dest, Due: base.Add(time.Duration(copies-i) * time.Second)})
	}
	byID := map[string]*fakeCopy{}
	for _, c := range all {
		byID[c.id] = c
	}

	var refusals, sourceReads atomic.Int64
	done := map[string]bool{}
	var doneMu sync.Mutex
	res := scaleResult{Copies: copies, CopyBytes: size, PerPassBound: l.MaxCopiesPerTick, GlobalCeiling: l.MaxConcurrentReads, DestCeiling: l.MaxReadsPerDestination}

	start := time.Now()
	for len(due) > 0 {
		res.Passes++
		if res.Passes > copies+10 {
			t.Fatal("the scheduler is not converging: passes keep going while copies stay due")
		}
		considered, deferred := budget.SelectTick(due, l)
		if len(considered) > res.MaxConsideredPass {
			res.MaxConsideredPass = len(considered)
		}
		if len(considered) > l.MaxCopiesPerTick {
			t.Fatalf("pass %d considered %d copies, over the bound of %d", res.Passes, len(considered), l.MaxCopiesPerTick)
		}

		work := make(chan budget.Candidate)
		var retry []budget.Candidate
		var retryMu sync.Mutex
		var wg sync.WaitGroup
		// More workers than the global ceiling on purpose: the limiter, not
		// the worker count, is what holds the line.
		for w := 0; w < l.MaxConcurrentReads*2; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for cand := range work {
					c := byID[cand.ID]
					n := int64(len(c.data))
					s, err := engine.Stage(context.Background(), copySource{c, &sourceReads}, export.Expect{
						Size: &n, HashAlg: "sha256", Hash: c.sum, Origin: export.OriginRemote, Medium: c.dest,
					})
					if err != nil {
						if errors.Is(err, budget.ErrOverBudget) {
							refusals.Add(1)
							retryMu.Lock()
							retry = append(retry, cand)
							retryMu.Unlock()
							continue
						}
						t.Errorf("%s: %v", c.id, err)
						continue
					}
					_ = s.Discard()
					doneMu.Lock()
					done[c.id] = true
					doneMu.Unlock()
				}
			}()
		}
		for _, c := range considered {
			work <- c
		}
		close(work)
		wg.Wait()
		due = append(deferred, retry...)
	}
	res.Seconds = time.Since(start).Seconds()
	res.Refusals = refusals.Load()
	peakGlobal, perDest := reads.Peaks()
	res.PeakGlobalReads = peakGlobal
	for _, p := range perDest {
		if p > res.PeakPerDest {
			res.PeakPerDest = p
		}
	}
	if len(done) != copies {
		t.Fatalf("%d of %d copies completed", len(done), copies)
	}
	if got := sourceReads.Load(); got != int64(copies) {
		t.Fatalf("%d source reads for %d copies: a refused attempt must not have opened its copy", got, copies)
	}
	res.Host = runtime.GOOS + "/" + runtime.GOARCH + " " + strconv.Itoa(runtime.NumCPU()) + " threads"
	return res
}

func TestOneThousandDueCopiesCompleteInsideThePerPassBoundWithTheCeilingsNeverExceeded(t *testing.T) {
	copies := envInt("RECOVERY_BUDGET_COPIES", 1200)
	size := envInt("RECOVERY_BUDGET_BYTES", 16<<10)
	res := runScale(t, copies, size)

	l := budget.Defaults()
	if res.MaxConsideredPass > l.MaxCopiesPerTick {
		t.Errorf("a pass considered %d copies, over %d", res.MaxConsideredPass, l.MaxCopiesPerTick)
	}
	if res.PeakGlobalReads > l.MaxConcurrentReads {
		t.Errorf("%d full reads ran at once, over the ceiling of %d", res.PeakGlobalReads, l.MaxConcurrentReads)
	}
	if res.PeakPerDest > l.MaxReadsPerDestination {
		t.Errorf("%d full reads of one destination ran at once, over %d", res.PeakPerDest, l.MaxReadsPerDestination)
	}
	if res.PeakGlobalReads < 2 {
		t.Errorf("peak global reads = %d: the model never ran anything concurrently, so the ceilings above were not tested", res.PeakGlobalReads)
	}
	if res.Refusals == 0 {
		t.Error("the limiter never refused anything, so the per-destination ceiling was not exercised by a fleet that is mostly on one bucket")
	}
	if want := (copies + l.MaxCopiesPerTick - 1) / l.MaxCopiesPerTick; res.Passes < want {
		t.Errorf("%d passes for %d copies at %d a pass: it cannot have finished in fewer than %d", res.Passes, copies, l.MaxCopiesPerTick, want)
	}
	blob, _ := json.MarshalIndent(res, "", "  ")
	t.Logf("%s", blob)
	if out := os.Getenv("RECOVERY_BUDGET_OUT"); out != "" {
		if err := os.WriteFile(out, append(blob, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheScaleModelCanActuallyFail(t *testing.T) {
	// The positive control: take the read ceilings away and the same model
	// must report a peak over the line, or the test above proves nothing.
	l := budget.Limits{MaxConcurrentReads: 64, MaxReadsPerDestination: 64, MaxCopiesPerTick: 64}
	reads := budget.NewReads(l)
	var wg sync.WaitGroup
	var running, peak atomic.Int32
	for i := 0; i < 48; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := reads.Acquire("d")
			if err != nil {
				return
			}
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			running.Add(-1)
			rel()
		}()
	}
	wg.Wait()
	if g, _ := reads.Peaks(); g <= budget.Defaults().MaxConcurrentReads {
		t.Fatalf("with the ceiling lifted the peak was only %d: the model cannot tell a held line from an absent one", g)
	}
	_ = strings.Repeat
}
