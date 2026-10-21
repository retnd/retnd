package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The review of #1256, closing its M4 doubt. #1193 wants every retained
// backup of a set onto its production host asked for in so many words. The
// terminal spells that --all, and refuses a range that keeps the whole set,
// but the API had no way to say "all", so a request with no narrowing (or a
// range that kept everything) went through there while its echoed command was
// refused in a terminal. RecoverManyRequest.All is that word now, and the
// service applies the same rule the terminal does, with the same reason, so
// the two cannot disagree. A plan (the pre-flight) writes nothing and is not
// held to it, as `recover --preflight` is not.

func TestIssue1251_AWholeSetOntoTheSourceNeedsAll(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	f.seedMany(t,
		manyFile{name: "a.zst", data: manyData(1, 40_000), age: 10 * 24 * time.Hour},
		manyFile{name: "b.zst", data: manyData(2, 30_000), age: 5 * 24 * time.Hour},
	)
	ctx := context.Background()
	tg := ackedSource(t, f, "")
	for name, req := range map[string]RecoverManyRequest{
		"no narrowing":              {Set: "production/postgres", Target: tg},
		"an until after everything": {Set: "production/postgres", Target: tg, Until: time.Now().Add(24 * time.Hour)},
		"a since before everything": {Set: "production/postgres", Target: tg, Since: time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		_, err := f.b.RecoverMany(ctx, f.session(), req, nil)
		if !errors.Is(err, ErrRecoverManyInvalid) {
			t.Errorf("%s: RecoverMany = %v, want ErrRecoverManyInvalid", name, err)
			continue
		}
		for _, says := range []string{"all 3 retained restore points of production/postgres", "--all", `"all": true`} {
			if !strings.Contains(err.Error(), says) {
				t.Errorf("%s: the refusal %q does not say %q", name, err, says)
			}
		}
	}
	if host.probes.Load() != 0 || host.puts.Load() != 0 {
		t.Fatalf("a refused whole-set run touched the source host (%d probes, %d uploads)", host.probes.Load(), host.puts.Load())
	}

	// The pre-flight is not held to it.
	if _, err := f.b.PlanRecoverMany(ctx, RecoverManyRequest{Set: "production/postgres", Target: tg}); err != nil {
		t.Errorf("a plan of the whole set onto the source = %v, want a plan", err)
	}

	// A range that leaves something out is a narrowing.
	res, err := f.b.RecoverMany(ctx, f.session(), RecoverManyRequest{Set: "production/postgres", Target: ackedSource(t, f, ""), Until: time.Now().Add(-7 * 24 * time.Hour)}, nil)
	if err != nil || res.Totals.Recovered != 1 {
		t.Errorf("an until that keeps one of three = %+v, %v, want one recovered", res.Totals, err)
	}

	// All says it.
	res, err = f.b.RecoverMany(ctx, f.session(), RecoverManyRequest{Set: "production/postgres", Target: ackedSource(t, f, ""), All: true}, nil)
	if err != nil || res.Totals.Recovered != 3 {
		t.Errorf("all onto the source = %+v, %v, want three recovered", res.Totals, err)
	}
}

func TestIssue1251_AllIsRefusedBesideANarrowingOrWithoutASet(t *testing.T) {
	f, _ := newSourceFixture(t, sftpSet(false))
	ctx := context.Background()
	tg := ackedSource(t, f, "")
	for name, req := range map[string]RecoverManyRequest{
		"with newest-only": {Set: "production/postgres", Target: tg, All: true, NewestOnly: true},
		"with since":       {Set: "production/postgres", Target: tg, All: true, Since: time.Now().Add(-time.Hour)},
		"with until":       {Set: "production/postgres", Target: tg, All: true, Until: time.Now()},
		"without a set":    {Artifacts: []string{"production/postgres/dump.zst"}, Target: tg, All: true},
	} {
		if _, err := f.b.PlanRecoverMany(ctx, req); !errors.Is(err, ErrRecoverManyInvalid) {
			t.Errorf("%s: PlanRecoverMany = %v, want ErrRecoverManyInvalid", name, err)
		}
	}
}

func TestIssue1251_AWholeSetIntoStagingNeedsNoAll(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	res, err := f.b.RecoverMany(context.Background(), f.session(), setReq("whole"), nil)
	if err != nil || res.Totals.Recovered != 1 {
		t.Errorf("a whole set into staging = %+v, %v, want it recovered with no all", res.Totals, err)
	}
}
