package movecrash_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"

	_ "modernc.org/sqlite"
)

// The crash cells for #1545: re-encrypting an artifact that still has an ACTIVE
// local plaintext copy, by writing the new copy from that local copy
// (Plan.FromLocal, the harness's -from-local). A real process is killed at each
// durable boundary of the run and the real engine restarts over what it left.
//
// The world is #1317's (an encrypted copy on the first medium under key 1,
// rotated to key 2) plus the local copy the first hop retired, written back and
// journaled ACTIVE again, which is an artifact inside its local retention.
//
// What every cell asserts, beyond the kill having fired where it names:
//
//   - at the crash, every ACTIVE copy is readable: the local file is
//     byte-identical, and the old ciphertext is on the first medium untouched
//     until its retirement has actually been issued;
//   - the restart finishes (or, for the kills before any upload, finishes the
//     whole run) to DONE: the object on the second medium decrypts under key 2
//     and not under key 1, the old object is gone, staging is empty;
//   - the local copy's bytes, file and ACTIVE placement are exactly as they
//     were, because this run never touches it.

func (w *world) keepLocalCopy() {
	w.t.Helper()
	if w.localExists() {
		w.t.Fatal("control: the first hop left the local copy behind")
	}
	if err := os.WriteFile(w.localPath(), w.content, 0o600); err != nil {
		w.t.Fatal(err)
	}
	db, err := sql.Open("sqlite", w.journal)
	if err != nil {
		w.t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Exec(`UPDATE placements SET status = 'ACTIVE', location = ? WHERE medium = 'local'`, w.localPath())
	if err != nil {
		w.t.Fatalf("re-activating the local placement: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		w.t.Fatalf("control: %d local placement rows, want one", n)
	}
}

func (w *world) fromLocalPlans() []placement.Plan {
	return []placement.Plan{{Artifact: w.artifact, DestinationMedium: crashSecondMedium, Reencrypt: true, FromLocal: true}}
}

func (w *world) localCopyIntact(j *state.Journal, when string) {
	w.t.Helper()
	got, err := os.ReadFile(w.localPath())
	if err != nil || !bytes.Equal(got, w.content) {
		w.t.Fatalf("%s: the local copy is gone or changed (err=%v)", when, err)
	}
	rec, err := j.Get(w.ctx, w.artifact)
	if err != nil {
		w.t.Fatal(err)
	}
	p, ok := rec.LocalPlacement()
	if !ok || p.Status != state.PlacementActive {
		w.t.Fatalf("%s: the local placement is %+v (ok=%v), want ACTIVE", when, p, ok)
	}
}

func TestCrashMatrixOverReencryptingFromTheLocalCopy_1545(t *testing.T) {
	for _, cell := range []struct {
		name        string
		kill        []string
		reason      string
		proves      string
		oldSurvives bool
		freshObject bool
	}{
		{
			name:        "L1 the opt-in is durable, nothing has been uploaded",
			kill:        []string{"-kill-after-consent"},
			reason:      "the operator's opt-in for a move committed",
			proves:      "a kill before the upload leaves both copies as they were and the next run does the whole job from the local copy",
			oldSurvives: true,
		},
		{
			name:        "L2 part-way through reading the local copy into the encrypting pass",
			kill:        []string{"-kill-during-encrypt"},
			reason:      "was interrupted in flight",
			proves:      "an interrupted read of the local copy leaves at most a temporary ciphertext file, uploads nothing, and the resumed run starts the read again",
			oldSurvives: true,
		},
		{
			name:        "L3 the instant the new ciphertext is staged, before the upload",
			kill:        []string{"-kill-before-upload"},
			reason:      "was about to be uploaded",
			proves:      "a finished staging object nobody uploaded is replaced by a fresh encryption of the local copy, never resumed or trusted",
			oldSurvives: true,
		},
		{
			name:        "L4 the instant the upload returns, before anything is journaled",
			kill:        []string{"-kill-after-copy"},
			reason:      "returned, before anything was journaled",
			proves:      "an uploaded object with no record naming it is replaced by a fresh encryption, and the old copy is untouched until the new one verifies",
			oldSurvives: true,
			freshObject: true,
		},
		{
			name:        "L5 after COPIED is durably recorded, before verification",
			kill:        []string{"-kill-after-phase=COPIED"},
			reason:      "the phase write",
			proves:      "an unverified new ciphertext has no placement and no record, and the restart decrypt-verifies it before recording anything",
			oldSurvives: true,
		},
		{
			name:        "L6 after VERIFIED is durably recorded, before the old copy is retired",
			kill:        []string{"-kill-after-phase=VERIFIED"},
			reason:      "the phase write",
			proves:      "the new copy and its record exist and the old copy is still there because retiring it has not been decided",
			oldSurvives: true,
		},
		{
			name:        "L7 after the retirement of the old copy is decided, before it is issued",
			kill:        []string{"-kill-after-phase=SOURCE_DELETE_PENDING"},
			reason:      "the phase write",
			proves:      "the old ciphertext survives a crash between deciding to retire it and retiring it, and the restart re-verifies the new copy before deleting",
			oldSurvives: true,
		},
		{
			name:   "L8 the instant the old object is deleted, before anything is journaled",
			kill:   []string{"-kill-after-object-delete"},
			reason: "was removed, before anything was journaled",
			proves: "with the old copy gone the verified new copy and the local copy remain, and the restart converges to DONE without a second delete",
		},
	} {
		t.Run(cell.name, func(t *testing.T) {
			w, oldKey, oldBytes := reencWorld(t)
			w.keepLocalCopy()

			res := w.crashTo(crashSecondMedium, append([]string{"-plan", "-encrypt-existing", "-reencrypt", "-from-local"}, cell.kill...)...)
			if !res.killed() {
				t.Fatalf("the harness was not killed by SIGKILL (err=%v)\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
			}
			if !strings.Contains(res.stderr, "MOVECRASH_SELF_KILL: ") || !strings.Contains(res.stderr, cell.reason) {
				t.Fatalf("the process died, but not at %q, so this cell is not testing the boundary it names:\n%s", cell.reason, res.stderr)
			}
			if v := res.violations(); len(v) > 0 {
				t.Fatalf("the crashed process reported invariant violations: %v", v)
			}

			j := w.journalNow()
			w.localCopyIntact(j, "at the crash ("+cell.name+")")
			if cell.oldSurvives {
				w.oldCopyIntact(j, oldKey, oldBytes, "at the crash ("+cell.name+")")
			}
			first := w.ciphertextObject()
			w.checkInvariantNow(j, "at the instant of the crash ("+cell.name+")")
			w.retroAgree(j, "at the instant of the crash ("+cell.name+")")

			keys := w.keysOver(j)
			engine, guard := w.encryptedRestart(j, keys)
			report, err := engine.EncryptExisting(w.ctx, w.fromLocalPlans())
			if err != nil {
				t.Fatalf("the restart failed: %v", err)
			}
			guard.fail()
			if report.Completed != 1 {
				t.Fatalf("the restart did not finish the run: %+v\nthis cell proves: %s", report, cell.proves)
			}
			moves, err := j.MovesForArtifact(w.ctx, w.artifact)
			if err != nil || len(moves) != 2 || placement.Phase(moves[len(moves)-1].Phase) != placement.Done {
				t.Fatalf("the move did not converge to DONE: %+v (err=%v)\nthis cell proves: %s", moves, err, cell.proves)
			}
			w.retroAgree(j, "after the restart")
			w.localCopyIntact(j, "after the restart")

			obj := w.ciphertextObject()
			active := mustActive(t, keys, w.ctx)
			if err := keys.WithKey(w.ctx, active, func(k []byte) error {
				_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), copycrypt.AgeX25519{}, k, w.hash)
				return err
			}); err != nil {
				t.Fatalf("the object on the second medium does not decrypt to the artifact under the active key: %v\nthis cell proves: %s", err, cell.proves)
			}
			if err := keys.WithKey(w.ctx, oldKey, func(k []byte) error {
				_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), copycrypt.AgeX25519{}, k, w.hash)
				return err
			}); !errors.Is(err, copycrypt.ErrWrongKey) {
				t.Fatalf("the object on the second medium under key 1: %v, want ErrWrongKey", err)
			}
			if cell.freshObject && bytes.Equal(obj, first) {
				t.Fatal("the object is byte-identical to the dead process's; a re-copy must be a fresh encryption")
			}
			enc, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashSecondMedium)
			if err != nil || !ok || enc.KeyID != active || enc.StoredHash != hexSum(obj) || enc.StoredSize != int64(len(obj)) {
				t.Fatalf("the record after convergence is %+v (ok=%v err=%v), want it under key %s describing the object", enc, ok, err, active)
			}
			if len(w.plaintextObject()) != 0 {
				t.Error("the old ciphertext is still on the first medium after a completed run")
			}
			if left := w.stagingLeftovers(); len(left) != 0 {
				t.Errorf("the staging area holds %v after the restart\nthis cell proves: %s", left, cell.proves)
			}
			if n := w.objectCountOn(t, w.bucket); n != 0 {
				t.Errorf("%d object(s) left on the first medium after a completed run", n)
			}
			if n := w.objectCountOn(t, w.secondBucket); n != 1 {
				t.Errorf("%d object(s) on the second medium, want exactly one", n)
			}
		})
	}
}

