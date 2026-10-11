// The per-copy scrub record: what RecordCopyScrub writes, what it refuses
// to let a weak result do to a strong claim, and the lease that makes two
// scrubbers of one copy impossible (EPIC Q, #978).

package state

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
)

// scrubFixture is one committed artifact with its durable local copy.
func scrubFixture(t *testing.T) (*Journal, model.ArtifactID) {
	t.Helper()
	j, _ := openJournal(t)
	set := testArtifact(t).Set
	driveOneArtifact(t, j, set, "scrub-me.dump", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	artifact, err := model.NewArtifactID(set, "scrub-me.dump")
	if err != nil {
		t.Fatalf("NewArtifactID: %v", err)
	}
	return j, artifact
}

func localCopy(t *testing.T, j *Journal, a model.ArtifactID) Placement {
	t.Helper()
	rec, err := j.Get(context.Background(), a)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	p, ok := rec.LocalPlacement()
	if !ok {
		t.Fatalf("no local placement on %s", a)
	}
	return p
}

var scrubAt = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func TestRecordCopyScrub_AFreshCopyReadsUnknownAndDue(t *testing.T) {
	j, a := scrubFixture(t)
	p := localCopy(t, j, a)

	if p.AssuranceState != AssuranceUnknown {
		t.Errorf("AssuranceState = %q, want %q", p.AssuranceState, AssuranceUnknown)
	}
	if p.NextDueAt != nil {
		t.Errorf("NextDueAt = %v, want nil (due now)", p.NextDueAt)
	}
	if p.ContentVerifiedAt != nil || p.LastCheckedAt != nil {
		t.Errorf("a copy nobody scrubbed carries ages: content=%v checked=%v", p.ContentVerifiedAt, p.LastCheckedAt)
	}
	if p.RecoveryState != RecoveryUnknown {
		t.Errorf("RecoveryState = %q, want %q", p.RecoveryState, RecoveryUnknown)
	}
}

func TestRecordCopyScrub_AContentPassMovesTheContentAge(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	due := scrubAt.Add(24 * time.Hour)
	bytesRead := int64(4096)

	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationContent,
		At: scrubAt, NextDue: due, BytesRead: &bytesRead, Duration: 250 * time.Millisecond,
		Reason: "recomputed hash still matches",
	}); err != nil {
		t.Fatalf("RecordCopyScrub: %v", err)
	}

	p := localCopy(t, j, a)
	if p.AssuranceState != AssurancePassed {
		t.Errorf("AssuranceState = %q, want passed", p.AssuranceState)
	}
	if p.LastCheckedClass != VerificationContent {
		t.Errorf("LastCheckedClass = %q, want content", p.LastCheckedClass)
	}
	if p.ContentVerifiedAt == nil || !p.ContentVerifiedAt.Equal(scrubAt) {
		t.Errorf("ContentVerifiedAt = %v, want %v", p.ContentVerifiedAt, scrubAt)
	}
	if p.NextDueAt == nil || !p.NextDueAt.Equal(due) {
		t.Errorf("NextDueAt = %v, want %v", p.NextDueAt, due)
	}
	if p.LastBytesRead == nil || *p.LastBytesRead != 4096 {
		t.Errorf("LastBytesRead = %v, want 4096", p.LastBytesRead)
	}
	if p.LastDurationMS == nil || *p.LastDurationMS != 250 {
		t.Errorf("LastDurationMS = %v, want 250", p.LastDurationMS)
	}
	if p.VerificationClass != VerificationContent || p.VerifiedAt == nil || !p.VerifiedAt.Equal(scrubAt) {
		t.Errorf("class/verified_at = %q/%v, want content/%v", p.VerificationClass, p.VerifiedAt, scrubAt)
	}
}

