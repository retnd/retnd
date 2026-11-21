package placement_test

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"

	_ "modernc.org/sqlite"
)

// These are #1545's engine tests: re-encrypting an artifact that still has an
// ACTIVE local plaintext copy, by uploading a fresh copy under the active key
// from that local copy (Plan.FromLocal) instead of reading the old remote one.
//
// The world is the one #1317's tests use (an artifact moved for real onto
// medium A under key 1, then custody rotates to key 2), plus the local copy
// the first hop retired, written back and journaled ACTIVE again, which is
// what an artifact inside its local retention window looks like.

type fromLocalWorld struct{ *reencWorld }

func newFromLocalWorld(t *testing.T) *fromLocalWorld {
	t.Helper()
	w := newReencWorld(t)
	if w.localExists() {
		t.Fatal("control: the first hop left the local copy behind, so this world cannot say the test restored it")
	}
	if err := os.WriteFile(w.localPath(), w.content, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(w.root, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Exec(`UPDATE placements SET status = 'ACTIVE', location = ? WHERE medium = 'local'`, w.localPath())
	if err != nil {
		t.Fatalf("re-activating the local placement: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("control: %d local placement rows, want one", n)
	}
	if p, ok := w.placement("local"); !ok || p.Status != state.PlacementActive {
		t.Fatalf("control: the local placement is %+v (ok=%v), want ACTIVE", p, ok)
	}
	return &fromLocalWorld{w}
}

func (w *fromLocalWorld) fromLocalPlan(destination string) placement.Plan {
	p := w.plan(destination)
	p.FromLocal = true
	return p
}

func (w *fromLocalWorld) run(p placement.Plan) placement.CycleReport {
	w.t.Helper()
	report, err := w.engine.EncryptExisting(w.ctx, []placement.Plan{p})
	if err != nil {
		w.t.Fatalf("EncryptExisting: %v", err)
	}
	return report
}

// localCopyIntact is the claim about the local copy this run never changes: its
// bytes, its file and its ACTIVE placement.
func (w *fromLocalWorld) localCopyIntact(when string) {
	w.t.Helper()
	got, err := os.ReadFile(w.localPath())
	if err != nil || !bytes.Equal(got, w.content) {
		w.t.Fatalf("%s: the local copy is gone or changed (err=%v)", when, err)
	}
	if p, ok := w.placement("local"); !ok || p.Status != state.PlacementActive {
		w.t.Fatalf("%s: the local placement is %+v (ok=%v), want ACTIVE", when, p, ok)
	}
}

// Without the mode the refusal #1545 describes stands: two ACTIVE placements,
// nothing chosen, nothing planned. Green today, and it has to stay green: the
// mode is opt-in, never the default.
func TestWithoutFromLocalAnArtifactWithALocalCopyIsStillRefused_1545(t *testing.T) {
	w := newFromLocalWorld(t)
	err := w.engine.CheckEncryptExisting(w.ctx, w.plan(mediumB))
	if !errors.Is(err, placement.ErrNotEligible) {
		t.Fatalf("CheckEncryptExisting = %v, want ErrNotEligible", err)
	}
	report := w.run(w.plan(mediumB))
	if report.Refused != 1 || report.Planned != 0 || len(w.moves()) != 1 {
		t.Fatalf("report %+v with %d moves, want one refusal and only the first hop's move", report, len(w.moves()))
	}
	w.sourceIntact("after a refusal without the mode")
	w.localCopyIntact("after a refusal without the mode")
}

// The ordinary retention cycle never uses the mode either.
func TestTheRetentionCycleNeverUploadsFromTheLocalCopy_1545(t *testing.T) {
	w := newFromLocalWorld(t)
	p := w.fromLocalPlan(mediumB)
	report, err := w.engine.RunCycle(w.ctx, []placement.Plan{p})
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if report.Completed != 0 || len(w.moves()) != 1 {
		t.Fatalf("the cycle did something with a plan that asked for the mode: %+v, %d moves", report, len(w.moves()))
	}
	w.sourceIntact("after a cycle")
}

// The happy path. Red until the mode exists. Also red if the old remote copy
// is read (no egress), if the new copy is not under the active key and
// recorded, if the old copy is not retired afterwards, if the local copy is
// touched, or if plaintext lands anywhere but the artifact's own local path.
func TestFromLocalUploadsFreshCopyWithoutReadingTheOldOne_1545(t *testing.T) {
	w := newFromLocalWorld(t)
	opensBefore := w.store.opensOf(w.key) // the first hop's own verification read
	if err := w.engine.CheckEncryptExisting(w.ctx, w.fromLocalPlan(mediumB)); err != nil {
		t.Fatalf("the dry-run check refuses what the run would do: %v", err)
	}

	report := w.run(w.fromLocalPlan(mediumB))
	w.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("the run did not complete: %+v (move error %q)", report, w.lastMove().Error)
	}
	if n := w.store.opensOf(w.key) - opensBefore; n != 0 {
		t.Errorf("the old copy on %s was opened %d time(s); the mode reads the local copy and nothing from the old one", testMedium, n)
	}

	obj := w.medium.bytesAt(w.keyB)
	if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) || bytes.Contains(obj, w.content[:64]) {
		t.Fatalf("the object on %s is not age ciphertext of the artifact (%d bytes)", mediumB, len(obj))
	}
	if err := w.keys.WithKey(w.ctx, w.newKey.ID, func(k []byte) error {
		_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), copycrypt.AgeX25519{}, k, w.hash)
		return err
	}); err != nil {
		t.Fatalf("the new copy does not decrypt to the artifact under the active key: %v", err)
	}
	if err := w.keys.WithKey(w.ctx, w.oldKey.ID, func(k []byte) error {
		_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), copycrypt.AgeX25519{}, k, w.hash)
		return err
	}); !errors.Is(err, copycrypt.ErrWrongKey) {
		t.Fatalf("the new copy under the superseded key: %v, want ErrWrongKey", err)
	}
	rec, ok := w.recorded(mediumB)
	if !ok || rec.KeyID != w.newKey.ID || rec.StoredHash != sum(obj) || rec.StoredSize != int64(len(obj)) {
		t.Fatalf("record %+v (ok=%v) does not describe the new object under key %s", rec, ok, w.newKey.ID)
	}
	row, ok := w.placement(mediumB)
	if !ok || row.Status != state.PlacementActive || row.VerificationClass != state.VerificationContent {
		t.Fatalf("destination placement %+v (ok=%v), want ACTIVE at content class", row, ok)
	}

	if w.medium.has(w.key) {
		t.Errorf("the copy under the old key is still on %s after a completed run", testMedium)
	}
	if p, ok := w.placement(testMedium); !ok || p.Status != state.PlacementGone {
		t.Errorf("the old copy's placement is %+v (ok=%v), want GONE", p, ok)
	}
	w.localCopyIntact("after the run")
	if files := w.plaintextOnDisk(); len(files) != 1 || files[0] != w.localPath() {
		t.Errorf("plaintext is in %v, want only the artifact's own local copy %s", files, w.localPath())
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Errorf("the staging area holds %v after the run", left)
	}
	if consented, err := w.journal.EncryptExistingConsented(w.ctx, w.lastMove().ID); err != nil || !consented {
		t.Fatalf("the move has no recorded opt-in (consented=%v err=%v)", consented, err)
	}
}