// The anti-vacuity control: with the kill suppressed the run reaches the
// consent kill point, finishes, and what it leaves is the whole claim.
func TestTheFromLocalCrashCellsReallyUploadFromTheLocalCopy_1545(t *testing.T) {
	w, oldKey, oldBytes := reencWorld(t)
	w.keepLocalCopy()

	res := w.crashTo(crashSecondMedium, "-plan", "-encrypt-existing", "-reencrypt", "-from-local", "-kill-after-consent", "-suppress-kill")
	if res.killed() || !strings.Contains(res.stdout, "FINISHED") {
		t.Fatalf("the suppressed run did not finish:\n%s\n%s", res.stdout, res.stderr)
	}
	obj := w.ciphertextObject()
	if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) || bytes.Equal(obj, oldBytes) {
		t.Fatalf("an uninterrupted run left something other than new age ciphertext on the second medium (%d bytes)", len(obj))
	}
	j := w.journalNow()
	keys := w.keysOver(j)
	if err := keys.WithKey(w.ctx, oldKey, func(k []byte) error {
		_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), copycrypt.AgeX25519{}, k, w.hash)
		return err
	}); !errors.Is(err, copycrypt.ErrWrongKey) {
		t.Fatalf("the new object under key 1: %v, want ErrWrongKey", err)
	}
	if len(w.plaintextObject()) != 0 {
		t.Fatal("the old object survived a completed run")
	}
	w.localCopyIntact(j, "after an uninterrupted run")
}