// The relabelling hazard the epic names first: a result achieved by HEAD
// must never be able to move the content age, however it is written.
func TestRecordCopyScrub_AnExistencePassCannotMoveTheContentAge(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	earlier := scrubAt.Add(-72 * time.Hour)

	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationContent,
		At: earlier, NextDue: earlier.Add(time.Hour),
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationExistence,
		At: scrubAt, NextDue: scrubAt.Add(time.Hour),
	}); err != nil {
		t.Fatalf("second: %v", err)
	}

	p := localCopy(t, j, a)
	if p.ContentVerifiedAt == nil || !p.ContentVerifiedAt.Equal(earlier) {
		t.Errorf("ContentVerifiedAt = %v, want it left at %v: an existence pass moved the content age", p.ContentVerifiedAt, earlier)
	}
	if p.LastCheckedClass != VerificationExistence {
		t.Errorf("LastCheckedClass = %q, want existence: the record must say what ran last", p.LastCheckedClass)
	}
	if p.VerificationClass != VerificationContent {
		t.Errorf("VerificationClass = %q, want content: a weaker pass must not downgrade the strongest achieved", p.VerificationClass)
	}
	if p.VerifiedAt == nil || !p.VerifiedAt.Equal(earlier) {
		t.Errorf("VerifiedAt = %v, want %v: a weaker pass must not refresh the age of a stronger class", p.VerifiedAt, earlier)
	}
}

func TestRecordCopyScrub_AnExistencePassAloneNeverSetsAContentAge(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationExistence,
		At: scrubAt, NextDue: scrubAt.Add(time.Hour),
	}); err != nil {
		t.Fatalf("RecordCopyScrub: %v", err)
	}
	if p := localCopy(t, j, a); p.ContentVerifiedAt != nil {
		t.Errorf("ContentVerifiedAt = %v after an existence-only pass", p.ContentVerifiedAt)
	}
}

func TestRecordCopyScrub_AFailureClearsTheClaimItContradicts(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationContent,
		At: scrubAt.Add(-time.Hour), NextDue: scrubAt,
	}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubFailed, Class: VerificationContent,
		At: scrubAt, NextDue: scrubAt.Add(time.Hour), Reason: "now hashes to something else",
	}); err != nil {
		t.Fatalf("fail: %v", err)
	}

	p := localCopy(t, j, a)
	if p.AssuranceState != AssuranceFailed {
		t.Errorf("AssuranceState = %q, want failed", p.AssuranceState)
	}
	if p.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1", p.ConsecutiveFailures)
	}
	if p.LastFailureAt == nil || !p.LastFailureAt.Equal(scrubAt) || !strings.Contains(p.LastFailureReason, "something else") {
		t.Errorf("failure record = %v / %q", p.LastFailureAt, p.LastFailureReason)
	}
	if p.ContentVerifiedAt != nil || p.VerificationClass != "" || p.VerifiedAt != nil {
		t.Errorf("a failed content check left the claim standing: content_verified_at=%v class=%q verified_at=%v",
			p.ContentVerifiedAt, p.VerificationClass, p.VerifiedAt)
	}
}

func TestRecordCopyScrub_ConsecutiveFailuresCountAndAPassResetsThem(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	record := func(v ScrubVerdict, class string, n int) {
		t.Helper()
		if err := j.RecordCopyScrub(ctx, CopyScrub{
			Artifact: a, Medium: MediumLocal, Verdict: v, Class: class,
			At: scrubAt.Add(time.Duration(n) * time.Minute), NextDue: scrubAt.Add(time.Duration(n+1) * time.Minute),
			Reason: "r",
		}); err != nil {
			t.Fatalf("record %d: %v", n, err)
		}
	}
	record(ScrubCouldNotComplete, "", 1)
	record(ScrubFailed, VerificationContent, 2)
	record(ScrubCouldNotComplete, "", 3)
	if got := localCopy(t, j, a).ConsecutiveFailures; got != 3 {
		t.Fatalf("ConsecutiveFailures = %d, want 3", got)
	}
	record(ScrubPassed, VerificationContent, 4)
	p := localCopy(t, j, a)
	if p.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d after a pass, want 0", p.ConsecutiveFailures)
	}
	if p.LastFailureAt == nil {
		t.Error("a pass erased the last failure: that is history, and only the streak resets")
	}
}

func TestRecordCopyScrub_CouldNotCompleteIsNotAVerdictAboutTheCopy(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationContent,
		At: scrubAt.Add(-time.Hour), NextDue: scrubAt,
	}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubCouldNotComplete,
		At: scrubAt, NextDue: scrubAt.Add(time.Minute), Reason: "the endpoint did not answer",
	}); err != nil {
		t.Fatalf("could not complete: %v", err)
	}
	p := localCopy(t, j, a)
	if p.AssuranceState != AssuranceCouldNotComplete {
		t.Errorf("AssuranceState = %q, want could_not_complete", p.AssuranceState)
	}
	if p.VerificationClass != VerificationContent || p.ContentVerifiedAt == nil {
		t.Errorf("an unreachable endpoint erased a content claim: class=%q content_verified_at=%v", p.VerificationClass, p.ContentVerifiedAt)
	}
}

