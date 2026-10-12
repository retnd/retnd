package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/reconcile"
	"github.com/retnd/retnd/core/internal/recovery/budget"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// EPIC Q, #983: the proofs that sit on top of the recovery engine, the scrub
// driver and the operation registry together. Each one names the promise it
// holds and what the planted violation in scripts/bdtools/recovery/selftest.py
// does to it.

// --- the provider's checksum disagrees, and the finding is not a warning ---

// A destination that attests a SHA-256 different from the recorded one has
// said, positively, that the copy is not the bytes we ingested. Refusing the
// one recovery is not enough: a refusal that lives in a response body is a
// soft warning, gone with the request. The finding has to be written on the
// copy (so the settings card, the activity feed and the next scrub read it
// back), and it has to reach internal/reconcile, which is where every other
// positive finding about a retained copy is routed, so that the pass that
// reports on the set says "needs investigation" instead of "left on a medium,
// no action".
func TestAProviderChecksumMismatchIsRecordedOnTheCopyAndReconcileFlagsIt(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{})
	a := seedComplete(t, f, "mismatch.dump", "warm")
	f.store.attest = func() (transport.ChecksumAttestation, error) {
		return transport.ChecksumAttestation{Algorithm: transport.SHA256, Value: strings.Repeat("0", 64)}, nil
	}

	before := claimOf(t, f, a, "warm")
	if before.AssuranceState == state.AssuranceFailed {
		t.Fatalf("the copy was already failed before the recovery asked: %+v", before.Assurance)
	}

	_, _, err := f.b.RecoverNow(ctx, f.session(), RecoveryRequest{ArtifactID: a.String(), Target: stagingTo("x"), AcknowledgeCost: true}, nil)
	if err == nil || codeOf(t, err) != RecoveryRemoteCopyCorrupt {
		t.Fatalf("want %s, got %v", RecoveryRemoteCopyCorrupt, err)
	}
	if f.store.opens.Load() != 0 {
		t.Fatal("the copy was downloaded although the provider had already said it is not the recorded bytes")
	}

	got := claimOf(t, f, a, "warm")
	if got.AssuranceState != state.AssuranceFailed {
		t.Fatalf("the copy's scrub claim is %q after a positive finding of corruption, want %q", got.AssuranceState, state.AssuranceFailed)
	}
	if got.LastFailureAt == nil || !strings.Contains(strings.ToLower(got.LastFailureReason), "checksum") {
		t.Errorf("the failure carries no reason an operator can act on: at=%v reason=%q", got.LastFailureAt, got.LastFailureReason)
	}
	if got.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1", got.ConsecutiveFailures)
	}
	if got.LastCheckedClass != state.VerificationAttested {
		t.Errorf("LastCheckedClass = %q: the check that ran was the provider's attestation", got.LastCheckedClass)
	}
	if !equalTimePtr(got.ContentVerifiedAt, before.ContentVerifiedAt) {
		t.Errorf("a failed attestation moved the content-verified age from %v to %v", before.ContentVerifiedAt, got.ContentVerifiedAt)
	}
	if got.NextDueAt == nil || got.NextDueAt.After(f.clock.Now().Add(time.Hour)) {
		t.Errorf("a copy with a positive finding is owed a re-check soon, NextDueAt = %v", got.NextDueAt)
	}

	// The same finding, read by the pass that reports on the set.
	rep, err := reconcile.Reconcile(ctx, reconcile.Deps{Journal: f.b.journal, Transport: f.store}, transport.Source{}, setID(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 0 {
		t.Fatalf("reconcile errors: %v", rep.Errors)
	}
	var found bool
	for _, fd := range rep.Findings {
		if fd.Artifact != a {
			continue
		}
		found = true
		if !fd.NeedsInvestigation {
			t.Errorf("reconcile reports %s as %q and does not flag it: a copy a provider says is corrupt is a finding, not a no-action", a, fd.Reason)
		}
		if !strings.Contains(fd.Reason, "warm") {
			t.Errorf("the reason does not name the copy: %q", fd.Reason)
		}
		if fd.Changed() {
			t.Errorf("reconcile moved %s from %s to %s: it must not decide the artifact's fate from one copy", a, fd.From, fd.To)
		}
	}
	if !found {
		t.Fatalf("reconcile never reported %s", a)
	}
}

func TestAProviderThatAgreesLeavesTheCopysClaimAloneAndReconcileQuiet(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{})
	a := seedComplete(t, f, "agrees.dump", "warm")
	f.store.attest = func() (transport.ChecksumAttestation, error) {
		return transport.ChecksumAttestation{Algorithm: transport.SHA256, Value: sha(f.data)}, nil
	}
	if _, _, err := f.b.RecoverNow(ctx, f.session(), RecoveryRequest{ArtifactID: a.String(), Target: stagingTo("x"), AcknowledgeCost: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := claimOf(t, f, a, "warm"); got.AssuranceState == state.AssuranceFailed {
		t.Fatalf("a provider that agreed left the copy %q", got.AssuranceState)
	}
	rep, err := reconcile.Reconcile(ctx, reconcile.Deps{Journal: f.b.journal, Transport: f.store}, transport.Source{}, setID(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range rep.Findings {
		if fd.NeedsInvestigation {
			t.Errorf("reconcile flagged %s with nothing wrong: %s", fd.Artifact, fd.Reason)
		}
	}
}

func equalTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// --- two concurrent first downloads cost one whole-copy read ---

// The preparation read of a download is the one full read it can cost, and
// on a billed medium it is a second bill. Two operators who press download on
// the same artifact in the same second, before either has finished the first
// read, must share it: one read of the whole copy, two streams.
func TestTwoConcurrentFirstDownloadsCauseExactlyOneWholeCopyPreflightRead(t *testing.T) {
	smallChunks(t)
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	f.store.rangeGate = make(chan struct{})
	ctx := context.Background()
	alice := RecoverySession{Actor: "alice", Expires: f.clock.Now().Add(time.Hour)}
	bob := RecoverySession{Actor: "bob", Expires: f.clock.Now().Add(time.Hour)}
	mint := func(s RecoverySession) string {
		g, err := f.b.MintRecovery(ctx, s, RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}, AcknowledgeCost: true})
		if err != nil {
			t.Fatal(err)
		}
		return g.Token
	}
	ta, tb := mint(alice), mint(bob)

	type out struct {
		d   RecoveryStream
		err error
	}
	ra, rb := make(chan out, 1), make(chan out, 1)
	go func() { d, _, err := f.b.RedeemRecovery(ctx, alice, ta); ra <- out{d, err} }()
	for f.store.rangeOpens.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	go func() { d, _, err := f.b.RedeemRecovery(ctx, bob, tb); rb <- out{d, err} }()
	// Give the second request every chance to start a read of its own.
	time.Sleep(300 * time.Millisecond)
	if n := f.store.rangeOpens.Load(); n != 1 {
		t.Fatalf("%d reads of the copy are open while the first is still preparing: the second request did not share it", n)
	}
	close(f.store.rangeGate)

	a, b := <-ra, <-rb
	if a.err != nil || b.err != nil {
		t.Fatal(a.err, b.err)
	}
	defer a.d.Close()
	defer b.d.Close()
	if n := f.store.rangeOpens.Load() + f.store.opens.Load(); n != 1 {
		t.Fatalf("two concurrent first downloads read the copy %d times, want exactly one whole-copy pre-flight read", n)
	}
	if n := f.store.rangeBytes.Load(); n != int64(len(f.data)) {
		t.Errorf("the pre-flight read took %d bytes for a %d byte copy: one whole read, not a half and not two", n, len(f.data))
	}
	if a.d.Length() != int64(len(f.data)) || b.d.Length() != int64(len(f.data)) {
		t.Errorf("lengths %d and %d, want %d for both", a.d.Length(), b.d.Length(), len(f.data))
	}
}

// --- the default deployment's refusals ---

// A deployment that sets nothing runs under budget.Defaults. The tests that
// drive a ceiling elsewhere in this package lower it first (a ceiling of 1 is
// quicker to reach), which proves the mechanism and not the number a default
// install ships with. These two take the install as it comes.
func TestADefaultDeploymentRefusesTheFifthConcurrentDownload(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	if f.b.recovery.limits != (budget.Limits{}) {
		t.Fatalf("this fixture is not a default deployment: limits = %+v", f.b.recovery.limits)
	}
	if budget.DefaultMaxDownloadStreams != 4 {
		t.Fatalf("the default ceiling is %d; the document that names it says 4", budget.DefaultMaxDownloadStreams)
	}
	sess := f.session()
	var held []RecoveryStream
	for i := 0; i < budget.DefaultMaxDownloadStreams; i++ {
		g, err := f.b.MintRecovery(ctx, sess, RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}})
		if err != nil {
			t.Fatal(err)
		}
		d, _, err := f.b.RedeemRecovery(ctx, sess, g.Token)
		if err != nil {
			t.Fatalf("download %d of %d was refused under the default ceiling: %v", i+1, budget.DefaultMaxDownloadStreams, err)
		}
		held = append(held, d)
	}
	g, err := f.b.MintRecovery(ctx, sess, RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.b.RedeemRecovery(ctx, sess, g.Token)
	if err == nil || codeOf(t, err) != RecoveryDownloadsBusy {
		t.Fatalf("the fifth stream on a default install: want %s, got %v", RecoveryDownloadsBusy, err)
	}
	if code := budget.ExitCode(budget.DownloadStreams); code != 36 {
		t.Errorf("the refusal's exit code is %d", code)
	}
	// Finishing one frees exactly one slot.
	held[0].Close()
	held[0].Served(0, held[0].Length()-1, held[0].Length(), nil)
	d, _, err := f.b.RedeemRecovery(ctx, sess, g.Token)
	if err != nil {
		t.Fatalf("a finished download did not give its slot back: %v", err)
	}
	d.Close()
	for _, h := range held[1:] {
		h.Close()
	}
}

func TestADefaultDeploymentRefusesARecoveryThatWouldLeaveLessThanTheStagingReserve(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	if f.b.recovery.limits != (budget.Limits{}) {
		t.Fatalf("this fixture is not a default deployment: limits = %+v", f.b.recovery.limits)
	}
	need := int64(len(f.data))
	h := f.b.recoveryHost()
	req := RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}

	// One byte short of the reserve plus what is about to be staged.
	h.staging.FreeBytes = func(string) (int64, error) { return budget.DefaultStagingReserveBytes + need - 1, nil }
	_, _, err := f.b.RecoverNow(ctx, f.session(), req, nil)
	if err == nil || codeOf(t, err) != RecoveryBelowReserve {
		t.Fatalf("one byte under the default reserve: want %s, got %v", RecoveryBelowReserve, err)
	}
	if f.store.opens.Load() != 0 {
		t.Fatal("a refusal for want of room still read (and billed) the copy")
	}
	stagingIsEmpty(t, f)

	// Exactly enough room is not a refusal: the line is the reserve, not
	// some margin beside it.
	h.staging.FreeBytes = func(string) (int64, error) { return budget.DefaultStagingReserveBytes + need, nil }
	if _, _, err := f.b.RecoverNow(ctx, f.session(), req, nil); err != nil {
		t.Fatalf("exactly the reserve plus the artifact was refused: %v", err)
	}
	if code := budget.ExitCode(budget.StagingReserve); code != 18 {
		t.Errorf("the refusal's exit code is %d", code)
	}
}

