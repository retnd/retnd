package revalidate

import (
	"context"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// Panel finding C10, mutant M18: Options.Force was ignored and the whole
// suite stayed green. RunWith's contract is that an operator who names a copy
// is asking for it to be read NOW, whatever the configuration schedules, so
// `retnd scrub <set>` works on a set whose policy schedules nothing. Every
// other test passes a configuration that already schedules, so the clause that
// lets Force through an empty schedule was never driven.
//
// (After C1 the product never hands RunWith an empty schedule on its own,
// because the scrub's configuration is derived from the resolved policy. The
// clause is still RunWith's stated behaviour for any caller that does, and a
// mutant that deletes it was the one that survived.)
func TestRunWith_AForcedPassReadsACopyUnderAConfigurationThatSchedulesNothing(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	newCopy := func(name string) (*state.Journal, Deps) {
		j := openJournal(t)
		a := artifactNamed(t, name)
		commitArtifactWithPlacement(t, j, a, []byte("payload"), now.Add(-999*time.Hour))
		return j, Deps{Journal: j, Now: func() time.Time { return now }, Bounds: Bounds{Rand: func() float64 { return 0 }}}
	}
	// MaxPerCycle is what schedules work; Hash says what a pass may do with a
	// copy it does read, and the operator's forced pass in internal/app turns it
	// on (re-reading a local file costs nothing).
	schedulesNothing := config.Revalidation{Hash: true}

	// The control: with no Force the same configuration reads nothing, so the
	// forced result below is Force's doing and not the fixture's.
	j, deps := newCopy("unforced.dump")
	a := artifactNamed(t, "unforced.dump")
	rep, err := RunWith(ctx, deps, a.Set, schedulesNothing, Options{})
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if rep.Selected != 0 || len(rep.Findings) != 0 {
		t.Fatalf("an unforced pass under a configuration that schedules nothing selected %d and found %d, want a pass that does nothing", rep.Selected, len(rep.Findings))
	}
	if got := copyAssuranceState(t, j, a); got != state.AssuranceUnknown {
		t.Fatalf("the unforced pass left the copy %q, want unknown", got)
	}

	j, deps = newCopy("forced.dump")
	a = artifactNamed(t, "forced.dump")
	rep, err = RunWith(ctx, deps, a.Set, schedulesNothing, Options{Force: true})
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if rep.Selected != 1 {
		t.Fatalf("a forced pass selected %d artifacts, want the one the operator named the set of", rep.Selected)
	}
	if got := copyAssuranceState(t, j, a); got == state.AssuranceUnknown {
		t.Errorf("a forced pass left the copy unknown: Force did not make the pass read it")
	}
}

func copyAssuranceState(t *testing.T, j *state.Journal, a model.ArtifactID) string {
	t.Helper()
	rec, err := j.Get(context.Background(), a)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	p, ok := rec.LocalPlacement()
	if !ok {
		t.Fatalf("%s has no local copy", a)
	}
	return p.AssuranceState
}