// Nothing is read from the old copy, so a key that is lost or compromised and
// withdrawn is no obstacle: this is the operator story of the issue. Red if
// the mode still demands to open the old copy.
func TestFromLocalDoesNotNeedTheOldKey_1545(t *testing.T) {
	w := newFromLocalWorld(t)
	w.engine.Keys = keysWithout{Keys: w.keys, gone: w.oldKey.ID}
	report := w.run(w.fromLocalPlan(mediumB))
	w.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("the run did not complete without the old key: %+v (move error %q)", report, w.lastMove().Error)
	}
	if rec, ok := w.recorded(mediumB); !ok || rec.KeyID != w.newKey.ID {
		t.Fatalf("record %+v (ok=%v), want the active key", rec, ok)
	}
}

// A FRESH plan for an artifact with no local copy takes the ordinary route even
// when the mode is asked for (the lenient-plan guard; the dry run printed the
// path): the old copy is streamed through its own key.
func TestAFreshFromLocalPlanForAnArtifactWithNoLocalCopyIsTheOrdinaryRun_1545(t *testing.T) {
	w := newReencWorld(t)
	opensBefore := w.store.opensOf(w.key)
	p := w.plan(mediumB)
	p.FromLocal = true
	report, err := w.engine.EncryptExisting(w.ctx, []placement.Plan{p})
	if err != nil {
		t.Fatal(err)
	}
	w.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("the run did not complete: %+v", report)
	}
	if n := w.store.opensOf(w.key) - opensBefore; n == 0 {
		t.Error("the old copy was never read, so this was not the ordinary route")
	}
}

