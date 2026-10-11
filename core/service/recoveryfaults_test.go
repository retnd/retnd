package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/recovery/budget"
	"github.com/retnd/retnd/core/internal/transport"
)

// Fault injection for artifact recovery (EPIC Q, #983). The scenarios the
// issue lists are tests here, each against the real service with a double
// that fails the way the real thing fails, and each asserting what was
// LEFT: a staging entry, a half-written target, a provider job, a second
// read. docs/conformance/epic-q-faults.md maps every scenario to its test
// and to the mutation that turns it red.
//
// The scenarios that need machinery this change does not own (a validator
// that hangs, double-forks or floods, hostile control sequences, and the
// decrypt stage) belong to the recovery-test runner and to EPIC N, and are
// named in that document as such rather than skipped here.

// mustBeInterrupted asserts a cancelled stage came back as an interruption,
// and says which stage in the message, because a recovery that went ahead
// after being cancelled is the failure this is for.
func mustBeInterrupted(t *testing.T, stage string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("cancellation during %s was ignored: the recovery went ahead anyway", stage)
	}
	if got := codeOf(t, err); got != RecoveryInterrupted {
		t.Fatalf("cancellation during %s: want %s, got %s (%v)", stage, RecoveryInterrupted, got, err)
	}
}

func stagingIsEmpty(t *testing.T, f *recoveryFixture) {
	t.Helper()
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("staging not empty: %v", e)
	}
}

func TestCancellationDuringTheThawLeavesNothingAndStartsNoGrant(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "cold"})
	f.b.recovery.thaw = func(ctx context.Context, _, _, _ string) (RecoveryThaw, error) {
		<-ctx.Done()
		return RecoveryThaw{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	g, err := f.b.MintRecovery(ctx, f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true})
	mustBeInterrupted(t, "the thaw", err)
	if g.Token != "" {
		t.Fatalf("a grant was minted: %+v", g)
	}
	stagingIsEmpty(t, f)
}

func TestCancellationDuringTheReadLeavesNothingAndNoTarget(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	f.store.openGate = make(chan struct{}) // never opened: the read is in flight until cancelled
	g, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x/y"), AcknowledgeCost: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	_, _, err = f.b.RedeemRecovery(ctx, f.session(), g.Token)
	mustBeInterrupted(t, "the read", err)
	stagingIsEmpty(t, f)
}

func TestCancellationBetweenVerifyingAndPlacingLeavesNoOutputAndNoPartial(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	ctx, cancel := context.WithCancel(context.Background())
	f.b.recovery.afterStage = cancel
	g, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x")})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.b.RedeemRecovery(ctx, f.session(), g.Token)
	mustBeInterrupted(t, "the gap between verifying and placing", err)
	stagingIsEmpty(t, f)
}

func TestCancellationDuringTheUploadLeavesNoPartialAndNoObject(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	f.store.uploadGate = make(chan struct{}) // the upload hangs until cancelled
	req := RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "storage", Medium: "other", Key: "recovered/a"}}
	g, err := f.b.MintRecovery(context.Background(), f.session(), req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	_, _, err = f.b.RedeemRecovery(ctx, f.session(), g.Token)
	mustBeInterrupted(t, "the upload", err)
	stagingIsEmpty(t, f)
	if _, ok := f.store.objects["recovered/a"]; ok {
		t.Fatal("a cancelled upload left an object behind")
	}
}

func TestADestinationThatDisappearsMidReadFailsCleanlyAndSaysWhy(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	f.store.failAfter = len(f.data) / 3
	f.store.failErr = transport.NewError(transport.NotFound, "read", errors.New("NoSuchKey: the object is gone"))
	_, _, err := f.b.RecoverNow(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil)
	if err == nil || codeOf(t, err) != RecoveryNotReadable {
		t.Fatalf("a destination that loses the object mid-read: want %s, got %v", RecoveryNotReadable, err)
	}
	if strings.Contains(err.Error(), "NoSuchKey") {
		t.Fatalf("the provider's error text reached the operator: %v", err)
	}
	stagingIsEmpty(t, f)
}

func TestADestinationThatFailsMidReadForAnyOtherReasonIsAFailureNotAnAccusation(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	f.store.failAfter = len(f.data) / 2
	f.store.failErr = errors.New("connection reset by peer 203.0.113.9:443")
	_, _, err := f.b.RecoverNow(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil)
	if codeOf(t, err) != RecoveryFailed || strings.Contains(err.Error(), "203.0.113.9") {
		t.Fatalf("got %v", err)
	}
	stagingIsEmpty(t, f)
}