func TestRecordCopyScrub_RequiresRestoreIsItsOwnStateAndKeepsTheClassItAchieved(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubRequiresRestore, Class: VerificationExistence,
		At: scrubAt, NextDue: scrubAt.Add(time.Hour), Reason: "archive storage class",
	}); err != nil {
		t.Fatalf("RecordCopyScrub: %v", err)
	}
	p := localCopy(t, j, a)
	if p.AssuranceState != AssuranceRequiresRestore {
		t.Errorf("AssuranceState = %q, want requires_restore", p.AssuranceState)
	}
	if p.LastCheckedClass != VerificationExistence || p.ContentVerifiedAt != nil {
		t.Errorf("class/content age = %q/%v: the HEAD that ran is existence, and no bytes were read", p.LastCheckedClass, p.ContentVerifiedAt)
	}
	if p.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d: an archived copy is not failing", p.ConsecutiveFailures)
	}
}

func TestRecordCopyScrub_NotCheckedOnlyPushesTheDueTime(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	next := scrubAt.Add(6 * time.Hour)
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubNotChecked, At: scrubAt, NextDue: next,
		Reason: "nothing to check",
	}); err != nil {
		t.Fatalf("RecordCopyScrub: %v", err)
	}
	p := localCopy(t, j, a)
	if p.AssuranceState != AssuranceUnknown {
		t.Errorf("AssuranceState = %q: a pass that checked nothing must leave the copy unknown", p.AssuranceState)
	}
	if p.LastCheckedAt != nil || p.ConsecutiveFailures != 0 {
		t.Errorf("checked=%v failures=%d after a not-checked result", p.LastCheckedAt, p.ConsecutiveFailures)
	}
	if p.NextDueAt == nil || !p.NextDueAt.Equal(next) {
		t.Errorf("NextDueAt = %v, want %v so it does not hog every cycle", p.NextDueAt, next)
	}
}

func TestRecordCopyScrub_AnUnknownVerdictOrClassIsRefused(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	for _, c := range []CopyScrub{
		{Artifact: a, Medium: MediumLocal, Verdict: "green", At: scrubAt, NextDue: scrubAt},
		{Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: "full", At: scrubAt, NextDue: scrubAt},
		{Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: "", At: scrubAt, NextDue: scrubAt},
		{Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationContent, NextDue: scrubAt},
		{Artifact: a, Medium: "", Verdict: ScrubPassed, Class: VerificationContent, At: scrubAt, NextDue: scrubAt},
	} {
		if err := j.RecordCopyScrub(ctx, c); err == nil {
			t.Errorf("RecordCopyScrub(%+v) was accepted", c)
		}
	}
	if p := localCopy(t, j, a); p.AssuranceState != AssuranceUnknown {
		t.Errorf("a refused write changed the record: %q", p.AssuranceState)
	}
}

func TestRecordCopyScrub_NamesAMissingCopy(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: "bucket", Verdict: ScrubPassed, Class: VerificationExistence, At: scrubAt, NextDue: scrubAt,
	})
	if err == nil || !strings.Contains(err.Error(), "bucket") {
		t.Errorf("err = %v, want one naming the copy that does not exist", err)
	}
}

func TestRecordCopyScrub_TheFailureReasonIsBoundedAndCarriesNoControlBytes(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	hostile := "bad\x1b[31m\x07\x00" + strings.Repeat("x", 5000) + "\n\x1b]0;title\x07tail"
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubFailed, Class: VerificationContent,
		At: scrubAt, NextDue: scrubAt, Reason: hostile,
	}); err != nil {
		t.Fatalf("RecordCopyScrub: %v", err)
	}
	reason := localCopy(t, j, a).LastFailureReason
	if len(reason) > MaxScrubReasonBytes {
		t.Errorf("reason is %d bytes, bound is %d", len(reason), MaxScrubReasonBytes)
	}
	for _, r := range reason {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("reason carries control byte %#x: %q", r, reason)
		}
	}
}

