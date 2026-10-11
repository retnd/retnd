// The recovery-test claim's durable record (EPIC Q, #980): a verdict that is
// only a pass when the bytes matched first, kept apart from the scrub claim,
// with a cleanup failure that can neither fake a failure nor hide behind a
// pass.

package state

import (
	"context"
	"strings"
	"testing"
	"time"
)

var recAt = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func passingTest(a CopyRef) RecoveryTest {
	return RecoveryTest{
		Copy: a, State: RecoveryPassed, At: recAt, NextDue: recAt.Add(24 * time.Hour),
		ValidatorID: "pg-restore-check", ValidatorHash: strings.Repeat("ab", 32),
		DigestMatched: yes(), Termination: RecoveryTerminationExited, CleanupState: RecoveryCleanupRemoved,
		Duration: 2 * time.Second,
	}
}

func copyRef(t *testing.T) (*Journal, CopyRef) {
	j, a := scrubFixture(t)
	return j, CopyRef{Artifact: a, Medium: MediumLocal}
}

func TestRecordRecoveryTest_APassNamesTheDigestGateTheValidatorAndWhatItHashed(t *testing.T) {
	ctx := context.Background()
	j, ref := copyRef(t)
	if err := j.RecordRecoveryTest(ctx, passingTest(ref)); err != nil {
		t.Fatalf("RecordRecoveryTest: %v", err)
	}
	p := localCopy(t, j, ref.Artifact)
	if p.RecoveryState != RecoveryPassed || p.RecoveryTestedAt == nil || !p.RecoveryTestedAt.Equal(recAt) {
		t.Errorf("record = %q / %v", p.RecoveryState, p.RecoveryTestedAt)
	}
	if p.RecoveryDigestMatched == nil || !*p.RecoveryDigestMatched {
		t.Errorf("RecoveryDigestMatched = %v", p.RecoveryDigestMatched)
	}
	if p.RecoveryValidatorID != "pg-restore-check" || len(p.RecoveryValidatorHash) != 64 {
		t.Errorf("validator = %q / %q", p.RecoveryValidatorID, p.RecoveryValidatorHash)
	}
	if p.RecoveryTermination != RecoveryTerminationExited || p.RecoveryCleanupState != RecoveryCleanupRemoved {
		t.Errorf("termination/cleanup = %q/%q", p.RecoveryTermination, p.RecoveryCleanupState)
	}
	if p.RecoveryNextDueAt == nil || !p.RecoveryNextDueAt.Equal(recAt.Add(24*time.Hour)) {
		t.Errorf("RecoveryNextDueAt = %v", p.RecoveryNextDueAt)
	}
}

// The digest gate, enforced where it cannot be forgotten: a pass that was not
// preceded by a digest match is not storable, whoever calls.
func TestRecordRecoveryTest_APassWithoutADigestMatchIsRefused(t *testing.T) {
	ctx := context.Background()
	j, ref := copyRef(t)
	for name, mutate := range map[string]func(*RecoveryTest){
		"digest never compared": func(r *RecoveryTest) { r.DigestMatched = nil },
		"digest did not match":  func(r *RecoveryTest) { r.DigestMatched = no() },
		"no validator hash":     func(r *RecoveryTest) { r.ValidatorHash = "" },
		"no validator id":       func(r *RecoveryTest) { r.ValidatorID = "" },
		"validator was killed":  func(r *RecoveryTest) { r.Termination = RecoveryTerminationKilledGroup },
		"kill unconfirmed":      func(r *RecoveryTest) { r.Termination = RecoveryTerminationUnconfirmed },
		"validator never ran":   func(r *RecoveryTest) { r.Termination = "" },
	} {
		t.Run(name, func(t *testing.T) {
			rt := passingTest(ref)
			mutate(&rt)
			if err := j.RecordRecoveryTest(ctx, rt); err == nil {
				t.Errorf("a pass was recorded with %s", name)
			}
		})
	}
	if p := localCopy(t, j, ref.Artifact); p.RecoveryState != RecoveryUnknown {
		t.Errorf("refused writes changed the record: %q", p.RecoveryState)
	}
}

func TestRecordRecoveryTest_ADigestMismatchIsAFailureThatNeverRanTheValidator(t *testing.T) {
	ctx := context.Background()
	j, ref := copyRef(t)
	if err := j.RecordRecoveryTest(ctx, RecoveryTest{
		Copy: ref, State: RecoveryFailed, At: recAt, NextDue: recAt.Add(time.Hour),
		DigestMatched: no(), Reason: "recovered bytes hash to something else", CleanupState: RecoveryCleanupRemoved,
	}); err != nil {
		t.Fatalf("RecordRecoveryTest: %v", err)
	}
	p := localCopy(t, j, ref.Artifact)
	if p.RecoveryState != RecoveryFailed || p.RecoveryDigestMatched == nil || *p.RecoveryDigestMatched {
		t.Errorf("record = %q / %v", p.RecoveryState, p.RecoveryDigestMatched)
	}
	if p.RecoveryValidatorID != "" || p.RecoveryTermination != "" {
		t.Errorf("a validator identity was recorded for a validator that never ran: %q/%q", p.RecoveryValidatorID, p.RecoveryTermination)
	}
}

