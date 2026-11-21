package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// Issue #1611: a FAILED backup whose own copy sits at its final name, whose
// source is still on the host, and whose copy does not match the remote
// object. Retry compared the two, saw the difference, fell through to an
// ordinary retry, the next cycle met the collision guard on the copy and the
// row went back to FAILED. Forever.
// See docs/design/1611-own-copy-mismatch-source-present.md.

const rewritten1611 = "the host rewrote this object after the copy was made, and it is longer"

// ownCopyMismatch1611 is the issue's shape: FAILED, the copy at the final name,
// the source present, and the two differing. "remote-rewritten" leaves the copy
// as it was recorded and changes the object; "copy-damaged" changes the copy.
func ownCopyMismatch1611(t *testing.T, kind string) deadEndFixture {
	t.Helper()
	fx := newDeadEndFixture(t, true)
	walkToFailed662(t, fx)
	remote := mustRecord662(t, fx).RemotePath
	switch kind {
	case "remote-rewritten":
		fx.tr.put(remote, rewritten1611, epoch.Unix())
	case "copy-damaged":
		mustWriteFile(t, fx.final, strings.Repeat("Z", len(payload662)))
	default:
		t.Fatalf("unknown shape %q", kind)
	}
	if h := fileHash1594(t, fx.final); strings.EqualFold(h, fx.tr.hash(fx.tr.objects[remote])) {
		t.Fatal("precondition: the copy and the remote object hash the same; this shape needs them to differ")
	}
	return fx
}

// The loop, and what ends it: every retry is refused by name, writes nothing,
// and leaves the copy where it is, with a cycle in between to prove nothing
// else moves the row either.
func TestRetryRefusesAnOwnCopyThatDoesNotMatchAPresentSource_1611(t *testing.T) {
	for _, kind := range []string{"remote-rewritten", "copy-damaged"} {
		t.Run(kind, func(t *testing.T) {
			fx := ownCopyMismatch1611(t, kind)
			ctx := context.Background()
			before := fileHash1594(t, fx.final)
			row := mustRecord662(t, fx)

			for attempt := 1; attempt <= 3; attempt++ {
				err := fx.svc.RetryFailedIngestion(ctx, fx.artifact, "issue #1611")
				r, ok := AsRetryRefusal(err)
				if !ok || r.Code != RefusalRetryOwnCopyMismatch {
					t.Fatalf("attempt %d: retry = %v, want a refusal with code %s (without it the row goes DISCOVERED and the collision guard sends it back to FAILED)",
						attempt, err, RefusalRetryOwnCopyMismatch)
				}
				if got := lifecycle.State(stateOf662(t, fx.journal, fx.artifact)); got != lifecycle.Failed {
					t.Fatalf("attempt %d: %s is %s after the refusal, want FAILED untouched", attempt, fx.artifact, got)
				}
				if now := mustRecord662(t, fx); !reflect.DeepEqual(now, row) {
					t.Fatalf("attempt %d: the refusal wrote to the row:\n before %+v\n after  %+v", attempt, row, now)
				}
				fx.svc.RunCycle(ctx)
			}
			if after := fileHash1594(t, fx.final); after != before {
				t.Errorf("the copy at %s changed", fx.final)
			}
			if !strings.Contains(r1611(fx.svc.RetryFailedIngestion(ctx, fx.artifact, "")).Sentence, fx.final) {
				t.Errorf("the sentence does not name %s, the file the operator has to move", fx.final)
			}
		})
	}
}

func r1611(err error) *RetryRefusal {
	r, _ := AsRetryRefusal(err)
	if r == nil {
		return &RetryRefusal{}
	}
	return r
}

// The sentence says what was found and the way out, and it never repeats a
// string the remote chose: the remote object's checksum stays out.
func TestTheMismatchRefusalNamesTheWayOutAndNothingTheRemoteChose_1611(t *testing.T) {
	for kind, wantFact := range map[string]string{
		"remote-rewritten": "still matches the checksum recorded",
		"copy-damaged":     "does not match the checksum recorded",
	} {
		t.Run(kind, func(t *testing.T) {
			fx := ownCopyMismatch1611(t, kind)
			s := r1611(fx.svc.RetryFailedIngestion(context.Background(), fx.artifact, "")).Sentence
			for _, want := range []string{wantFact, "Move", "outside", "retry", "do not track"} {
				if !strings.Contains(s, want) {
					t.Errorf("sentence lacks %q: %s", want, s)
				}
			}
			remote := mustRecord662(t, fx)
			for _, bad := range []string{fx.tr.hash(fx.tr.objects[remote.RemotePath])} {
				if strings.Contains(s, bad) {
					t.Errorf("sentence repeats a string the remote supplied (%q): %s", bad, s)
				}
			}
		})
	}
}