// expireLocalCopy is the local copy's retention ending between the crash and
// the re-run: the file goes and the placement is GONE.
func (w *world) expireLocalCopy() {
	w.t.Helper()
	if err := os.Remove(w.localPath()); err != nil {
		w.t.Fatal(err)
	}
	db, err := sql.Open("sqlite", w.journal)
	if err != nil {
		w.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE placements SET status = 'GONE' WHERE medium = 'local'`); err != nil {
		w.t.Fatal(err)
	}
}

// resolvedKeys counts every key custody was asked to resolve, so a cell can say
// the old key was never touched.
type resolvedKeys struct {
	placement.Keys
	asked map[string]int
}

func (k *resolvedKeys) WithKey(ctx context.Context, id string, fn func([]byte) error) error {
	k.asked[id]++
	return k.Keys.WithKey(ctx, id, fn)
}

// D4 (#1545): the local copy expires between a kill and the re-run. Before
// anything was uploaded, a --from-local re-run abandons the move by name rather
// than reading the old copy; the old copy stays ACTIVE and is never opened.
func TestCrashCellLocalCopyExpiresBeforeTheUploadAbandonsByName_1545(t *testing.T) {
	w, oldKey, oldBytes := reencWorld(t)
	w.keepLocalCopy()
	res := w.crashTo(crashSecondMedium, "-plan", "-encrypt-existing", "-reencrypt", "-from-local", "-kill-after-consent")
	if !res.killed() || !strings.Contains(res.stderr, "the operator's opt-in for a move committed") {
		t.Fatalf("the harness was not killed at the opt-in:\n%s", res.stderr)
	}
	w.expireLocalCopy()

	j := w.journalNow()
	keys := &resolvedKeys{Keys: w.keysOver(j), asked: map[string]int{}}
	engine, guard := w.encryptedRestart(j, w.keysOver(j))
	engine.Keys = keys
	report, err := engine.EncryptExisting(w.ctx, w.fromLocalPlans())
	if err != nil {
		t.Fatal(err)
	}
	guard.fail()
	if report.Abandoned != 1 || report.Completed != 0 || len(report.Outcomes) != 1 ||
		!errors.Is(report.Outcomes[0].Err, placement.ErrReencryptLocalCopyGone) {
		t.Fatalf("report %+v, want the move abandoned with ErrReencryptLocalCopyGone", report)
	}
	moves, err := j.MovesForArtifact(w.ctx, w.artifact)
	last := moves[len(moves)-1]
	if err != nil || placement.Phase(last.Phase) != placement.Abandoned || !strings.Contains(last.Error, "run without --from-local") {
		t.Fatalf("the last move is %+v (err=%v), want ABANDONED with the sentence", last, err)
	}
	w.oldCopyIntact(j, oldKey, oldBytes, "after the abandon")
	if n := w.store.opensOf(crashMedium, w.key); n != 0 {
		t.Errorf("the old copy was opened %d time(s)", n)
	}
	if keys.asked[oldKey] != 0 {
		t.Errorf("the old key was resolved %d time(s)", keys.asked[oldKey])
	}
	if len(w.ciphertextObject()) != 0 {
		t.Error("an object is on the second medium")
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Errorf("the staging area holds %v", left)
	}
	w.checkInvariantNow(j, "after the abandon")
}

// Its control: the same expiry after COPIED. The upload from the local copy
// already happened, so the re-run finishes it, reads the old copy zero times
// and never resolves the old key.
func TestCrashCellLocalCopyExpiresAfterCopiedStillFinishes_1545(t *testing.T) {
	w, oldKey, _ := reencWorld(t)
	w.keepLocalCopy()
	res := w.crashTo(crashSecondMedium, "-plan", "-encrypt-existing", "-reencrypt", "-from-local", "-kill-after-phase=COPIED")
	if !res.killed() {
		t.Fatalf("the harness was not killed:\n%s", res.stderr)
	}
	w.expireLocalCopy()

	j := w.journalNow()
	keys := &resolvedKeys{Keys: w.keysOver(j), asked: map[string]int{}}
	engine, guard := w.encryptedRestart(j, w.keysOver(j))
	engine.Keys = keys
	report, err := engine.EncryptExisting(w.ctx, w.fromLocalPlans())
	if err != nil {
		t.Fatal(err)
	}
	guard.fail()
	if report.Completed != 1 {
		t.Fatalf("report %+v, want the move finished", report)
	}
	if n := w.store.opensOf(crashMedium, w.key); n != 0 {
		t.Errorf("the old copy was opened %d time(s)", n)
	}
	if keys.asked[oldKey] != 0 {
		t.Errorf("the old key was resolved %d time(s)", keys.asked[oldKey])
	}
	if len(w.plaintextObject()) != 0 || len(w.ciphertextObject()) == 0 {
		t.Error("the old object should be gone and the new one present")
	}
}
