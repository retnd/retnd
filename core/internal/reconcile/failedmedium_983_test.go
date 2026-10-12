package reconcile

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
)

// EPIC Q, #983: a copy on a storage medium whose last check failed (a scrub
// that found wrong bytes, or a destination that attested a checksum other than
// the one recorded) is a positive finding. Reconcile cannot read a medium, so
// it does not re-prove it; it reads what the journal already says and refuses
// to report the set as "left alone" while one copy in it is known bad.

func movedAndRecordedAs(t *testing.T, verdict state.ScrubVerdict, class string) (*state.Journal, Report) {
	t.Helper()
	j := openTestJournal(t)
	artifact := testArtifact(t, "moved.dump")
	size := int64(32)
	localPath := filepath.Join(t.TempDir(), "moved.final")
	driveTo(t, j, driveParams{
		artifact: artifact, remote: state.RemoteIdentity{Size: &size}, localPath: localPath,
		transfer: &state.TransferResult{BytesTransferred: size}, stopAt: lifecycle.Complete,
	})
	moveToMedium(t, j, artifact, lifecycle.Complete, localPath, size)
	at := time.Now().UTC()
	if err := j.RecordCopyScrub(context.Background(), state.CopyScrub{
		Artifact: artifact, Medium: onMediumTestMedium, Verdict: verdict, Class: class,
		Reason: "the destination's own checksum (SHA-256) for this copy does not match", At: at, NextDue: at.Add(time.Hour),
	}); err != nil {
		t.Fatalf("RecordCopyScrub: %v", err)
	}
	report, err := Reconcile(context.Background(), Deps{Journal: j, Transport: &fakeTransport{}}, testSource, testSet(t))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	requireNoErrors(t, report)
	return j, report
}

func TestReconcile_AMediumCopyRecordedAsFailingNeedsInvestigationAndIsNotMoved(t *testing.T) {
	j, report := movedAndRecordedAs(t, state.ScrubFailed, state.VerificationAttested)
	f := requireOneFinding(t, report)
	if !f.NeedsInvestigation {
		t.Fatalf("Finding %q does not flag a copy recorded as failing", f.Reason)
	}
	if !strings.Contains(f.Reason, onMediumTestMedium) || !strings.Contains(f.Reason, "checksum") {
		t.Errorf("Reason = %q, want the medium and what was recorded", f.Reason)
	}
	if f.Changed() {
		t.Errorf("Finding %s -> %s: one failing copy must not decide the artifact's fate", f.From, f.To)
	}
	assertJournalState(t, j, testArtifact(t, "moved.dump"), lifecycle.Complete)
}

func TestReconcile_AMediumCopyThatPassedItsLastCheckIsNotFlagged(t *testing.T) {
	_, report := movedAndRecordedAs(t, state.ScrubPassed, state.VerificationExistence)
	if f := requireOneFinding(t, report); f.NeedsInvestigation {
		t.Fatalf("a passing copy was flagged: %q", f.Reason)
	}
}