func TestAProcessKilledMidWriteLeavesAPartialTheNextStartReconcilesAndReports(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	h := f.b.recoveryHost()
	file, err := h.staging.Begin(1000) // the dying process had this open
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(bytes.Repeat([]byte("x"), 500)); err != nil {
		t.Fatal(err)
	}
	// No Close, no cleanup: the process is gone.
	removed, failed, err := f.b.ReconcileRecoveryStaging(context.Background())
	if err != nil || removed != 1 || failed != 0 {
		t.Fatalf("reconcile = %d removed, %d failed, %v", removed, failed, err)
	}
	if !strings.Contains(f.log.String(), "removed 1 leftover") {
		t.Fatal("the reconcile did not report what it removed")
	}
	stagingIsEmpty(t, f)
}

func TestTwoOperatorsRequestingTheSameRecoveryProduceOneReadAndOneReceiptEach(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	f.store.openGate = make(chan struct{})
	alice := RecoverySession{Actor: "alice", Expires: f.clock.Now().Add(time.Hour)}
	bob := RecoverySession{Actor: "bob", Expires: f.clock.Now().Add(time.Hour)}

	mint := func(s RecoverySession, name string) string {
		g, err := f.b.MintRecovery(context.Background(), s, RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo(name), AcknowledgeCost: true})
		if err != nil {
			t.Fatal(err)
		}
		return g.Token
	}
	ta, tb := mint(alice, "a/out"), mint(bob, "b/out")

	type out struct {
		res *RecoveryResult
		err error
	}
	ra, rb := make(chan out, 1), make(chan out, 1)
	go func() { _, r, err := f.b.RedeemRecovery(context.Background(), alice, ta); ra <- out{r, err} }()
	for f.store.opens.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	go func() { _, r, err := f.b.RedeemRecovery(context.Background(), bob, tb); rb <- out{r, err} }()
	time.Sleep(150 * time.Millisecond)
	close(f.store.openGate)
	a, b := <-ra, <-rb
	if a.err != nil || b.err != nil {
		t.Fatal(a.err, b.err)
	}
	if n := f.store.opens.Load(); n != 1 {
		t.Fatalf("the copy was read %d times for two operators asking at once: each extra read is a second bill", n)
	}
	if a.res.Path == b.res.Path {
		t.Fatal("two operators were given one file")
	}
	for _, r := range []*RecoveryResult{a.res, b.res} {
		got, err := os.ReadFile(r.Path)
		if err != nil || !bytes.Equal(got, f.data) {
			t.Fatalf("%s: %v", r.Path, err)
		}
	}
	// One receipt each: a mint row and a run row per operator, naming them.
	byActor := map[string]int{}
	for _, row := range f.rows(t) {
		if a, _ := row["actor"].(string); a != "" {
			byActor[a]++
		}
	}
	if byActor["alice"] != 2 || byActor["bob"] != 2 {
		t.Fatalf("receipts per operator = %v, want 2 each (a mint and a run)", byActor)
	}
	// Nothing left but the two outputs.
	for _, e := range f.stagingEntries(t) {
		if strings.HasSuffix(e, ".partial") {
			t.Fatalf("a partial survived: %s", e)
		}
	}
}

func TestTheConcurrentReadBudgetRefusesAtTheMomentOfSpendAndReadsNothing(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	f.b.recovery.limits = budget.Limits{MaxConcurrentReads: 1, MaxReadsPerDestination: 1}
	h := f.b.recoveryHost()
	hold, err := h.reads.Acquire("some-other-destination") // the only global slot
	if err != nil {
		t.Fatal(err)
	}
	defer hold()

	_, _, err = f.b.RecoverNow(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil)
	if codeOf(t, err) != RecoveryReadsBusy {
		t.Fatalf("want %s, got %v", RecoveryReadsBusy, err)
	}
	if f.store.opens.Load() != 0 {
		t.Fatal("the budget refused but the copy was read anyway")
	}
	stagingIsEmpty(t, f)
}

