package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// Issue #1594: a FAILED backup whose own copy sits at its final name (#662's
// shape) and whose source the host has since deleted can never leave FAILED.
//
// Both ways out were closed. `retire` refused it with
// RETIRE_OWN_COPY_AT_FINAL_NAME ("retry instead") and nothing could override
// that, because #1528 trusted retry to rescue the shape. And retry's rescue is
// a comparison with the remote object (completeIngestionInPlace), so with the
// object gone it fell through to an ordinary retry, the FR-12 collision guard
// sent the row straight back to FAILED, and the set read FAILING for good.
//
// What the engine owes is in docs/design/1594-own-copy-source-gone.md:
//
//   - the copy still matches the hash recorded when it was verified (or the
//     set's validator passes on it): retry keeps it, through the
//     reinstatement path, without the remote. Retiring it stays refused,
//     because that would stop counting a good backup;
//   - the copy cannot be verified: retry refuses by name and writes nothing,
//     rather than sending the row round a loop that can only fail; retire
//     goes ahead only with the operator's accept_unverified_copy
//     acknowledgement, and the file stays where it is;
//   - none of that applies unless the source is confirmed gone (the same
//     listing plus classified Stat #1528 uses). With the source there, or not
//     answering, the shape behaves exactly as before.
//
// The fixture is #662's own (newDeadEndFixture, walkToFailed662): a 294-byte
// file at the final name, walked through real edges to FAILED by a quarantine
// retry and a cycle meeting the collision guard. honestRecord=true is a copy
// that verifies; false is #662's empty record, which is what a copy that no
// longer matches its recorded hash looks like from here.

// ownCopySourceGone builds the fixture, walks it to FAILED and then has the
// host delete the source object.
func ownCopySourceGone(t *testing.T, honestRecord bool) deadEndFixture {
	t.Helper()
	fx := newDeadEndFixture(t, honestRecord)
	walkToFailed662(t, fx)
	rec := mustRecord662(t, fx)
	if _, ok := fx.tr.objects[rec.RemotePath]; !ok {
		t.Fatalf("precondition: the fake source has no object at %q", rec.RemotePath)
	}
	delete(fx.tr.objects, rec.RemotePath)
	return fx
}

