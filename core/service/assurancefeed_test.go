package service

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/obs"
)

// EPIC Q, Q1.4 (#982): the feed volume of a scrub pass is a decision. A
// thousand healthy copies must not become a thousand rows in an append-only,
// cursor-paged feed.

func scrubFixture(n int, outcome string) []ScrubCopyResult {
	out := make([]ScrubCopyResult, n)
	for i := range out {
		out[i] = ScrubCopyResult{
			Artifact: fmt.Sprintf("alpha/nightly/a%04d.dump", i), Medium: "local", Outcome: outcome, BytesRead: 1 << 20,
		}
		if outcome != ScrubPass {
			out[i].Reason = "digest mismatch"
		}
	}
	return out
}

func feedRows(t *testing.T, results []ScrubCopyResult) (rows int, end LiveActivityEvent, la *liveActivity) {
	t.Helper()
	la = newLiveActivity(configuredSets("alpha/nightly"))
	logger := obs.New(io.Discard, obs.LevelDebug).WithSink(la)
	EmitScrubPass(context.Background(), logger, "run-1", "alpha/nightly", results, time.Second)
	snap := la.snapshot("alpha/nightly", 0, 1000)
	for _, e := range snap.Events {
		if e.Event == obs.EventScrubEnd {
			end = e
		}
	}
	return len(snap.Events), end, la
}

func field(e LiveActivityEvent, k string) string {
	for _, f := range e.Fields {
		if f.Key == k {
			return f.Value
		}
	}
	return ""
}

func TestScrubFeed_AThousandHealthyCopiesCostTwoRows(t *testing.T) {
	rows, end, _ := feedRows(t, scrubFixture(1000, ScrubPass))
	if rows != 2 {
		t.Fatalf("1000 passing copies produced %d feed rows, want 2 (start and end)", rows)
	}
	if end.Result != string(obs.ResultSuccess) || field(end, "checked") != "1000" || field(end, "passed") != "1000" {
		t.Errorf("end line = %+v", end)
	}
}

func TestScrubFeed_AThousandFailingCopiesAreCappedAndSayTheyWere(t *testing.T) {
	rows, end, _ := feedRows(t, scrubFixture(1000, ScrubFail))
	if want := MaxScrubCopyRows + 2; rows != want {
		t.Fatalf("1000 failing copies produced %d rows, want %d", rows, want)
	}
	if end.Result != string(obs.ResultError) || field(end, "failed") != "1000" || field(end, "truncated") != fmt.Sprint(1000-MaxScrubCopyRows) {
		t.Errorf("end line must state error, the full count and what was left out: %+v", end)
	}
}

func TestScrubFeed_RefusedForCostIsAWarnNotAPass(t *testing.T) {
	mixed := append(scrubFixture(3, ScrubPass), ScrubCopyResult{Artifact: "alpha/nightly/z", Medium: "glacier", Outcome: ScrubRefused, Reason: "archive tier needs a thaw"})
	_, end, _ := feedRows(t, mixed)
	if end.Result != string(obs.ResultWarn) || field(end, "refused") != "1" {
		t.Errorf("a pass with a refused copy must not read as success: %+v", end)
	}
}

// The durable, cursor-paged read is untouched by feed volume: scrub rows live
// on the live feed and never enter the transition log, so paging cost stays
// where it was.
func TestScrubFeed_DoesNotGrowTheDurablePagedRead(t *testing.T) {
	svc, _ := openAssuranceService(t, "")
	before, _, err := svc.ListActivity(context.Background(), MaxActivityLimit, "")
	if err != nil {
		t.Fatal(err)
	}
	logger := obs.New(io.Discard, obs.LevelDebug).WithSink(svc.activity)
	EmitScrubPass(context.Background(), logger, "run-1", inheritingSet, scrubFixture(1000, ScrubFail), time.Second)
	after, next, err := svc.ListActivity(context.Background(), MaxActivityLimit, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || len(after) > MaxActivityLimit {
		t.Errorf("durable page grew from %d to %d rows", len(before), len(after))
	}
	_ = next
}

// Review finding: a pass that looked at nothing must not read as a pass.
func TestScrubFeed_AnEmptyPassIsNeutralNotSuccess(t *testing.T) {
	_, end, _ := feedRows(t, nil)
	if end.Result != string(obs.ResultInfo) {
		t.Errorf("an empty pass states %q, want %q: nothing was checked, so nothing passed", end.Result, obs.ResultInfo)
	}
}