func TestThePerDestinationBudgetIsADifferentRefusalFromTheGlobalOne(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	f.b.recovery.limits = budget.Limits{MaxConcurrentReads: 4, MaxReadsPerDestination: 1}
	h := f.b.recoveryHost()
	hold, _ := h.reads.Acquire("warm")
	defer hold()
	_, _, err := f.b.RecoverNow(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil)
	if codeOf(t, err) != RecoveryDestinationBusy {
		t.Fatalf("want %s, got %v", RecoveryDestinationBusy, err)
	}
}

func TestTheDownloadCeilingRefusesTheNextStreamAndFreesWhenOneFinishes(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	f.b.recovery.limits = budget.Limits{MaxDownloadStreams: 1}
	sess := f.session()
	mint := func() string {
		g, err := f.b.MintRecovery(ctx, sess, RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}})
		if err != nil {
			t.Fatal(err)
		}
		return g.Token
	}
	t1, t2, t3 := mint(), mint(), mint()

	d1, _, err := f.b.RedeemRecovery(ctx, sess, t1)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.b.RedeemRecovery(ctx, sess, t2)
	if err == nil || codeOf(t, err) != RecoveryDownloadsBusy {
		t.Fatalf("a second stream over a ceiling of one: %v", err)
	}
	if f.store.opens.Load() != 0 {
		t.Fatal("unexpected remote read")
	}
	// The refused grant is not spent by the refusal: the operator can come
	// back for it.
	d1.Close()
	d1.Served(0, d1.Length()-1, d1.Length(), nil)
	d2, _, err := f.b.RedeemRecovery(ctx, sess, t2)
	if err != nil {
		t.Fatalf("after the first stream finished the refused grant should work: %v", err)
	}
	d2.Close()
	d2.Served(0, d2.Length()-1, d2.Length(), nil)
	d3, _, err := f.b.RedeemRecovery(ctx, sess, t3)
	if err != nil {
		t.Fatal(err)
	}
	d3.Close()
	d3.Served(0, d3.Length()-1, d3.Length(), nil)
}

func TestAnAbandonedDownloadGivesBackItsStreamSlot(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	f.b.recovery.limits = budget.Limits{MaxDownloadStreams: 1}
	sess := f.session()
	g1, _ := f.b.MintRecovery(ctx, sess, RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}})
	d1, _, err := f.b.RedeemRecovery(ctx, sess, g1.Token)
	if err != nil {
		t.Fatal(err)
	}
	d1.Close() // the client went away and never came back
	f.clock.Advance(10 * time.Minute)
	g2, _ := f.b.MintRecovery(ctx, sess, RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}})
	d2, _, err := f.b.RedeemRecovery(ctx, sess, g2.Token)
	if err != nil {
		t.Fatalf("an abandoned stream kept its slot for ever: %v", err)
	}
	d2.Close()
}

func TestAStagingReserveRefusalHasItsOwnCodeAndReadsNothing(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	h := f.b.recoveryHost()
	h.staging.FreeBytes = func(string) (int64, error) { return 100, nil } // far below the reserve
	_, _, err := f.b.RecoverNow(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil)
	if codeOf(t, err) != RecoveryBelowReserve {
		t.Fatalf("want %s, got %v", RecoveryBelowReserve, err)
	}
	if f.store.opens.Load() != 0 {
		t.Fatal("a refusal for lack of room still read (and billed) the copy")
	}
}

func TestAProviderChecksumThatDisagreesIsAFindingBeforeAnyByteIsRead(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	f.store.attest = func() (transport.ChecksumAttestation, error) {
		return transport.ChecksumAttestation{Algorithm: transport.SHA256, Value: strings.Repeat("0", 64)}, nil
	}
	_, _, err := f.b.RecoverNow(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil)
	if err == nil || codeOf(t, err) != RecoveryRemoteCopyCorrupt {
		t.Fatalf("a provider that attests a different checksum is a positive finding of corruption, got %v", err)
	}
	if f.store.opens.Load() != 0 {
		t.Fatal("the copy was downloaded (and billed) although the provider had already said it is not the recorded bytes")
	}
	rows := f.rows(t)
	if last := rows[len(rows)-1]; last["outcome"] != "failed" {
		t.Fatalf("a finding is a failure on the record, never a soft warning: %v", last)
	}
	stagingIsEmpty(t, f)
}

