package placement_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/retnd/retnd/core/internal/placement"
)

// Issue #1290: the move engine reports the two stages of an encrypted copy,
// Encrypting and Decrypt-verifying, with byte progress, through the
// Engine.Progress callback that core/internal/app feeds into app.Progress.
//
// These were written by reading the engine and the fixtures in this package
// and have not been run. Each one names the defect that turns it red in its
// own comment.

// progressLog is what a caller of Engine.Progress keeps.
type progressLog struct {
	mu  sync.Mutex
	got []placement.CopyProgress
}

func (l *progressLog) add(p placement.CopyProgress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, p)
}

func (l *progressLog) all() []placement.CopyProgress {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]placement.CopyProgress(nil), l.got...)
}

// of returns one stage's events in arrival order.
func (l *progressLog) of(stage string) []placement.CopyProgress {
	var out []placement.CopyProgress
	for _, p := range l.all() {
		if p.Stage == stage {
			out = append(out, p)
		}
	}
	return out
}

// watch installs a log on the fixture's engine with a step small enough for
// the six-chunk artifact to report several times.
func watch(f *encFixture) *progressLog {
	l := &progressLog{}
	f.engine.Progress = l.add
	f.engine.ProgressStep = 64 * 1024
	return l
}

// measuring drops the closing event of a stage.
func measuring(events []placement.CopyProgress) []placement.CopyProgress {
	var out []placement.CopyProgress
	for _, p := range events {
		if !p.Ended {
			out = append(out, p)
		}
	}
	return out
}

// checkBytes asserts the three properties every byte reading must keep:
// it starts at a measured zero, it never goes backward, and it never passes
// the total, and it ends at the total.
func checkBytes(t *testing.T, stage string, events []placement.CopyProgress, wantTotal int64) {
	t.Helper()
	m := measuring(events)
	if len(m) < 3 {
		t.Fatalf("%s reported %d readings over a multi-chunk artifact, want a start, at least one in between and an end: %+v", stage, len(m), m)
	}
	if m[0].Done != 0 {
		t.Errorf("%s: the first reading is %d bytes, want a measured zero", stage, m[0].Done)
	}
	var prev int64
	for i, p := range m {
		if p.Total != wantTotal {
			t.Errorf("%s reading %d: total %d, want %d", stage, i, p.Total, wantTotal)
		}
		if p.Done < prev {
			t.Errorf("%s reading %d went backward: %d after %d", stage, i, p.Done, prev)
		}
		if p.Done > p.Total {
			t.Errorf("%s reading %d is past the total: %d of %d", stage, i, p.Done, p.Total)
		}
		prev = p.Done
	}
	if last := m[len(m)-1]; last.Done != wantTotal {
		t.Errorf("%s ends at %d of %d bytes", stage, last.Done, wantTotal)
	}
}

// Red when the engine has no Progress callback, or never calls it for the
// encrypting pass, or reports plaintext bytes that skip, repeat or overshoot.
func TestAnEncryptedCopyReportsEncryptingBytesThatNeverGoBackwardOrPastTheTotal_1290(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{content: multiChunk()})
	l := watch(f)

	if r := f.runCycle(); r.Completed != 1 {
		t.Fatalf("the move did not complete: %+v", r)
	}
	checkBytes(t, placement.StageEncrypting, l.of(placement.StageEncrypting), int64(len(multiChunk())))
}

// The total of decrypt-verifying is the stored object, ciphertext, which is
// bigger than the artifact. Red when it reports the plaintext size, or no
// total, or counts only the bytes DecryptVerify consumed before the drain.
func TestDecryptVerifyingReportsTheBytesOfTheStoredObject_1290(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{content: multiChunk()})
	l := watch(f)

	if r := f.runCycle(); r.Completed != 1 {
		t.Fatalf("the move did not complete: %+v", r)
	}
	stored := int64(len(f.medium.bytesAt(f.objectKey())))
	if stored <= int64(len(multiChunk())) {
		t.Fatalf("the stored object (%d bytes) is not bigger than the artifact, so the fixture cannot tell the two totals apart", stored)
	}
	checkBytes(t, placement.StageDecryptVerifying, l.of(placement.StageDecryptVerifying), stored)
}

// Each stage opens, measures and closes once, and the second does not start
// before the first has closed. The pre-delete re-read of the destination is
// not a stage of its own, so there is exactly one close of each. Red when the
// re-read reports as a second decrypt-verify, or a stage never closes.
func TestEachEncryptedStageClosesExactlyOnceAndInOrder_1290(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{content: multiChunk()})
	l := watch(f)

	if r := f.runCycle(); r.Completed != 1 {
		t.Fatalf("the move did not complete: %+v", r)
	}
	var shape []string
	for _, p := range l.all() {
		tag := p.Stage
		if p.Ended {
			tag += "/end"
		}
		if n := len(shape); n == 0 || shape[n-1] != tag {
			shape = append(shape, tag)
		}
		if p.Artifact != f.artifact {
			t.Errorf("a %s reading names %v, want %v", p.Stage, p.Artifact, f.artifact)
		}
	}
	want := []string{
		placement.StageEncrypting, placement.StageEncrypting + "/end",
		placement.StageDecryptVerifying, placement.StageDecryptVerifying + "/end",
	}
	if strings.Join(shape, ",") != strings.Join(want, ",") {
		t.Errorf("the stages arrived as %v, want %v", shape, want)
	}
}

