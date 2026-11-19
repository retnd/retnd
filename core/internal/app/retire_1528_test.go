package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// Issue #1528: a FAILED backup whose source is gone can never be retired, so
// the set shows FAILING forever.
//
// The reproduction is the forge set on the NAS, built through the real cycle
// rather than by writing rows by hand: two dumps fail to transfer (one with
// not_found, one with permission_denied), a newer dump arrives and is backed up
// cleanly, and then the host's own retention deletes the two failed dumps. From
// that moment nothing can ever fetch them again, and before this issue the set
// read FAILING for good, because retry was the only verb that touched a FAILED
// row and it can only fail again.
//
// What the engine owes is in docs/design/1528-retire-failed-artifacts.md: the
// automatic predicate P1 to P7, and the way back when the object reappears.
// Every case below names the condition it pins.

// retiredState is the state's contract name, spelled as a string so these
// tests say exactly what the journal stores.
const retiredState = "RETIRED"

// forgeFixture is the forge set: a service over one artifact-engine backup
// set, a fake source the test can delete objects from, and a clock the test
// moves forward between cycles so "newer" means something.
type forgeFixture struct {
	t        *testing.T
	svc      *Service
	journal  *state.Journal
	tr       *fakeTransport
	localDir string
	set      model.BackupSetID
	now      time.Time
}

func newForgeFixture(t *testing.T) *forgeFixture {
	t.Helper()
	localDir := t.TempDir()
	bs := testBackupSet(t, localDir)
	bs.RemotePath = ""

	f := &forgeFixture{t: t, tr: newFakeTransport(), journal: openJournal(t), localDir: localDir, set: bs.ID, now: epoch}
	f.svc = New(testConfig(t, testSource("production", bs)), f.journal, f.tr, nil)
	f.svc.Now = func() time.Time { return f.now }
	return f
}

// cycle advances the clock an hour and runs one cycle, failing the test if
// the set itself errored: every case here is about artifacts, and a set-level
// error would make a "nothing changed" assertion pass for the wrong reason.
func (f *forgeFixture) cycle() {
	f.t.Helper()
	f.now = f.now.Add(time.Hour)
	rep := f.svc.RunCycle(context.Background())
	for _, s := range rep.Sets {
		if s.Err != nil {
			f.t.Fatalf("the cycle failed for %s: %v", s.Set, s.Err)
		}
	}
}

// failWith puts one object on the source, runs a cycle whose transfers all
// fail with category c, and leaves later cycles running clean.
func (f *forgeFixture) failWith(name string, c transport.Category) model.ArtifactID {
	f.t.Helper()
	f.tr.put(name, "dump "+name, f.now.Unix())
	f.tr.copyToLocalErr = transport.NewError(c, "copy_to_local", errors.New("the fake source refused"))
	f.cycle()
	f.tr.copyToLocalErr = nil
	id := f.id(name)
	if got := f.stateOf(id); got != string(lifecycle.Failed) {
		f.t.Fatalf("fixture: %s is %s after a %s transfer, want FAILED", id, got, c)
	}
	return id
}

// backUp lands one artifact cleanly, through to COMPLETE.
func (f *forgeFixture) backUp(name string) model.ArtifactID {
	f.t.Helper()
	f.tr.put(name, "dump "+name, f.now.Unix())
	f.cycle()
	id := f.id(name)
	if got := f.stateOf(id); !lifecycle.IsDurableRestorePoint(lifecycle.State(got)) {
		f.t.Fatalf("fixture: %s is %s, want a durable restore point", id, got)
	}
	return id
}

func (f *forgeFixture) id(name string) model.ArtifactID {
	f.t.Helper()
	id, err := model.NewArtifactID(f.set, name)
	if err != nil {
		f.t.Fatalf("NewArtifactID: %v", err)
	}
	return id
}

func (f *forgeFixture) stateOf(id model.ArtifactID) string {
	f.t.Helper()
	rec, err := f.journal.Get(context.Background(), id)
	if err != nil {
		f.t.Fatalf("Get(%s): %v", id, err)
	}
	return rec.State
}

// hostDeletes is the host's own retention removing a source object.
func (f *forgeFixture) hostDeletes(name string) { delete(f.tr.objects, name) }

func (f *forgeFixture) health() health.BackupSetHealth {
	f.t.Helper()
	rep, err := f.svc.BuildHealthReport(context.Background(), VersionInfo{})
	if err != nil {
		f.t.Fatalf("BuildHealthReport: %v", err)
	}
	for _, h := range rep.BackupSets {
		if h.Set == f.set {
			return h
		}
	}
	f.t.Fatalf("the health report has no entry for %s", f.set)
	return health.BackupSetHealth{}
}