func TestAProviderThatAgreesOrCannotAttestIsReadAsUsual(t *testing.T) {
	for name, attest := range map[string]func(*recoveryFixture) func() (transport.ChecksumAttestation, error){
		"agrees": func(f *recoveryFixture) func() (transport.ChecksumAttestation, error) {
			return func() (transport.ChecksumAttestation, error) {
				return transport.ChecksumAttestation{Algorithm: transport.SHA256, Value: sha(f.data)}, nil
			}
		},
		"cannot attest": func(*recoveryFixture) func() (transport.ChecksumAttestation, error) { return nil },
		"attests another algorithm": func(*recoveryFixture) func() (transport.ChecksumAttestation, error) {
			return func() (transport.ChecksumAttestation, error) {
				return transport.ChecksumAttestation{Algorithm: "md5", Value: "abc"}, nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
			f.store.attest = attest(f)
			if _, _, err := f.b.RecoverNow(context.Background(), f.session(),
				RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil); err != nil {
				t.Fatalf("a checksum that is not a disagreement must not stop the recovery (the bytes are verified on the way out anyway): %v", err)
			}
		})
	}
}

func TestEveryBudgetRefusalMapsToACodeWithItsOwnExitCodeInTheBudgetTable(t *testing.T) {
	cases := map[string]budget.Name{
		RecoveryReadsBusy:       budget.ConcurrentReads,
		RecoveryDestinationBusy: budget.DestinationReads,
		RecoveryDownloadsBusy:   budget.DownloadStreams,
		RecoveryBelowReserve:    budget.StagingReserve,
	}
	for code, name := range cases {
		ref := &budget.Refusal{Budget: name, Limit: 1}
		got, ok := AsRecoveryError(mapRecoveryError(ref))
		if !ok || got.Code != code {
			t.Errorf("a %s refusal maps to %v, want %s", name, got, code)
		}
	}
}

var _ = sync.Mutex{}
var _ = atomic.Int32{}
var _ = filepath.Join

func TestHostLimitsSetBeforeFirstUseAreTheOnesTheGatesEnforce(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	if err := f.b.SetRecoveryLimits(RecoveryLimits{StagingReserveBytes: 5 << 30, MaxConcurrentReads: 2, MaxReadsPerDestination: 1, MaxDownloadStreams: 1}); err != nil {
		t.Fatal(err)
	}
	got := f.b.RecoveryLimits()
	if got.StagingReserveBytes != 5<<30 || got.MaxConcurrentReads != 2 || got.MaxReadsPerDestination != 1 || got.MaxDownloadStreams != 1 {
		t.Fatalf("effective limits = %+v", got)
	}
	h := f.b.recoveryHost()
	if h.staging.Reserve != 5<<30 {
		t.Fatalf("the staging reserve the host asked for did not reach staging: %d", h.staging.Reserve)
	}
	r1, err := h.reads.Acquire("d")
	if err != nil {
		t.Fatal(err)
	}
	defer r1()
	if _, err := h.reads.Acquire("d"); err == nil {
		t.Fatal("the host's per-destination limit of 1 was not applied")
	}
}

func TestZeroHostLimitsTakeTheEnginesDefaults(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	got := f.b.RecoveryLimits()
	d := budget.Defaults()
	if got.StagingReserveBytes != d.StagingReserveBytes || got.MaxConcurrentReads != d.MaxConcurrentReads ||
		got.MaxReadsPerDestination != d.MaxReadsPerDestination || got.MaxDownloadStreams != d.MaxDownloadStreams {
		t.Fatalf("a host with no opinion got %+v, want the defaults %+v", got, d)
	}
}

func TestHostLimitsThatCannotMeanAnythingAreRefused(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	for name, l := range map[string]RecoveryLimits{
		"negative reserve":    {StagingReserveBytes: -1},
		"per dest above all":  {MaxConcurrentReads: 2, MaxReadsPerDestination: 3},
		"lone dest too large": {MaxReadsPerDestination: budget.DefaultMaxConcurrentReads + 1},
	} {
		if err := f.b.SetRecoveryLimits(l); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestHostLimitsCannotChangeOnceRecoveryHasStarted(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	if _, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x")}); err != nil {
		t.Fatal(err)
	}
	if err := f.b.SetRecoveryLimits(RecoveryLimits{MaxDownloadStreams: 9}); err == nil {
		t.Fatal("limits changed under live grants and gates: they must be fixed once recovery has started")
	}
}