// --- cancellation through the operation registry, stage by stage ---

// stageCase is one place a recovery test can be standing when an operator
// presses cancel. Every one starts a real test under an operation id and
// stops it with CancelAssuranceOperation, the call POST
// /assurance/operations/{op}/cancel and `retnd assurance cancel` make.
//
// There is no thaw row. A recovery test never starts a provider restore (an
// archive-class copy is refused for cost and recorded as could-not-complete),
// so a cancel cannot find one in flight. The nearest stage is the probe of the
// provider, a metadata call made before a byte is read, and that is the row
// that stands in its place; the thaw of an operator's recovery is cancelled
// by TestCancellationDuringTheThawLeavesNothingAndStartsNoGrant. Decrypt
// arrives with EPIC N.
type stageCase struct {
	name string
	// arm prepares the fixture and returns the channel closed when the test
	// is standing at the stage, and a check that the stage really holds
	// something (the positive control) before the cancel.
	arm func(t *testing.T, f *recoveryFixture) (at <-chan struct{}, holding func() string)
}

func TestCancellingARecoveryTestAtEachStageLeavesNothingBehind(t *testing.T) {
	cases := []stageCase{
		{"the provider probe", func(t *testing.T, f *recoveryFixture) (<-chan struct{}, func() string) {
			f.store.probeGate = make(chan struct{})
			f.store.probeEntered = make(chan struct{})
			return f.store.probeEntered, func() string { return "" }
		}},
		{"the read", func(t *testing.T, f *recoveryFixture) (<-chan struct{}, func() string) {
			f.store.openGate = make(chan struct{})
			entered := make(chan struct{})
			go func() {
				for f.store.opens.Load() == 0 {
					time.Sleep(5 * time.Millisecond)
				}
				close(entered)
			}()
			return entered, func() string { return "" }
		}},
		{"the write to staging", func(t *testing.T, f *recoveryFixture) (<-chan struct{}, func() string) {
			f.store.stallAfter = 64 << 10
			f.store.stalled = make(chan struct{})
			return f.store.stalled, func() string {
				// A partial file is on disk while the write is stalled, or the
				// test would be proving that nothing was cleaned up that was
				// never there.
				if len(f.stagingEntries(t)) == 0 {
					return "no partial file in staging while the write was in flight"
				}
				return ""
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
			stages := &stubStages{script: "pg-check.local.sh"}
			withStages(f, stages)
			a := seedComplete(t, f, "stage.dump", "warm")
			f.store.attest = func() (transport.ChecksumAttestation, error) {
				return transport.ChecksumAttestation{Algorithm: transport.SHA256, Value: sha(f.data)}, nil
			}
			at, holding := tc.arm(t, f)

			started, err := f.b.StartRecoveryTest(ctx, "production/postgres", RecoveryTestOptions{Actor: "alice", ArtifactID: a.String(), AcknowledgeCost: true})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-at:
			case <-time.After(20 * time.Second):
				t.Fatalf("the test never reached %s", tc.name)
			}
			if why := holding(); why != "" {
				t.Fatal(why)
			}

			got, err := f.b.CancelAssuranceOperation(ctx, started.OperationID)
			if err != nil {
				t.Fatalf("cancel: %v", err)
			}
			if got.OperationID != started.OperationID || got.Status != "cancelling" {
				t.Errorf("answer = %+v", got)
			}
			idle(t, f)

			if e := f.stagingEntries(t); len(e) != 0 {
				t.Errorf("a test cancelled at %s left %v in staging", tc.name, e)
			}
			if stages.ran() != 0 {
				t.Errorf("the validator ran %d times for a test cancelled at %s", stages.ran(), tc.name)
			}
			claim := claimOf(t, f, a, "warm")
			if claim.RecoveryState == state.RecoveryPassed || claim.RecoveryState == state.RecoveryFailed {
				t.Errorf("a test cancelled at %s recorded a verdict (%q) about a copy it never finished testing", tc.name, claim.RecoveryState)
			}
			if claim.AssuranceState == state.AssuranceFailed {
				t.Errorf("a cancel at %s was recorded as a failure of the copy", tc.name)
			}
			end := f.eventsNamed(t, obs.EventRecoveryTestEnd)
			if len(end) != 1 || end[0]["op_id"] != started.OperationID || end[0]["result"] == "success" {
				t.Errorf("end lines = %v, want one for the cancelled operation that is not a success", end)
			}
			if _, err := f.b.CancelAssuranceOperation(ctx, started.OperationID); !errors.Is(err, ErrOperationNotFound) {
				t.Errorf("the operation is still registered after it ended: %v", err)
			}
		})
	}
}