// Fail closed on cancel: a cancel in the middle of encrypting closes the
// stage, claims no later one, and leaves nothing on the medium. Red when the
// stage is left open (a UI would show Encrypting for ever) or when a stage
// that never ran is reported.
func TestACancelledEncryptingPassClosesItsStageAndClaimsNoLaterOne_1290(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{content: multiChunk()})
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	l := &progressLog{}
	f.engine.ProgressStep = 64 * 1024
	f.engine.Progress = func(p placement.CopyProgress) {
		l.add(p)
		if p.Stage == placement.StageEncrypting && !p.Ended && p.Done > 0 {
			cancel()
		}
	}

	_, _ = f.engine.RunCycle(ctx, []placement.Plan{{Artifact: f.artifact, DestinationMedium: testMedium}})

	enc := l.of(placement.StageEncrypting)
	if len(enc) == 0 || !enc[len(enc)-1].Ended {
		t.Fatalf("the cancelled encrypting stage was left open: %+v", enc)
	}
	if got := l.of(placement.StageDecryptVerifying); len(got) != 0 {
		t.Errorf("decrypt-verifying was reported for a copy that never uploaded: %+v", got)
	}
	if obj := f.medium.bytesAt(f.objectKey()); len(obj) != 0 {
		t.Errorf("a cancelled encrypting pass left %d bytes on the medium", len(obj))
	}
}

// A failed upload closes encrypting and never opens decrypt-verifying. Red
// when the failure path returns before the stage is closed.
func TestAFailedUploadClosesEncryptingAndNeverOpensDecryptVerifying_1290(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{content: multiChunk()})
	l := watch(f)
	f.medium.uploadErr = fmt.Errorf("endpoint down")

	f.runCycle()

	enc := l.of(placement.StageEncrypting)
	if len(enc) == 0 || !enc[len(enc)-1].Ended {
		t.Fatalf("encrypting was left open after a failed upload: %+v", enc)
	}
	if got := l.of(placement.StageDecryptVerifying); len(got) != 0 {
		t.Errorf("decrypt-verifying was reported after a failed upload: %+v", got)
	}
}

// A destination that keeps something other than what was sent fails the
// decrypt-verify. The stage still closes, so a failed verification never
// reads as one still running. Red when the failing path skips the close.
func TestAFailedDecryptVerifyStillClosesItsStage_1290(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{content: multiChunk()})
	l := watch(f)
	f.medium.corrupt = []byte("age-encryption.org/v1\nnot a real stream")

	f.runCycle()

	dv := l.of(placement.StageDecryptVerifying)
	if len(dv) == 0 {
		t.Fatal("the failing decrypt-verify reported nothing at all")
	}
	if !dv[len(dv)-1].Ended {
		t.Errorf("a failed decrypt-verify left its stage open: %+v", dv)
	}
	for _, p := range measuring(dv) {
		if p.Total > 0 && p.Done > p.Total {
			t.Errorf("a failing read reported %d of %d bytes", p.Done, p.Total)
		}
	}
}

// Nothing a reading carries can be key material, and the two readings of a
// run are value types: a canary for the secret and the key id is searched for
// in everything that was reported. Red if a field that names a key is added.
func TestNoProgressReadingCarriesKeyMaterialOrAKeyId_1290(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{content: multiChunk()})
	l := watch(f)

	f.runCycle()

	blob := fmt.Sprintf("%+v", l.all())
	if len(l.all()) == 0 {
		t.Fatal("no readings were reported, so the search below would pass vacuously")
	}
	if strings.Contains(blob, "AGE-SECRET-KEY") || strings.Contains(blob, f.ck.ID) {
		t.Errorf("a progress reading names key material or a key id: %s", blob)
	}
}

// A plaintext set reports neither stage: the stages exist for encrypted
// copies only. Red when the callback fires for any copy.
func TestAPlaintextCopyReportsNoEncryptedStage_1290(t *testing.T) {
	f := newFixture(t, fixtureOpts{content: multiChunk()})
	l := &progressLog{}
	f.engine.Progress = l.add

	if r := f.runCycle(); r.Completed != 1 {
		t.Fatalf("the move did not complete: %+v", r)
	}
	if got := l.all(); len(got) != 0 {
		t.Errorf("a plaintext copy reported encrypted stages: %+v", got)
	}
}

// No callback is the old behaviour exactly. Red when the engine dereferences
// a nil Progress.
func TestAnEngineWithNoProgressCallbackStillCompletesAnEncryptedCopy_1290(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{content: multiChunk()})

	if r := f.runCycle(); r.Completed != 1 {
		t.Fatalf("the move did not complete: %+v", r)
	}
}