// The way out the sentence names has to work: with the copy moved aside by
// hand, retry is an ordinary retry, the next cycle fetches a fresh copy, and
// the old file is still there under its new name.
func TestMovingTheMismatchedCopyAsideLetsRetryFetchAFreshOne_1611(t *testing.T) {
	fx := ownCopyMismatch1611(t, "copy-damaged")
	ctx := context.Background()
	aside := fx.final + ".mismatch"
	damaged := fileHash1594(t, fx.final)
	if err := os.Rename(fx.final, aside); err != nil {
		t.Fatal(err)
	}

	if err := fx.svc.RetryFailedIngestion(ctx, fx.artifact, "moved the copy aside"); err != nil {
		t.Fatalf("retry after moving the copy aside: %v", err)
	}
	fx.svc.RunCycle(ctx)
	got := lifecycle.State(stateOf662(t, fx.journal, fx.artifact))
	if !durable(got) {
		t.Fatalf("%s is %s after retry and a cycle, want a durable state (the fresh copy)", fx.artifact, got)
	}
	remote := mustRecord662(t, fx).RemotePath
	if h := fileHash1594(t, fx.final); !strings.EqualFold(h, fx.tr.hash(fx.tr.objects[remote])) {
		t.Errorf("the fresh copy at %s does not match the remote object", fx.final)
	}
	if h := fileHash1594(t, aside); h != damaged {
		t.Errorf("the old copy at %s changed", aside)
	}
}

// What stays as it was: a copy that matches the remote is still kept in place.
func TestAMatchingOwnCopyIsStillKeptInPlace_1611(t *testing.T) {
	fx := newDeadEndFixture(t, true)
	walkToFailed662(t, fx)
	if err := fx.svc.RetryFailedIngestion(context.Background(), fx.artifact, ""); err != nil {
		t.Fatalf("a copy that matches the source: %v", err)
	}
	if got := lifecycle.State(stateOf662(t, fx.journal, fx.artifact)); !durable(got) {
		t.Errorf("a matching copy ends %s, want it kept in a durable state", got)
	}
}

// Only a definite mismatch is refused. A retry that cannot get a checksum (the
// remote does not answer for it, or the copy cannot be read) knows nothing
// about the two differing, so it takes the ordinary retry as #1594 pinned, and
// the copy is not touched.
func TestARetryThatCannotCompareIsNotAMismatch_1611(t *testing.T) {
	for _, tc := range []string{"remote checksum fails", "copy unreadable"} {
		t.Run(tc, func(t *testing.T) {
			fx := newDeadEndFixture(t, true)
			walkToFailed662(t, fx)
			if tc == "remote checksum fails" {
				fx.tr.remoteHashErr = transport.NewError(transport.Transient, "remote_hash", errors.New("the host did not answer"))
			} else {
				if os.Geteuid() == 0 {
					t.Skip("root reads a mode 0 file")
				}
				if err := os.Chmod(fx.final, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(fx.final, 0o644) })
			}
			err := fx.svc.RetryFailedIngestion(context.Background(), fx.artifact, "")
			if r, ok := AsRetryRefusal(err); ok {
				t.Fatalf("refused with %s although nothing showed the copy differs from the source", r.Code)
			}
			if err != nil {
				t.Fatalf("RetryFailedIngestion: %v", err)
			}
			if got := lifecycle.State(stateOf662(t, fx.journal, fx.artifact)); got != lifecycle.Discovered {
				t.Errorf("%s is %s, want DISCOVERED (the ordinary retry)", fx.artifact, got)
			}
		})
	}
}

// Retire still refuses this shape while the source is there, and its sentence
// has to stop promising that retry keeps the copy: it only does when the two
// match.
func TestTheRetireRefusalSaysRetryKeepsTheCopyOnlyIfItMatches_1611(t *testing.T) {
	fx := ownCopyMismatch1611(t, "remote-rewritten")
	p, err := fx.svc.PreviewRetirement(context.Background(), fx.artifact, RetireRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Refusal == nil || p.Refusal.Code != RefusalOwnCopy {
		t.Fatalf("refusal = %+v, want %s", p.Refusal, RefusalOwnCopy)
	}
	if p.SourceCheck.Result != state.SourcePresent {
		t.Fatalf("source check = %q, want present", p.SourceCheck.Result)
	}
	if !strings.Contains(p.Refusal.Sentence, "do not match") {
		t.Errorf("the sentence says retry keeps the copy without saying what happens when it does not match: %q", p.Refusal.Sentence)
	}
}