// The fourth stage, the validator, is TestCancelDuringTheValidatorStageKillsTheWholeProcessGroup
// in assuranceoperations_test.go: a real script that starts a child and waits,
// cancelled through the same registry call, with the whole process group
// checked for survivors. It is named here so the four stages can be read in
// one place.

// --- two operators, one test ---

// Two operators who ask for the same recovery test while it is running get
// one execution and a receipt each. The receipts are what each of them is
// shown: whose request it was, which operation answered it, and whether it
// started the work or joined it.
func TestTwoOperatorsOnTheSameRecoveryTestGetOneExecutionAndAReceiptEach(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	stages := &stubStages{script: "pg-check.local.sh", gate: make(chan struct{})}
	withStages(f, stages)
	a := seedComplete(t, f, "shared.dump", state.MediumLocal)

	opts := func(actor string) RecoveryTestOptions { return RecoveryTestOptions{Actor: actor, ArtifactID: a.String()} }
	first, err := f.b.StartRecoveryTest(ctx, "production/postgres", opts("alice"))
	if err != nil {
		t.Fatal(err)
	}
	waitRan(t, stages)

	var wg sync.WaitGroup
	joined := make([]RecoveryTestStarted, 3)
	errs := make([]error, 3)
	for i, who := range []string{"bob", "carol", "dave"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			joined[i], errs[i] = f.b.StartRecoveryTest(ctx, "production/postgres", opts(who))
		}()
	}
	wg.Wait()
	for i, st := range joined {
		if errs[i] != nil {
			t.Fatalf("operator %d: %v", i, errs[i])
		}
		if st.OperationID != first.OperationID || !st.Coalesced {
			t.Errorf("operator %d got %+v, want to join %s", i, st, first.OperationID)
		}
	}
	// Asking for something else while it runs is refused: a second read of a
	// copy that may be billed.
	if _, err := f.b.StartRecoveryTest(ctx, "production/postgres", RecoveryTestOptions{Actor: "erin"}); !errors.Is(err, ErrRecoveryTestRunning) {
		t.Errorf("a different request while one runs: err = %v, want ErrRecoveryTestRunning", err)
	}

	close(stages.gate)
	idle(t, f)

	if n := stages.ran(); n != 1 {
		t.Errorf("the validator ran %d times for four operators, want exactly one execution", n)
	}
	end := f.eventsNamed(t, obs.EventRecoveryTestEnd)
	if len(end) != 1 {
		t.Errorf("%d end lines for one execution", len(end))
	}
	receipts, err := f.b.journal.RecoveryReceipts(ctx, first.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]state.RecoveryReceipt{}
	for _, r := range receipts {
		by[r.RequestedBy] = r
	}
	for _, who := range []string{"alice", "bob", "carol", "dave"} {
		r, ok := by[who]
		if !ok {
			t.Errorf("%s has no receipt", who)
			continue
		}
		if r.OperationID != first.OperationID {
			t.Errorf("%s's receipt names operation %q, want %q", who, r.OperationID, first.OperationID)
		}
		if r.Coalesced != (who != "alice") {
			t.Errorf("%s's receipt: Coalesced = %v", who, r.Coalesced)
		}
	}
	if _, ok := by["erin"]; ok {
		t.Error("the refused request left a receipt as though it had been served")
	}
	if len(receipts) != 4 {
		t.Errorf("%d receipts for four served requests", len(receipts))
	}
}