// The headline composite: the bytes matched, the validator said no. It reads
// Content verified / Recovery test failed, and the two claims stay apart.
func TestRecordRecoveryTest_AFailedValidatorLeavesTheContentClaimStanding(t *testing.T) {
	ctx := context.Background()
	j, ref := copyRef(t)
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: ref.Artifact, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationContent,
		At: recAt.Add(-time.Hour), NextDue: recAt.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordRecoveryTest(ctx, RecoveryTest{
		Copy: ref, State: RecoveryFailed, At: recAt, NextDue: recAt.Add(time.Hour),
		ValidatorID: "pg-restore-check", ValidatorHash: strings.Repeat("cd", 32),
		DigestMatched: yes(), Termination: RecoveryTerminationExited, Reason: "validator exited 3",
		CleanupState: RecoveryCleanupRemoved,
	}); err != nil {
		t.Fatalf("RecordRecoveryTest: %v", err)
	}
	p := localCopy(t, j, ref.Artifact)
	if p.AssuranceState != AssurancePassed || p.ContentVerifiedAt == nil {
		t.Errorf("a failed recovery test erased the content claim: %q / %v", p.AssuranceState, p.ContentVerifiedAt)
	}
	if p.RecoveryState != RecoveryFailed {
		t.Errorf("RecoveryState = %q", p.RecoveryState)
	}
	if p.RecoveryDigestMatched == nil || !*p.RecoveryDigestMatched {
		t.Error("the digest match that preceded the validator was not recorded")
	}
}

func TestRecordRecoveryTest_AScrubNeverTouchesTheRecoveryClaim(t *testing.T) {
	ctx := context.Background()
	j, ref := copyRef(t)
	if err := j.RecordRecoveryTest(ctx, passingTest(ref)); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: ref.Artifact, Medium: MediumLocal, Verdict: ScrubFailed, Class: VerificationContent,
		At: recAt.Add(time.Hour), NextDue: recAt.Add(2 * time.Hour), Reason: "bytes changed",
	}); err != nil {
		t.Fatal(err)
	}
	if p := localCopy(t, j, ref.Artifact); p.RecoveryState != RecoveryPassed || p.RecoveryTestedAt == nil {
		t.Errorf("a failed scrub rewrote the recovery claim: %q / %v (history is not rewritten, it ages)", p.RecoveryState, p.RecoveryTestedAt)
	}
}

// A cleanup failure is its own state: it does not turn a pass into a failure
// and a failure does not hide in it.
func TestRecordRecoveryTest_ACleanupFailureDoesNotChangeTheVerdictEitherWay(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		build func(CopyRef) RecoveryTest
		want  string
	}{
		{"pass stays pass", func(r CopyRef) RecoveryTest {
			rt := passingTest(r)
			rt.CleanupState = RecoveryCleanupFailed
			return rt
		}, RecoveryPassed},
		{"fail stays fail", func(r CopyRef) RecoveryTest {
			return RecoveryTest{Copy: r, State: RecoveryFailed, At: recAt, NextDue: recAt, DigestMatched: no(), CleanupState: RecoveryCleanupFailed}
		}, RecoveryFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, ref := copyRef(t)
			if err := j.RecordRecoveryTest(ctx, tc.build(ref)); err != nil {
				t.Fatal(err)
			}
			p := localCopy(t, j, ref.Artifact)
			if p.RecoveryState != tc.want || p.RecoveryCleanupState != RecoveryCleanupFailed {
				t.Errorf("state/cleanup = %q/%q, want %q/failed", p.RecoveryState, p.RecoveryCleanupState, tc.want)
			}
		})
	}
}

func TestRecordRecoveryCleanup_ALaterSweepCanSettleCleanupWithoutTouchingTheVerdict(t *testing.T) {
	ctx := context.Background()
	j, ref := copyRef(t)
	rt := passingTest(ref)
	rt.CleanupState = RecoveryCleanupFailed
	if err := j.RecordRecoveryTest(ctx, rt); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordRecoveryCleanup(ctx, ref, RecoveryCleanupRemoved); err != nil {
		t.Fatalf("RecordRecoveryCleanup: %v", err)
	}
	p := localCopy(t, j, ref.Artifact)
	if p.RecoveryCleanupState != RecoveryCleanupRemoved || p.RecoveryState != RecoveryPassed {
		t.Errorf("cleanup/state = %q/%q", p.RecoveryCleanupState, p.RecoveryState)
	}
	if err := j.RecordRecoveryCleanup(ctx, ref, "maybe"); err == nil {
		t.Error("an unknown cleanup state was accepted")
	}
}