func fileHash1594(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func healthOf1594(t *testing.T, fx deadEndFixture) health.BackupSetHealth {
	t.Helper()
	rep, err := fx.svc.BuildHealthReport(context.Background(), VersionInfo{})
	if err != nil {
		t.Fatalf("BuildHealthReport: %v", err)
	}
	for _, h := range rep.BackupSets {
		if h.Set == fx.artifact.Set {
			return h
		}
	}
	t.Fatalf("the health report has no entry for %s", fx.artifact.Set)
	return health.BackupSetHealth{}
}

// The issue's reproduction, and the case retry owes: the copy verifies, the
// source is gone, and retry ends in a durable state on local evidence alone.
func TestRetryKeepsAVerifiedOwnCopyOnceTheSourceIsGone_1594(t *testing.T) {
	fx := ownCopySourceGone(t, true)
	ctx := context.Background()
	before := fileHash1594(t, fx.final)

	if h := healthOf1594(t, fx); h.State != health.Failing {
		t.Fatalf("precondition: the set is %s (%s), want FAILING", h.State, h.Reason)
	}

	if err := fx.svc.RetryFailedIngestion(ctx, fx.artifact, "issue #1594"); err != nil {
		t.Fatalf("RetryFailedIngestion: %v", err)
	}
	got := lifecycle.State(stateOf662(t, fx.journal, fx.artifact))
	if !durable(got) {
		t.Fatalf("after retry %s is %s; want a durable restore point, because its own copy at %s still matches the hash "+
			"recorded when it was verified and the source is gone, so there is nothing a re-fetch could do better", fx.artifact, got, fx.final)
	}
	if got != lifecycle.RemoteRetained {
		t.Errorf("%s came back as %s; it was quarantined out of REMOTE_RETAINED and has to return there", fx.artifact, got)
	}
	if after := fileHash1594(t, fx.final); after != before {
		t.Errorf("the copy at %s changed from %s to %s during the retry", fx.final, before, after)
	}
	// And it stays put: the next cycles find nothing to do with it, and the
	// set stops reading FAILING over a backup that was there all along.
	fx.svc.RunCycle(ctx)
	fx.svc.RunCycle(ctx)
	if got := lifecycle.State(stateOf662(t, fx.journal, fx.artifact)); !durable(got) {
		t.Errorf("two cycles after the retry %s is %s, want it still durable", fx.artifact, got)
	}
	if h := healthOf1594(t, fx); h.State == health.Failing {
		t.Errorf("the set still reads FAILING (%s) after its only failure was kept from its own verified copy", h.Reason)
	}
}

// A copy that does not verify, with nothing left to fetch: retry refuses by
// name, writes nothing, and says what to do instead. The ordinary retry it
// used to fall through to can only fail on the collision guard, which is the
// loop.
func TestRetryRefusesAnOwnCopyItCannotVerifyOnceTheSourceIsGone_1594(t *testing.T) {
	fx := ownCopySourceGone(t, false)
	ctx := context.Background()
	rows := transitionRows(t, fx.journal)
	before := fileHash1594(t, fx.final)

	err := fx.svc.RetryFailedIngestion(ctx, fx.artifact, "issue #1594")
	if err == nil {
		t.Fatalf("retry accepted %s (now %s) although its source is gone and its own copy does not match the hash recorded "+
			"at verification; the only thing an ordinary retry can do from here is meet the collision guard again", fx.artifact,
			stateOf662(t, fx.journal, fx.artifact))
	}
	r, ok := AsRetryRefusal(err)
	if !ok {
		t.Fatalf("retry error = %v (%T), want a *RetryRefusal", err, err)
	}
	if r.Code != RefusalRetryOwnCopyUnverified {
		t.Errorf("refusal code = %q, want %q", r.Code, RefusalRetryOwnCopyUnverified)
	}
	for _, want := range []string{"source", "retire", "--accept-unverified-copy"} {
		if !strings.Contains(r.Sentence, want) {
			t.Errorf("the refusal does not mention %q, so it does not name the way out: %q", want, r.Sentence)
		}
	}
	if got := stateOf662(t, fx.journal, fx.artifact); got != string(lifecycle.Failed) {
		t.Errorf("%s is %s after a refused retry, want FAILED", fx.artifact, got)
	}
	if n := transitionRows(t, fx.journal); n != rows {
		t.Errorf("a refused retry wrote %d transition rows; a refusal writes nothing", n-rows)
	}
	if after := fileHash1594(t, fx.final); after != before {
		t.Errorf("the copy at %s changed during a refused retry", fx.final)
	}
}

// The preview names the acknowledgement once the source is gone, and stops
// telling the operator that retry "checks that copy against the source",
// which with the source gone is no longer true.
func TestTheOwnCopyRefusalNamesTheAcknowledgementOnceTheSourceIsGone_1594(t *testing.T) {
	fx := ownCopySourceGone(t, false)
	p, err := fx.svc.PreviewRetirement(context.Background(), fx.artifact, RetireRequest{})
	if err != nil {
		t.Fatalf("PreviewRetirement: %v", err)
	}
	if p.Refusal == nil || p.Refusal.Code != RefusalOwnCopy {
		t.Fatalf("refusal = %+v, want %s", p.Refusal, RefusalOwnCopy)
	}
	if p.Refusal.OverridableWith != AckAcceptUnverifiedCopy {
		t.Errorf("overridable_with = %q, want %q: with the source gone nothing else can ever take this row out of FAILED",
			p.Refusal.OverridableWith, AckAcceptUnverifiedCopy)
	}
	if p.SourceCheck.Result != state.SourceAbsent {
		t.Errorf("source check = %q, want %q: the preview has to ask the source for this shape too", p.SourceCheck.Result, state.SourceAbsent)
	}
	if strings.Contains(p.Refusal.Sentence, "against the source") {
		t.Errorf("the sentence still says retry compares the copy with the source, which is gone: %q", p.Refusal.Sentence)
	}
}

// With the acknowledgement, a copy that does not verify is retired and the
// file stays exactly where it is: nothing is deleted on a judgement that the
// copy could not be proven.
func TestRetiringAnUnverifiableOwnCopyKeepsTheFile_1594(t *testing.T) {
	fx := ownCopySourceGone(t, false)
	ctx := context.Background()
	before := fileHash1594(t, fx.final)
	req := RetireRequest{AcceptUnverifiedCopy: true, NoSuccessor: true, Note: "issue #1594"}

	p, err := fx.svc.PreviewRetirement(ctx, fx.artifact, req)
	if err != nil {
		t.Fatalf("PreviewRetirement: %v", err)
	}
	if p.Refusal != nil {
		t.Fatalf("with both acknowledgements the preview still refuses: %+v", p.Refusal)
	}
	kept := false
	for _, e := range p.Effects {
		if strings.Contains(e, "kept") && strings.Contains(e, "final name") {
			kept = true
		}
	}
	if !kept {
		t.Errorf("the effects do not say the file at the final name is kept: %q", p.Effects)
	}

	if _, err := fx.svc.RetireArtifact(ctx, fx.artifact, req); err != nil {
		t.Fatalf("RetireArtifact: %v", err)
	}
	if got := stateOf662(t, fx.journal, fx.artifact); got != string(lifecycle.Retired) {
		t.Errorf("%s is %s, want RETIRED", fx.artifact, got)
	}
	if after := fileHash1594(t, fx.final); after != before {
		t.Errorf("retiring changed the copy at %s from %s to %s; it must stay as it was", fx.final, before, after)
	}
	if h := healthOf1594(t, fx); h.State == health.Failing {
		t.Errorf("the set still reads FAILING (%s) after its only failure was retired", h.Reason)
	}
}

// A copy that verifies is a good backup. Retiring it would stop counting a
// restore point, so the acknowledgement does not reach it: the refusal stands
// with no override and points at retry, which keeps it.
func TestAVerifiedOwnCopyIsNeverRetired_1594(t *testing.T) {
	fx := ownCopySourceGone(t, true)
	_, err := fx.svc.RetireArtifact(context.Background(), fx.artifact, RetireRequest{AcceptUnverifiedCopy: true, NoSuccessor: true})
	r, ok := AsRetireRefusal(err)
	if !ok {
		t.Fatalf("RetireArtifact = %v, want a refusal", err)
	}
	if r.Code != RefusalOwnCopy || r.OverridableWith != "" {
		t.Errorf("refusal = %+v, want %s with nothing that overrides it", r, RefusalOwnCopy)
	}
	if !strings.Contains(strings.ToLower(r.Sentence), "retry") {
		t.Errorf("the refusal does not point at retry: %q", r.Sentence)
	}
	if got := stateOf662(t, fx.journal, fx.artifact); got != string(lifecycle.Failed) {
		t.Errorf("%s is %s, want FAILED", fx.artifact, got)
	}
}

// The acknowledgement means "the source is gone and the copy cannot be
// proven". With the source still there (retry can still compare against it)
// or not answering (nobody knows it is gone), it changes nothing.
func TestTheUnverifiedCopyAcknowledgementNeedsTheSourceGone_1594(t *testing.T) {
	cases := []struct {
		name string
		arm  func(fx deadEndFixture)
	}{
		{name: "source still there", arm: func(deadEndFixture) {}},
		{name: "source not answering", arm: func(fx deadEndFixture) {
			fx.tr.listErr = transport.NewError(transport.Transient, "list", errors.New("the host did not answer"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newDeadEndFixture(t, false)
			walkToFailed662(t, fx)
			tc.arm(fx)
			_, err := fx.svc.RetireArtifact(context.Background(), fx.artifact,
				RetireRequest{AcceptUnverifiedCopy: true, AcceptSourcePresent: true, NoSuccessor: true})
			r, ok := AsRetireRefusal(err)
			if !ok {
				t.Fatalf("RetireArtifact = %v, want a refusal", err)
			}
			if r.Code != RefusalOwnCopy || r.OverridableWith != "" {
				t.Errorf("refusal = %+v, want %s with nothing that overrides it", r, RefusalOwnCopy)
			}
			if got := stateOf662(t, fx.journal, fx.artifact); got != string(lifecycle.Failed) {
				t.Errorf("%s is %s, want FAILED", fx.artifact, got)
			}
		})
	}
}

// The cycle never retires this shape on its own, whatever the copy is: the
// judgement that a copy cannot be proven is a person's, and the automatic
// pass must not spend a full read of every such file every cycle either.
func TestTheCycleNeverRetiresAnUnverifiableOwnCopy_1594(t *testing.T) {
	f := newForgeFixture(t)
	failed := f.failWith("db-20261101.dump", transport.NotFound)
	final, err := lifecycle.FinalArtifactPath(f.localDir, failed)
	if err != nil {
		t.Fatalf("FinalArtifactPath: %v", err)
	}
	mustWriteFile(t, final, "a copy that matches nothing recorded")
	if _, err := lifecycle.Advance(context.Background(), lifecycle.Deps{Journal: f.journal, Now: f.svc.Now}, state.Transition{
		Artifact: failed, Key: "fixture:1594-local-path", From: string(lifecycle.Failed), To: string(lifecycle.Failed),
		LocalPath: &final, Hashes: &state.HashUpdate{Hash: sha256OfNothing, Alg: string(transport.SHA256)},
		Detail: "fixture: the row records its own final name and a hash the file does not have",
	}); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	f.backUp("db-20261119.dump")
	f.hostDeletes("db-20261101.dump")
	f.cycle()
	f.cycle()

	if got := f.stateOf(failed); got != string(lifecycle.Failed) {
		t.Errorf("%s is %s after two cycles; only an operator's acknowledgement may retire an own copy, want FAILED", failed, got)
	}
	if _, err := os.Stat(final); err != nil {
		t.Errorf("the copy at %s is gone: %v", final, err)
	}
}