// A rewrite of the artifact row's clock must change nothing about a copy's
// due time: that is the whole reason due-ness moved.
func TestRecordCopyScrub_TheArtifactRowsClockDoesNotMoveACopysDueTime(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	next := scrubAt.Add(24 * time.Hour)
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationContent, At: scrubAt, NextDue: next,
	}); err != nil {
		t.Fatalf("RecordCopyScrub: %v", err)
	}
	if _, err := j.db.ExecContext(ctx, `UPDATE artifacts SET updated_at = ?`, formatTime(scrubAt.Add(100*time.Hour))); err != nil {
		t.Fatalf("rewriting updated_at: %v", err)
	}
	p := localCopy(t, j, a)
	if p.NextDueAt == nil || !p.NextDueAt.Equal(next) || p.AssuranceState != AssurancePassed {
		t.Errorf("rewriting artifacts.updated_at changed the copy's record: next=%v state=%q", p.NextDueAt, p.AssuranceState)
	}
}

func TestClaimCopyScrub_OnlyOneHolderAtATime(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)

	got, err := j.ClaimCopyScrub(ctx, a, MediumLocal, "scheduler:1", scrubAt, time.Minute)
	if err != nil || !got {
		t.Fatalf("first claim = %v, %v; want true", got, err)
	}
	got, err = j.ClaimCopyScrub(ctx, a, MediumLocal, "cli:2", scrubAt.Add(10*time.Second), time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if got {
		t.Error("a second holder claimed a copy that is being scrubbed: two scrubs of one copy")
	}
	// The holder renewing its own lease is not a conflict.
	if got, err = j.ClaimCopyScrub(ctx, a, MediumLocal, "scheduler:1", scrubAt.Add(20*time.Second), time.Minute); err != nil || !got {
		t.Errorf("renewing own lease = %v, %v; want true", got, err)
	}
}

func TestClaimCopyScrub_AnExpiredLeaseCanBeTaken(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if got, err := j.ClaimCopyScrub(ctx, a, MediumLocal, "dead-process", scrubAt, time.Minute); err != nil || !got {
		t.Fatalf("claim = %v, %v", got, err)
	}
	got, err := j.ClaimCopyScrub(ctx, a, MediumLocal, "live-process", scrubAt.Add(2*time.Minute), time.Minute)
	if err != nil || !got {
		t.Errorf("claim after expiry = %v, %v; a crashed holder must not make a copy unscrubbable", got, err)
	}
}

func TestRecordCopyScrub_ReleasesTheLease(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if got, err := j.ClaimCopyScrub(ctx, a, MediumLocal, "h1", scrubAt, time.Hour); err != nil || !got {
		t.Fatalf("claim = %v, %v", got, err)
	}
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationContent, At: scrubAt, NextDue: scrubAt.Add(time.Hour),
	}); err != nil {
		t.Fatalf("RecordCopyScrub: %v", err)
	}
	if got, err := j.ClaimCopyScrub(ctx, a, MediumLocal, "h2", scrubAt.Add(time.Second), time.Hour); err != nil || !got {
		t.Errorf("claim after the result landed = %v, %v; the record must release its own lease", got, err)
	}
}

func TestClaimCopyScrub_RefusesACopyThatIsNotActive(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if _, err := j.db.ExecContext(ctx, `UPDATE placements SET status = 'GONE'`); err != nil {
		t.Fatal(err)
	}
	got, err := j.ClaimCopyScrub(ctx, a, MediumLocal, "h", scrubAt, time.Minute)
	if err != nil || got {
		t.Errorf("claim of a GONE copy = %v, %v; want false, nil", got, err)
	}
}