// retiredCount reads BackupSetHealth.RetiredCount by name, so this file
// states the field the issue asks for before the field exists.
func retiredCount(t *testing.T, h health.BackupSetHealth) int {
	t.Helper()
	v := reflect.ValueOf(h).FieldByName("RetiredCount")
	if !v.IsValid() {
		t.Fatalf("BackupSetHealth has no RetiredCount: a retired backup has to be counted apart, not hidden")
	}
	return int(v.Int())
}

// The forge, end to end: FAILING with two dead records and a good backup from
// this morning, then HEALTHY once the engine retires them, with both records
// still in the journal.
func TestTheForgeSetGoesFromFailingToHealthyWhenTheSourceIsGone_1528(t *testing.T) {
	f := newForgeFixture(t)
	notFound := f.failWith("gitea-dump-20261029T034245Z.dump", transport.NotFound)
	denied := f.failWith("gitea-dump-20261103T033855Z.dump", transport.PermissionDenied)
	good := f.backUp("gitea-dump-20261119T033012Z.dump")

	f.hostDeletes("gitea-dump-20261029T034245Z.dump")
	f.hostDeletes("gitea-dump-20261103T033855Z.dump")

	before := f.health()
	if before.State != health.Failing {
		t.Fatalf("before the fix's pass the set is %s (%s), want FAILING: the fixture is not the forge", before.State, before.Reason)
	}

	f.cycle()

	for _, id := range []model.ArtifactID{notFound, denied} {
		if got := f.stateOf(id); got != retiredState {
			t.Errorf("%s is %s after a cycle that found its source gone and a newer good backup (%s), want RETIRED", id, got, good)
		}
	}
	after := f.health()
	if after.State != health.Healthy {
		t.Errorf("after retiring both dead records the set is %s (%s), want HEALTHY", after.State, after.Reason)
	}
	if after.Failures != 0 || len(after.StuckFailures) != 0 {
		t.Errorf("a retired record still counts: failures=%d stuck=%v", after.Failures, after.StuckFailures)
	}
	if n := retiredCount(t, after); n != 2 {
		t.Errorf("retired_count = %d, want 2: nothing is hidden, it is counted apart", n)
	}

	// The records stay, with what they failed on.
	detail, err := f.svc.GetArtifactDetail(context.Background(), notFound)
	if err != nil {
		t.Fatalf("GetArtifactDetail(%s): %v", notFound, err)
	}
	if detail.State != retiredState {
		t.Errorf("the detail view reads %s, want RETIRED", detail.State)
	}
}

// The negative control: the same set with the failed objects still on the
// source. Nothing may be retired, and the set stays FAILING, because a human
// is genuinely needed (retry is the answer there).
func TestNothingIsRetiredWhileTheSourceIsStillThere_1528(t *testing.T) {
	f := newForgeFixture(t)
	notFound := f.failWith("gitea-dump-20261029T034245Z.dump", transport.NotFound)
	denied := f.failWith("gitea-dump-20261103T033855Z.dump", transport.PermissionDenied)
	f.backUp("gitea-dump-20261119T033012Z.dump")

	f.cycle()
	f.cycle()

	for _, id := range []model.ArtifactID{notFound, denied} {
		if got := f.stateOf(id); got != string(lifecycle.Failed) {
			t.Errorf("%s is %s although its source object is still listed, want FAILED", id, got)
		}
	}
	if h := f.health(); h.State != health.Failing {
		t.Errorf("the set is %s (%s), want FAILING while a fetchable object sits in FAILED", h.State, h.Reason)
	}
}

// P4: a permission failure on a file that still exists is the case the owner
// named as never automatic. The object is not listed (a listing that hides
// unreadable files) but Stat answers permission_denied, which is not absence.
func TestAPermissionFailureOnAPresentFileIsNeverAutoRetired_1528(t *testing.T) {
	f := newForgeFixture(t)
	denied := f.failWith("db-20261101.dump", transport.PermissionDenied)
	f.backUp("db-20261119.dump")

	f.hostDeletes("db-20261101.dump")
	f.tr.statErrFor = map[string]error{
		"db-20261101.dump": transport.NewError(transport.PermissionDenied, "stat", errors.New("permission denied")),
	}
	f.cycle()

	if got := f.stateOf(denied); got != string(lifecycle.Failed) {
		t.Errorf("%s is %s although Stat answered permission_denied, want FAILED", denied, got)
	}
}

// P4: a transient error is not absence either.
func TestATransientStatIsNotAbsence_1528(t *testing.T) {
	f := newForgeFixture(t)
	failed := f.failWith("db-20261101.dump", transport.NotFound)
	f.backUp("db-20261119.dump")

	f.hostDeletes("db-20261101.dump")
	f.tr.statErrFor = map[string]error{
		"db-20261101.dump": transport.NewError(transport.Transient, "stat", errors.New("connection reset")),
	}
	f.cycle()

	if got := f.stateOf(failed); got != string(lifecycle.Failed) {
		t.Errorf("%s is %s after a transient Stat, want FAILED", failed, got)
	}
}