func TestRecordRecoveryTest_RefusedAndIncompleteCarryNoPassEvidence(t *testing.T) {
	ctx := context.Background()
	for _, st := range []string{RecoveryRefusedForCost, RecoveryCouldNotComplete} {
		t.Run(st, func(t *testing.T) {
			j, ref := copyRef(t)
			if err := j.RecordRecoveryTest(ctx, RecoveryTest{Copy: ref, State: st, At: recAt, NextDue: recAt.Add(time.Hour), Reason: "why"}); err != nil {
				t.Fatalf("RecordRecoveryTest: %v", err)
			}
			p := localCopy(t, j, ref.Artifact)
			if p.RecoveryState != st {
				t.Errorf("state = %q", p.RecoveryState)
			}
			if p.RecoveryDigestMatched != nil {
				t.Errorf("a %s test carries a digest verdict: %v", st, *p.RecoveryDigestMatched)
			}
			bad := RecoveryTest{Copy: ref, State: st, At: recAt, NextDue: recAt, DigestMatched: yes()}
			if err := j.RecordRecoveryTest(ctx, bad); err == nil {
				t.Errorf("a %s result claimed a digest match", st)
			}
		})
	}
}

func TestRecordRecoveryTest_OnlyTheRecordedVerdictsAreWritable(t *testing.T) {
	ctx := context.Background()
	j, ref := copyRef(t)
	for _, st := range []string{RecoveryUnknown, RecoveryNotConfigured, RecoveryNeverRun, RecoveryOverdue, "green", ""} {
		if err := j.RecordRecoveryTest(ctx, RecoveryTest{Copy: ref, State: st, At: recAt, NextDue: recAt}); err == nil {
			t.Errorf("%q was accepted as a recorded test result", st)
		}
	}
}

func TestRecordRecoveryTest_TheReasonAndTheValidatorIDAreBounded(t *testing.T) {
	ctx := context.Background()
	j, ref := copyRef(t)
	rt := passingTest(ref)
	rt.ValidatorID = "id\x1b[31m\x00" + strings.Repeat("v", 300)
	if err := j.RecordRecoveryTest(ctx, rt); err == nil {
		t.Error("a validator id with control bytes and 300 characters was accepted: an identity is a short printable name")
	}
	rt = passingTest(ref)
	if err := j.RecordRecoveryTest(ctx, RecoveryTest{Copy: ref, State: RecoveryFailed, At: recAt, NextDue: recAt, DigestMatched: no(),
		Reason: "x\x1b]0;t\x07" + strings.Repeat("y", 4000)}); err != nil {
		t.Fatal(err)
	}
	reason := localCopy(t, j, ref.Artifact).RecoveryFailureReason
	if len(reason) > MaxScrubReasonBytes {
		t.Errorf("reason is %d bytes", len(reason))
	}
	for _, r := range reason {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("control byte %#x in %q", r, reason)
		}
	}
}

func TestMarkRecoveryOverdue_OnlyAPassPastItsCadenceBecomesOverdue(t *testing.T) {
	ctx := context.Background()
	j, ref := copyRef(t)
	if err := j.RecordRecoveryTest(ctx, passingTest(ref)); err != nil {
		t.Fatal(err)
	}
	if n, err := j.MarkRecoveryOverdue(ctx, recAt.Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("MarkRecoveryOverdue inside the cadence = %d, %v", n, err)
	}
	n, err := j.MarkRecoveryOverdue(ctx, recAt.Add(25*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("MarkRecoveryOverdue past the cadence = %d, %v", n, err)
	}
	p := localCopy(t, j, ref.Artifact)
	if p.RecoveryState != RecoveryOverdue {
		t.Errorf("state = %q", p.RecoveryState)
	}
	if p.RecoveryTestedAt == nil || !p.RecoveryTestedAt.Equal(recAt) || p.RecoveryValidatorID == "" {
		t.Errorf("overdue erased the history of the last pass: %v / %q", p.RecoveryTestedAt, p.RecoveryValidatorID)
	}
}

func TestRecordRecoveryEvaluation_NotConfiguredAndNeverRunAreNotPassesAndNotFailures(t *testing.T) {
	ctx := context.Background()
	j, ref := copyRef(t)
	for _, st := range []string{RecoveryNotConfigured, RecoveryNeverRun} {
		if err := j.RecordRecoveryEvaluation(ctx, ref, st); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		p := localCopy(t, j, ref.Artifact)
		if p.RecoveryState != st || p.RecoveryTestedAt != nil || p.RecoveryDigestMatched != nil {
			t.Errorf("%s left %q / %v / %v", st, p.RecoveryState, p.RecoveryTestedAt, p.RecoveryDigestMatched)
		}
	}
	if err := j.RecordRecoveryTest(ctx, passingTest(ref)); err != nil {
		t.Fatal(err)
	}
	// A pass is history: re-evaluating never_run over it would erase it.
	if err := j.RecordRecoveryEvaluation(ctx, ref, RecoveryNeverRun); err == nil {
		t.Error("an evaluation overwrote a recorded pass")
	}
	if err := j.RecordRecoveryEvaluation(ctx, ref, RecoveryPassed); err == nil {
		t.Error("an evaluation was allowed to say passed")
	}
}