func TestRecordScrubPass_IsOneRowOfCounts(t *testing.T) {
	ctx := context.Background()
	j, _ := scrubFixture(t)
	id, err := j.RecordScrubPass(ctx, ScrubPass{
		TriggeredBy: ScrubBySchedule, Scope: "all-due",
		StartedAt: scrubAt, FinishedAt: scrubAt.Add(time.Minute),
		Selected: 1000, Checked: 990, Passed: 980, Failed: 4, CouldNotComplete: 6,
		RequiresRestore: 0, SkippedHeld: 7, SkippedLeased: 3, BytesRead: 1 << 30,
	})
	if err != nil || id <= 0 {
		t.Fatalf("RecordScrubPass = %d, %v", id, err)
	}
	passes, err := j.ListScrubPasses(ctx, 10)
	if err != nil || len(passes) != 1 {
		t.Fatalf("ListScrubPasses = %v, %v", passes, err)
	}
	if got := passes[0]; got.Selected != 1000 || got.Failed != 4 || got.SkippedHeld != 7 || got.TriggeredBy != ScrubBySchedule {
		t.Errorf("round trip = %+v", got)
	}
	if _, err := j.RecordScrubPass(ctx, ScrubPass{TriggeredBy: "cron", Scope: "x", StartedAt: scrubAt, FinishedAt: scrubAt}); err == nil {
		t.Error("an unknown trigger was accepted")
	}
	if _, err := j.RecordScrubPass(ctx, ScrubPass{TriggeredBy: ScrubByManual, Scope: "x", StartedAt: scrubAt, FinishedAt: scrubAt, Selected: -1}); err == nil {
		t.Error("a negative count was accepted")
	}
}

// The Go copies of the vocabularies must agree with the CHECKs in 0016.
func TestAssuranceVocabulariesAgreeWithTheSchema(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	_ = a
	for _, v := range []string{AssuranceUnknown, AssurancePassed, AssuranceFailed, AssuranceRequiresRestore, AssuranceCouldNotComplete} {
		if _, err := j.db.ExecContext(ctx, `UPDATE placements SET assurance_state = ?`, v); err != nil {
			t.Errorf("schema refuses assurance state %q the code writes: %v", v, err)
		}
	}
	for _, v := range []string{RecoveryUnknown, RecoveryNotConfigured, RecoveryNeverRun, RecoveryPassed, RecoveryOverdue, RecoveryFailed, RecoveryRefusedForCost, RecoveryCouldNotComplete} {
		if _, err := j.db.ExecContext(ctx, `UPDATE placements SET recovery_state = ?`, v); err != nil {
			t.Errorf("schema refuses recovery state %q the code writes: %v", v, err)
		}
	}
	if len(AssuranceStates) != 5 || len(RecoveryStates) != 8 {
		t.Errorf("vocabulary sizes = %d, %d; a new value needs a migration and a deliberate edit here", len(AssuranceStates), len(RecoveryStates))
	}
}

// A content scrub of an archive-class object is refused before anything is
// read, so no class ran. That is recorded as requires_restore without
// touching the class or either age.
func TestRecordCopyScrub_ARefusedArchiveReadRecordsNoClassAndMovesNoAge(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubPassed, Class: VerificationContent,
		At: scrubAt.Add(-time.Hour), NextDue: scrubAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordCopyScrub(ctx, CopyScrub{
		Artifact: a, Medium: MediumLocal, Verdict: ScrubRequiresRestore,
		At: scrubAt, NextDue: scrubAt.Add(time.Hour), Reason: "archive",
	}); err != nil {
		t.Fatalf("RecordCopyScrub: %v", err)
	}
	p := localCopy(t, j, a)
	if p.AssuranceState != AssuranceRequiresRestore {
		t.Errorf("AssuranceState = %q", p.AssuranceState)
	}
	if p.LastCheckedClass != VerificationContent || p.ContentVerifiedAt == nil || !p.ContentVerifiedAt.Equal(scrubAt.Add(-time.Hour)) {
		t.Errorf("a refusal that read nothing moved the class or the age: %q / %v", p.LastCheckedClass, p.ContentVerifiedAt)
	}
}

func TestScrubClaim_APassPastItsDueTimeReadsOverdueNotPassed(t *testing.T) {
	due := scrubAt.Add(time.Hour)
	a := Assurance{AssuranceState: AssurancePassed, NextDueAt: &due}
	if got := a.ScrubClaim(scrubAt); got != AssurancePassed {
		t.Errorf("inside its window: %q", got)
	}
	if got := a.ScrubClaim(due.Add(time.Second)); got != ScrubClaimOverdue {
		t.Errorf("past its window: %q, want %q: a stored pass is a statement about the moment it was made", got, ScrubClaimOverdue)
	}
	if got := (Assurance{AssuranceState: AssuranceFailed, NextDueAt: &due}).ScrubClaim(due.Add(time.Hour)); got != AssuranceFailed {
		t.Errorf("a failure must not soften into overdue: %q", got)
	}
	if got := (Assurance{}).ScrubClaim(scrubAt); got != AssuranceUnknown {
		t.Errorf("the zero value reads %q", got)
	}
}