// P6: with no newer good backup there is nothing that supersedes the failure,
// which also covers "the only copy".
func TestNoNewerGoodBackupMeansNoAutomaticRetirement_1528(t *testing.T) {
	f := newForgeFixture(t)
	failed := f.failWith("db-20261101.dump", transport.NotFound)
	f.hostDeletes("db-20261101.dump")
	f.cycle()

	if got := f.stateOf(failed); got != string(lifecycle.Failed) {
		t.Errorf("%s is %s with no newer good backup in the set, want FAILED", failed, got)
	}
}

// P6: a good backup OLDER than the failure does not supersede it.
func TestAnOlderGoodBackupIsNotASuccessor_1528(t *testing.T) {
	f := newForgeFixture(t)
	f.backUp("db-20261030.dump")
	failed := f.failWith("db-20261101.dump", transport.NotFound)
	f.hostDeletes("db-20261101.dump")
	f.cycle()

	if got := f.stateOf(failed); got != string(lifecycle.Failed) {
		t.Errorf("%s is %s although the only good backup is older, want FAILED", failed, got)
	}
}

// P5: issue #662's shape. A FAILED row whose own durable copy sits at its final
// name is rescued by retry; retiring it would orphan a real backup file.
func TestTheOwnCopyAtTheFinalNameIsNeverRetired_1528(t *testing.T) {
	f := newForgeFixture(t)
	failed := f.failWith("db-20261101.dump", transport.NotFound)
	final, err := lifecycle.FinalArtifactPath(f.localDir, failed)
	if err != nil {
		t.Fatalf("FinalArtifactPath: %v", err)
	}
	mustWriteFile(t, final, "a real copy")
	if _, err := lifecycle.Advance(context.Background(), lifecycle.Deps{Journal: f.journal, Now: f.svc.Now}, state.Transition{
		Artifact: failed, Key: "fixture:662-local-path", From: string(lifecycle.Failed), To: string(lifecycle.Failed),
		LocalPath: &final, Detail: "fixture: the row records its own final name, as #662 left it",
	}); err != nil {
		t.Fatalf("fixture: recording the final local path: %v", err)
	}
	f.backUp("db-20261119.dump")
	f.hostDeletes("db-20261101.dump")
	f.cycle()

	if got := f.stateOf(failed); got != string(lifecycle.Failed) {
		t.Errorf("%s is %s although its own copy is at the final name, want FAILED", failed, got)
	}
	if _, err := os.Stat(final); err != nil {
		t.Errorf("the durable copy at %s is gone: %v", final, err)
	}
}

// The way back: a retired object that reappears on the source is put back
// into the pipeline by the next cycle, because the retirement recorded the
// source as gone and that is no longer true.
func TestARetiredObjectThatReappearsReentersThePipeline_1528(t *testing.T) {
	f := newForgeFixture(t)
	failed := f.failWith("db-20261101.dump", transport.NotFound)
	f.backUp("db-20261119.dump")
	f.hostDeletes("db-20261101.dump")
	f.cycle()
	if got := f.stateOf(failed); got != retiredState {
		t.Fatalf("fixture: %s is %s, want RETIRED before the object comes back", failed, got)
	}

	f.tr.put("db-20261101.dump", "dump db-20261101.dump", f.now.Unix())
	f.cycle()

	if got := f.stateOf(failed); got == retiredState || got == string(lifecycle.Failed) {
		t.Errorf("%s is %s after its object reappeared, want it back in the pipeline", failed, got)
	}
	if _, found, err := f.journal.LastTransition(context.Background(), failed, retiredState, string(lifecycle.Discovered)); err != nil || !found {
		t.Errorf("no RETIRED -> DISCOVERED transition was recorded (found=%v err=%v)", found, err)
	}
}

// The leftover .partial is removed with the retirement.
func TestTheLeftoverPartialIsRemovedWithTheRetirement_1528(t *testing.T) {
	f := newForgeFixture(t)
	failed := f.failWith("db-20261101.dump", transport.NotFound)
	final, err := lifecycle.FinalArtifactPath(f.localDir, failed)
	if err != nil {
		t.Fatalf("FinalArtifactPath: %v", err)
	}
	partial := final + ".partial"
	mustWriteFile(t, partial, "half a dump")
	f.backUp("db-20261119.dump")
	f.hostDeletes("db-20261101.dump")
	f.cycle()

	if got := f.stateOf(failed); got != retiredState {
		t.Fatalf("%s is %s, want RETIRED", failed, got)
	}
	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the leftover partial %s is still there (stat err %v)", partial, err)
	}
}