// A local copy that no longer matches the recorded digest is never uploaded
// and never trusted: the old copy stays the only good one, nothing is
// catalogued on the destination, and the old copy is not read as a silent
// fallback. Red if anything is retired or catalogued.
func TestFromLocalWithAChangedLocalCopyChangesNothing_1545(t *testing.T) {
	w := newFromLocalWorld(t)
	opensBefore := w.store.opensOf(w.key)
	uploadsBefore := w.medium.uploadCount()
	bad := append([]byte(nil), w.content...)
	bad[len(bad)/2] ^= 0xff
	if err := os.WriteFile(w.localPath(), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	report := w.run(w.fromLocalPlan(mediumB))
	w.guard.fail()
	if report.Completed != 0 {
		t.Fatalf("a run over a changed local copy completed: %+v", report)
	}
	if n := w.medium.uploadCount() - uploadsBefore; n != 0 {
		t.Errorf("%d upload(s) reached the medium; a changed local copy is never uploaded", n)
	}
	w.sourceIntact("after a changed local copy")
	if row, ok := w.placement(mediumB); ok && row.Status == state.PlacementActive {
		t.Fatalf("a copy was catalogued on %s: %+v", mediumB, row)
	}
	if w.medium.has(w.keyB) {
		t.Errorf("an object is left on %s", mediumB)
	}
	if n := w.store.opensOf(w.key) - opensBefore; n != 0 {
		t.Errorf("the old copy was read %d time(s) as a fallback; the dry run promised no egress", n)
	}
	got, _ := os.ReadFile(w.localPath())
	if !bytes.Equal(got, bad) {
		t.Error("the local copy was rewritten or removed; this run never touches it")
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Errorf("the staging area holds %v", left)
	}
}

// An endpoint that keeps something other than what it was sent fails the
// decrypt-verify, and the copy under the old key is not retired.
func TestFromLocalRetiresNothingWhenTheUploadDoesNotVerify_1545(t *testing.T) {
	w := newFromLocalWorld(t)
	w.medium.corrupt = []byte("this is not what was uploaded")
	report := w.run(w.fromLocalPlan(mediumB))
	w.guard.fail()
	if report.Completed != 0 {
		t.Fatalf("a run whose upload does not verify completed: %+v", report)
	}
	w.sourceIntact("after a bad upload")
	w.localCopyIntact("after a bad upload")
	if row, ok := w.placement(mediumB); ok && row.Status == state.PlacementActive {
		t.Fatalf("a copy that never verified is catalogued on %s: %+v", mediumB, row)
	}
}

// The old copy is retired only after the new one verified: every read of the
// new copy finds the old object and its ACTIVE placement still there.
func TestFromLocalRetiresTheOldCopyOnlyAfterDecryptVerify_1545(t *testing.T) {
	w := newFromLocalWorld(t)
	var reads, missing int
	w.engine.Store = &openProbe{MediumStore: w.engine.Store, onOpen: func(key string) {
		if key != w.keyB {
			return
		}
		reads++
		if p, _ := w.placement(testMedium); !w.medium.has(w.key) || p.Status == state.PlacementGone {
			missing++
		}
	}}
	report := w.run(w.fromLocalPlan(mediumB))
	w.guard.fail()
	if report.Completed != 1 || reads == 0 {
		t.Fatalf("report %+v with %d read(s) of the new copy, want a completed run that read it", report, reads)
	}
	if missing != 0 {
		t.Fatalf("the old copy was already retired at %d of %d read(s) of the new one", missing, reads)
	}
}

// Plan.FromLocal is honoured only with Reencrypt: the plaintext verb of #1291
// has nothing to do with it.
func TestFromLocalWithoutReencryptIsRefused_1545(t *testing.T) {
	w := newFromLocalWorld(t)
	p := placement.Plan{Artifact: w.artifact, DestinationMedium: mediumB, FromLocal: true}
	if err := w.engine.CheckEncryptExisting(w.ctx, p); err == nil {
		t.Fatal("the check accepted FromLocal without Reencrypt")
	}
}

// expireLocal is the local copy's retention ending: the file goes and the
// placement is GONE.
func (w *fromLocalWorld) expireLocal() {
	w.t.Helper()
	if err := os.Remove(w.localPath()); err != nil {
		w.t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(w.root, "journal.db"))
	if err != nil {
		w.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE placements SET status = 'GONE' WHERE medium = 'local'`); err != nil {
		w.t.Fatal(err)
	}
}

// plannedAndConsented is a from-local run killed after the opt-in, before
// the upload: a live, consented move at PLANNED.
func (w *fromLocalWorld) plannedAndConsented() state.Move {
	w.t.Helper()
	mv, err := w.journal.PlanMove(w.ctx, state.MovePlan{
		Artifact: w.artifact, SourceMedium: testMedium,
		DestinationMedium: mediumB, DestinationKey: w.keyB, OccurredAt: w.clock,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.journal.RecordEncryptExisting(w.ctx, mv.ID, w.clock); err != nil {
		w.t.Fatal(err)
	}
	return mv
}

// D4: a run that asked for --from-local and resumes a consented move that has
// uploaded nothing yet, whose artifact has lost its local copy, abandons it by
// name instead of quietly reading the old copy (egress the dry run did not
// show). Red until the rule exists.
func TestAResumedFromLocalMoveWhoseLocalCopyIsGoneIsAbandonedNotReadFromTheOldCopy_1545(t *testing.T) {
	w := newFromLocalWorld(t)
	mv := w.plannedAndConsented()
	w.expireLocal()
	opensBefore, uploadsBefore := w.store.opensOf(w.key), w.medium.uploadCount()

	report := w.run(w.fromLocalPlan(mediumB))
	w.guard.fail()
	if report.Abandoned != 1 || report.Completed != 0 || len(report.Outcomes) != 1 ||
		!errors.Is(report.Outcomes[0].Err, placement.ErrReencryptLocalCopyGone) {
		t.Fatalf("report %+v, want the one move abandoned with ErrReencryptLocalCopyGone", report)
	}
	if got, _ := w.journal.GetMove(w.ctx, mv.ID); got.Phase != string(placement.Abandoned) || !strings.Contains(got.Error, "run without --from-local") {
		t.Fatalf("the move row is %+v, want ABANDONED with the sentence", got)
	}
	if n := w.store.opensOf(w.key) - opensBefore; n != 0 {
		t.Errorf("the old copy was read %d time(s)", n)
	}
	if n := w.medium.uploadCount() - uploadsBefore; n != 0 {
		t.Errorf("%d upload(s)", n)
	}
	w.sourceIntact("after the abandon")
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Errorf("the staging area holds %v", left)
	}
}

// The way out the sentence names: a run without --from-local resumes the same
// move as an ordinary re-encryption and reads the old copy, which is the
// explicit choice to pay for it. Green today and has to stay.
func TestAResumedMoveWhoseLocalCopyIsGoneStillFinishesWithoutFromLocal_1545(t *testing.T) {
	w := newFromLocalWorld(t)
	w.plannedAndConsented()
	w.expireLocal()
	opensBefore := w.store.opensOf(w.key)
	report := w.run(w.plan(mediumB))
	w.guard.fail()
	if report.Completed != 1 || w.store.opensOf(w.key)-opensBefore == 0 {
		t.Fatalf("report %+v, want it finished by reading the old copy", report)
	}
}

// markLocalGone is the retention pass finally marking the missing local copy.
func (w *fromLocalWorld) markLocalGone() {
	w.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(w.root, "journal.db"))
	if err != nil {
		w.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE placements SET status = 'GONE' WHERE medium = 'local'`); err != nil {
		w.t.Fatal(err)
	}
}

// The wedge: the local placement is still ACTIVE but the file is missing. The
// resumed move derives "from local" from the journal, so every run (with or
// without --from-local) used to fail the copy at COPYING for ever. A missing
// file is the store answering "not there" (artifactstore.ErrNotPresent), which
// is told apart from a transient I/O error, so the move is abandoned by name:
// the old copy ACTIVE, the destination and staging discarded. Red until then.
func TestAMissingLocalFileAbandonsTheResumedMoveByNameWhateverTheFlag_1545(t *testing.T) {
	for _, fromLocalRun := range []bool{true, false} {
		w := newFromLocalWorld(t)
		mv := w.plannedAndConsented()
		if err := os.Remove(w.localPath()); err != nil { // the placement stays ACTIVE
			t.Fatal(err)
		}
		opensBefore, uploadsBefore := w.store.opensOf(w.key), w.medium.uploadCount()
		plan := w.plan(mediumB)
		if fromLocalRun {
			plan = w.fromLocalPlan(mediumB)
		}
		report := w.run(plan)
		w.guard.fail()
		got, _ := w.journal.GetMove(w.ctx, mv.ID)
		if got.Phase != string(placement.Abandoned) || !strings.Contains(got.Error, "run without --from-local") || !strings.Contains(got.Error, "missing, changed or gone") {
			t.Fatalf("from-local run=%v: report %+v, move %+v, want ABANDONED with the sentence", fromLocalRun, report, got)
		}
		if len(report.Outcomes) != 1 || !errors.Is(report.Outcomes[0].Err, placement.ErrReencryptLocalCopyGone) {
			t.Errorf("the outcome %+v does not carry ErrReencryptLocalCopyGone (the CLI and API say local_copy_gone from it)", report.Outcomes)
		}
		if n := w.store.opensOf(w.key) - opensBefore; n != 0 {
			t.Errorf("the old copy was read %d time(s)", n)
		}
		if n := w.medium.uploadCount() - uploadsBefore; n != 0 {
			t.Errorf("%d upload(s)", n)
		}
		w.sourceIntact("after the abandon")
		if w.medium.has(w.keyB) || len(w.stagingLeftovers()) != 0 {
			t.Error("a destination object or staging file is left behind")
		}

		// The sentence's instruction is true: while the local placement is
		// still ACTIVE the plain run is refused (two ACTIVE copies), and once
		// it is GONE the plain run is an ordinary re-encryption that converges.
		if r := w.run(w.plan(mediumB)); r.Refused != 1 || r.Completed != 0 {
			t.Fatalf("the plain run with the local placement ACTIVE: %+v, want refused", r)
		}
		w.markLocalGone()
		opensBefore = w.store.opensOf(w.key)
		if r := w.run(w.plan(mediumB)); r.Completed != 1 || w.store.opensOf(w.key)-opensBefore == 0 {
			t.Fatalf("the plain run after the local copy is GONE: %+v, want it converged by reading the old copy", r)
		}
		w.guard.fail()
	}
}

// A changed local file is likewise abandoned by name: the digest check is
// deterministic, unlike an I/O error.
func TestAChangedLocalFileAbandonsTheResumedMoveByName_1545(t *testing.T) {
	w := newFromLocalWorld(t)
	mv := w.plannedAndConsented()
	if err := os.WriteFile(w.localPath(), append([]byte("x"), w.content...), 0o600); err != nil {
		t.Fatal(err)
	}
	w.run(w.fromLocalPlan(mediumB))
	w.guard.fail()
	if got, _ := w.journal.GetMove(w.ctx, mv.ID); got.Phase != string(placement.Abandoned) || !strings.Contains(got.Error, "missing, changed or gone") {
		t.Fatalf("move %+v, want ABANDONED by name", got)
	}
	w.sourceIntact("after the abandon")
}

// A local file that cannot be read for another reason (here, no permission) is
// not known to be gone: it is not abandoned by name, the old copy is not read,
// and nothing is retired.
func TestAnUnreadableLocalFileIsNotCalledGone_1545(t *testing.T) {
	w := newFromLocalWorld(t)
	mv := w.plannedAndConsented()
	if err := os.Chmod(w.localPath(), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(w.localPath(), 0o600) })
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 0 file")
	}
	opensBefore := w.store.opensOf(w.key)
	w.run(w.fromLocalPlan(mediumB))
	w.guard.fail()
	if got, _ := w.journal.GetMove(w.ctx, mv.ID); strings.Contains(got.Error, "missing, changed or gone") {
		t.Fatalf("an unreadable file was called gone: %+v", got)
	}
	if n := w.store.opensOf(w.key) - opensBefore; n != 0 {
		t.Errorf("the old copy was read %d time(s)", n)
	}
	w.sourceIntact("after an unreadable local file")
}
