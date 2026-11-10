package app

import (
	"reflect"
	"testing"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/placement"
)

// Issue #1290: the two stages of an encrypted copy and how the tracker folds
// the move engine's readings into Progress.
//
// Written by reading and not run. Each test names what turns it red.

func copyArtifact() model.ArtifactID { return model.ArtifactID{Name: "nightly.dump"} }

func newCopyTracker() (*cycleProgress, *recordingObserver) {
	rec := &recordingObserver{}
	c := &cycleProgress{obs: rec}
	c.cur.Stage = StageCleaningRemote
	return c, rec
}

func lastReading(t *testing.T, rec *recordingObserver) Progress {
	t.Helper()
	all := rec.all()
	if len(all) == 0 {
		t.Fatal("nothing was published")
	}
	return all[len(all)-1]
}

// The names are the placement package's own, appended after the cycle's five
// so an existing client's idea of the order does not move. Red when either
// is missing, renamed on one side, or inserted before an existing stage.
func TestStagesAppendEncryptingAndDecryptVerifyingAfterTheCyclesOwn_1290(t *testing.T) {
	want := []string{"discovering", "transferring", "verifying", "committing", "cleaning-remote", "encrypting", "decrypt-verifying"}
	if !reflect.DeepEqual(Stages, want) {
		t.Errorf("Stages = %v, want %v", Stages, want)
	}
	if StageEncrypting != placement.StageEncrypting || StageDecryptVerifying != placement.StageDecryptVerifying {
		t.Errorf("app and placement spell the stages differently: %q/%q vs %q/%q",
			StageEncrypting, StageDecryptVerifying, placement.StageEncrypting, placement.StageDecryptVerifying)
	}
}

// A measured zero is a non-nil zero, and an unknown rate is nil. Red when the
// first reading is nil (reads as "not measured") or carries a rate.
func TestObserveCopyStartsAtAMeasuredZeroWithNoRate_1290(t *testing.T) {
	c, rec := newCopyTracker()

	c.observeCopy(placement.CopyProgress{Stage: placement.StageEncrypting, Artifact: copyArtifact(), Done: 0, Total: 1000})

	p := lastReading(t, rec)
	if p.Stage != StageEncrypting || p.Artifact != "nightly.dump" {
		t.Errorf("stage/artifact = %q/%q", p.Stage, p.Artifact)
	}
	if p.BytesTransferred == nil || *p.BytesTransferred != 0 {
		t.Errorf("BytesTransferred = %v, want a non-nil zero", p.BytesTransferred)
	}
	if p.BytesTotal == nil || *p.BytesTotal != 1000 {
		t.Errorf("BytesTotal = %v, want 1000", p.BytesTotal)
	}
	if p.BytesPerSecond != nil {
		t.Errorf("BytesPerSecond = %v, want nil: the engine measures no rate", *p.BytesPerSecond)
	}
}

// A reading below the last one in the same stage and artifact is dropped, and
// one past the total is held at the total. Red when a late or reordered
// reading moves the bar backward or off the end.
func TestObserveCopyIsMonotonicAndBounded_1290(t *testing.T) {
	c, rec := newCopyTracker()
	send := func(done int64) {
		c.observeCopy(placement.CopyProgress{Stage: placement.StageDecryptVerifying, Artifact: copyArtifact(), Done: done, Total: 1000})
	}
	send(400)
	send(300)
	if got := *lastReading(t, rec).BytesTransferred; got != 400 {
		t.Errorf("a lower reading moved the bar to %d", got)
	}
	send(5000)
	if got := *lastReading(t, rec).BytesTransferred; got != 1000 {
		t.Errorf("a reading past the total gave %d, want 1000", got)
	}
}

// A zero total is "not known", which is nil, never a total of zero. Red when
// it is reported as a pointer to zero.
func TestObserveCopyWithAnUnknownTotalReportsNilNotZero_1290(t *testing.T) {
	c, rec := newCopyTracker()

	c.observeCopy(placement.CopyProgress{Stage: placement.StageDecryptVerifying, Artifact: copyArtifact(), Done: 64, Total: 0})

	p := lastReading(t, rec)
	if p.BytesTotal != nil {
		t.Errorf("BytesTotal = %d, want nil", *p.BytesTotal)
	}
	if p.BytesTransferred == nil || *p.BytesTransferred != 64 {
		t.Errorf("BytesTransferred = %v, want 64", p.BytesTransferred)
	}
}

// Ending a stage puts back the stage and artifact the cycle had, and takes
// the counters away: a cancelled or failed copy leaves no Encrypting behind.
// Red when the stage stays, or the counters stay.
func TestObserveCopyEndRestoresTheStageAndClearsTheBytes_1290(t *testing.T) {
	c, rec := newCopyTracker()
	c.observeCopy(placement.CopyProgress{Stage: placement.StageEncrypting, Artifact: copyArtifact(), Done: 10, Total: 100})

	c.observeCopy(placement.CopyProgress{Stage: placement.StageEncrypting, Artifact: copyArtifact(), Ended: true})

	p := lastReading(t, rec)
	if p.Stage != StageCleaningRemote {
		t.Errorf("Stage = %q after the copy ended, want the cycle's own %q back", p.Stage, StageCleaningRemote)
	}
	if p.Artifact != "" || p.BytesTransferred != nil || p.BytesTotal != nil || p.BytesPerSecond != nil {
		t.Errorf("an ended copy left a reading behind: %+v", p)
	}
}

// Going from one stage to the next does not lose the first stage's restore
// point, and the second stage starts from its own zero. Red when the second
// stage inherits the first's counters or becomes the restore point.
func TestObserveCopyTwoStagesInARowRestoreToTheCyclesOwnStage_1290(t *testing.T) {
	c, rec := newCopyTracker()
	a := copyArtifact()
	c.observeCopy(placement.CopyProgress{Stage: placement.StageEncrypting, Artifact: a, Done: 90, Total: 100})
	c.observeCopy(placement.CopyProgress{Stage: placement.StageEncrypting, Artifact: a, Ended: true})
	c.observeCopy(placement.CopyProgress{Stage: placement.StageDecryptVerifying, Artifact: a, Done: 0, Total: 120})

	p := lastReading(t, rec)
	if p.Stage != StageDecryptVerifying || p.BytesTransferred == nil || *p.BytesTransferred != 0 {
		t.Errorf("the second stage did not start from its own measured zero: %+v", p)
	}
	c.observeCopy(placement.CopyProgress{Stage: placement.StageDecryptVerifying, Artifact: a, Ended: true})
	if got := lastReading(t, rec).Stage; got != StageCleaningRemote {
		t.Errorf("Stage = %q at the end, want %q", got, StageCleaningRemote)
	}
}

// A cycle nobody observes carries a nil tracker. Red on a nil dereference.
func TestObserveCopyOnANilTrackerIsSafe_1290(t *testing.T) {
	var c *cycleProgress
	c.observeCopy(placement.CopyProgress{Stage: placement.StageEncrypting, Artifact: copyArtifact(), Done: 1, Total: 2})
}
