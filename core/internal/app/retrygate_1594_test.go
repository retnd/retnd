package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// Issue #1594's review: retry keeps an own copy on local evidence only when
// the source is confirmed gone (a fresh listing without it plus a Stat
// classified not_found). Keeping it forfeits the remote delete for good, so a
// source that is still there, or that cannot be asked, has to leave the copy
// alone and take today's ordinary retry. Each case uses a copy that verifies
// (honest record), so a gate that let it through would keep it.
func TestRetryOnlyKeepsAnOwnCopyWhenTheSourceIsConfirmedGone_1594(t *testing.T) {
	cases := []struct {
		name string
		arm  func(fx deadEndFixture, remote string)
	}{
		{name: "source still there with other bytes", arm: func(fx deadEndFixture, remote string) {
			fx.tr.put(remote, "the host rewrote this object after the copy was made", epoch.Unix())
		}},
		{name: "source not answering", arm: func(fx deadEndFixture, _ string) {
			fx.tr.listErr = transport.NewError(transport.Transient, "list", errors.New("the host did not answer"))
		}},
		{name: "not listed and Stat answers permission denied", arm: func(fx deadEndFixture, remote string) {
			fx.tr.statErrFor = map[string]error{remote: transport.NewError(transport.PermissionDenied, "stat", errors.New("permission denied"))}
		}},
		{name: "not listed and Stat answers transient", arm: func(fx deadEndFixture, remote string) {
			fx.tr.statErrFor = map[string]error{remote: transport.NewError(transport.Transient, "stat", errors.New("connection reset"))}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newDeadEndFixture(t, true)
			walkToFailed662(t, fx)
			remote := mustRecord662(t, fx).RemotePath
			if tc.name != "source still there with other bytes" {
				delete(fx.tr.objects, remote)
			}
			tc.arm(fx, remote)
			before := fileHash1594(t, fx.final)

			err := fx.svc.RetryFailedIngestion(context.Background(), fx.artifact, "issue #1594")
			if r, ok := AsRetryRefusal(err); ok {
				t.Fatalf("retry refused with %s; with the source not confirmed gone it takes the ordinary retry: %v", r.Code, err)
			}
			if err != nil {
				t.Fatalf("RetryFailedIngestion: %v", err)
			}
			if got := lifecycle.State(stateOf662(t, fx.journal, fx.artifact)); durable(got) || got == lifecycle.Quarantined {
				t.Errorf("%s is %s after retry; its copy was kept on local evidence although the source was not confirmed gone, "+
					"which forfeits the remote delete for good", fx.artifact, got)
			}
			if after := fileHash1594(t, fx.final); after != before {
				t.Errorf("the copy at %s changed during the retry", fx.final)
			}
		})
	}
}

// racingJournal runs one action right after the n-th Get of a row, which is
// how a second retry that wins the race is made deterministic.
type racingJournal struct {
	Journal
	gets, at int
	then     func()
}

func (j *racingJournal) Get(ctx context.Context, id model.ArtifactID) (state.Record, error) {
	rec, err := j.Journal.Get(ctx, id)
	if j.gets++; j.gets == j.at && j.then != nil {
		f := j.then
		j.then = nil
		f()
	}
	return rec, err
}

// Two retries at once: the loser meets the row already moved out of FAILED at
// the hold it takes before the reinstatement, and that is "not stuck" (409),
// never a wrapped internal error.
func TestALosingRetryOfAnOwnCopyIsNotStuckNotAnInternalError_1594(t *testing.T) {
	// The winner lands after the loser's second or third read of the row: the
	// two places it can notice the row has moved on.
	for _, at := range []int{1, 2} {
		t.Run(fmt.Sprintf("after read %d", at), func(t *testing.T) {
			fx := ownCopySourceGone(t, true)
			inner := fx.svc.Journal
			race := &racingJournal{Journal: inner, at: at}
			race.then = func() {
				fx.svc.Journal = inner
				if err := fx.svc.RetryFailedIngestion(context.Background(), fx.artifact, "the winner"); err != nil {
					t.Errorf("the winning retry failed: %v", err)
				}
				fx.svc.Journal = race
			}
			fx.svc.Journal = race
			err := fx.svc.RetryFailedIngestion(context.Background(), fx.artifact, "the loser")
			if race.then != nil {
				t.Fatalf("the winner never ran (reads seen: %d); the hook point moved", race.gets)
			}
			if !errors.Is(err, ErrNotFailed) {
				t.Errorf("the losing retry = %v, want an error that is ErrNotFailed", err)
			}
		})
	}
}
