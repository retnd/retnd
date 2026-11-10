package movecrash_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
)

// The crash cells for #1291: encrypting a retained copy that already sits on
// a medium in the clear, after the set was switched to "required". A real
// process is killed at every durable boundary of Engine.EncryptExisting, and
// the real engine restarts over what it left.
//
// The world is built with the product's own moves: the artifact is moved for
// real onto the first medium while the set does not require encryption (so
// the copy there is plaintext), then a key is minted and the set is run as
// "required" for the hop to the second medium. Both mediums give one artifact
// the same key, so every lookup is by bucket.
//
// What every cell asserts, beyond what the encrypted matrix asserts:
//
//   - the kill really fired inside the phase the cell names (the harness's
//     stderr carries the self-kill line and its reason, and the process died
//     of SIGKILL), because "the plaintext survived" is trivially true of a
//     run that never started;
//   - until the encrypted copy has been verified and the retirement has been
//     decided, the plaintext object is still on the first medium and
//     byte-identical to what it was;
//   - after the restart the move is DONE, the object on the second medium is
//     age ciphertext that decrypts to the journal's digest under the key
//     custody holds, its record describes that object, the plaintext object is
//     gone, and the staging area is empty.
//
// The tests were written by reading the harness and the engine and have not
// been run.

func (w *world) plaintextObject() []byte {
	b, err := os.ReadFile(filepath.Join(w.bucket, filepath.FromSlash(w.key)))
	if err != nil {
		return nil
	}
	return b
}

func (w *world) ciphertextObject() []byte {
	b, err := os.ReadFile(filepath.Join(w.secondBucket, filepath.FromSlash(w.key)))
	if err != nil {
		return nil
	}
	return b
}

// retroWorld is a world whose artifact is plaintext on the first medium and
// whose next run is under a required set with a freshly minted key.
func retroWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.firstHop()
	if !bytes.Equal(w.plaintextObject(), w.content) {
		t.Fatal("control: the object on the first medium is not the plaintext artifact, so this world holds no plaintext retained copy")
	}
	w.enableEncryption()
	return w
}

func (w *world) retroPlans() []placement.Plan {
	return []placement.Plan{{Artifact: w.artifact, DestinationMedium: crashSecondMedium}}
}

func (w *world) retroAgree(j *state.Journal, when string) {
	w.t.Helper()
	rec, err := j.Get(w.ctx, w.artifact)
	if err != nil {
		w.t.Fatalf("reading the journal %s: %v", when, err)
	}
	var active bool
	for _, p := range rec.Placements {
		if p.Medium == crashSecondMedium && p.Status == state.PlacementActive {
			active = true
		}
	}
	_, hasRecord, err := j.PlacementEncryption(w.ctx, w.artifact, crashSecondMedium)
	if err != nil {
		w.t.Fatalf("reading the encryption record %s: %v", when, err)
	}
	if active != hasRecord {
		w.t.Fatalf("%s: ACTIVE placement on the second medium = %t but record = %t; the two are one write", when, active, hasRecord)
	}
}

func TestCrashMatrixOverEncryptingAnExistingPlaintextCopy_1291(t *testing.T) {
	for _, cell := range []struct {
		name string
		kill []string
		// reason is a fragment of the harness's self-kill line, so a cell
		// that dies somewhere other than where it says is red.
		reason string
		proves string
		// plaintextSurvives is true for every boundary before the delete of
		// the plaintext object has been issued.
		plaintextSurvives bool
		freshObject       bool
		afterCrash        func(t *testing.T, w *world)
	}{
		{
			name:              "R1 the move is planned, the opt-in is not yet written",
			kill:              []string{"-kill-after-plan"},
			reason:            "the durable intent to move",
			proves:            "a move with no recorded opt-in is adopted by the next explicit run for the same artifact and destination, and nothing was copied meanwhile",
			plaintextSurvives: true,
			afterCrash: func(t *testing.T, w *world) {
				if n := w.objectCountOn(t, w.secondBucket); n != 0 {
					t.Fatalf("%d object(s) on the second medium; the kill landed after a copy", n)
				}
				j := w.journalNow()
				live, err := j.ListMoves(w.ctx, placement.NonTerminalPhaseStrings()...)
				if err != nil || len(live) != 1 {
					t.Fatalf("%d live move(s) (err=%v), want the one that was planned", len(live), err)
				}
				if ok, err := j.EncryptExistingConsented(w.ctx, live[0].ID); err != nil || ok {
					t.Fatalf("the move reads as opted in before the opt-in was written (ok=%v err=%v)", ok, err)
				}
			},
		},
		{
			name:              "R2 the opt-in is durable, nothing has been copied",
			kill:              []string{"-kill-after-consent"},
			reason:            "the operator's opt-in for a move committed",
			proves:            "a recorded opt-in is what lets the resumed copy encrypt, with the plaintext object untouched in between",
			plaintextSurvives: true,
			afterCrash: func(t *testing.T, w *world) {
				j := w.journalNow()
				live, err := j.ListMoves(w.ctx, placement.NonTerminalPhaseStrings()...)
				if err != nil || len(live) != 1 {
					t.Fatalf("%d live move(s) (err=%v), want one", len(live), err)
				}
				if ok, err := j.EncryptExistingConsented(w.ctx, live[0].ID); err != nil || !ok {
					t.Fatalf("the opt-in is not durable at the crash (ok=%v err=%v)", ok, err)
				}
			},
		},
		{
			name:              "R3 part-way through reading the plaintext object into the encrypting pass",
			kill:              []string{"-kill-during-stage"},
			reason:            "was interrupted in flight",
			proves:            "an interrupted encrypt leaves only a temporary file in the staging area, uploads nothing, and the resumed copy re-encrypts from the plaintext object",
			plaintextSurvives: true,
			afterCrash: func(t *testing.T, w *world) {
				left := w.stagingLeftovers()
				if len(left) != 1 || !strings.HasPrefix(left[0], copycrypt.TempPrefix) {
					t.Fatalf("the staging area holds %v; an interrupted encrypt leaves exactly one temporary file", left)
				}
				if n := w.objectCountOn(t, w.secondBucket); n != 0 {
					t.Fatalf("%d object(s) on the second medium; the kill landed after the upload", n)
				}
			},
		},
		{
			name:              "R4 the instant the staged ciphertext is complete, before the upload",
			kill:              []string{"-kill-before-upload"},
			reason:            "was about to be uploaded",
			proves:            "a finished staging object nobody uploaded is replaced by a fresh encryption, never resumed or trusted",
			plaintextSurvives: true,
			afterCrash: func(t *testing.T, w *world) {
				left := w.stagingLeftovers()
				if len(left) != 1 || strings.HasPrefix(left[0], copycrypt.TempPrefix) {
					t.Fatalf("the staging area holds %v; a finished encrypt leaves exactly one object under its own name", left)
				}
				if n := w.objectCountOn(t, w.secondBucket); n != 0 {
					t.Fatalf("%d object(s) on the second medium; the kill landed after the upload", n)
				}
			},
		},
		{
			name:              "R5 the instant the upload returns, before anything is journaled",
			kill:              []string{"-kill-after-copy"},
			reason:            "returned, before anything was journaled",
			proves:            "ciphertext at the key with no record naming it is replaced by a fresh encryption, and the recorded digest is the second object's",
			plaintextSurvives: true,
			freshObject:       true,
			afterCrash: func(t *testing.T, w *world) {
				if len(w.ciphertextObject()) == 0 {
					t.Fatal("no object on the second medium; the kill landed before the upload returned")
				}
				if _, ok, err := w.journalNow().PlacementEncryption(w.ctx, w.artifact, crashSecondMedium); err != nil || ok {
					t.Fatalf("a record exists for an object nobody verified (ok=%v err=%v)", ok, err)
				}
			},
		},
		{
			name:              "R6 after COPIED is durably recorded, before verification",
			kill:              []string{"-kill-after-phase=COPIED"},
			reason:            "the phase write",
			proves:            "an unverified ciphertext has no placement and no record, so nothing can rely on it, and the restart decrypt-verifies before recording anything",
			plaintextSurvives: true,
			afterCrash: func(t *testing.T, w *world) {
				if _, ok, err := w.journalNow().PlacementEncryption(w.ctx, w.artifact, crashSecondMedium); err != nil || ok {
					t.Fatalf("a record exists at COPIED (ok=%v err=%v)", ok, err)
				}
			},
		},
		{
			name:              "R7 after VERIFIED is durably recorded",
			kill:              []string{"-kill-after-phase=VERIFIED"},
			reason:            "the phase write",
			proves:            "the placement and the record were written by one advance, and the plaintext object is still there because retiring it has not even been decided",
			plaintextSurvives: true,
			afterCrash: func(t *testing.T, w *world) {
				if _, ok, err := w.journalNow().PlacementEncryption(w.ctx, w.artifact, crashSecondMedium); err != nil || !ok {
					t.Fatalf("no record at VERIFIED (ok=%v err=%v)", ok, err)
				}
			},
		},
		{
			name:              "R8 after the retirement of the plaintext is decided, before it is issued",
			kill:              []string{"-kill-after-phase=SOURCE_DELETE_PENDING"},
			reason:            "the phase write",
			proves:            "the plaintext object survives a crash between deciding to retire it and retiring it, and the restart re-verifies the encrypted copy before deleting",
			plaintextSurvives: true,
			afterCrash: func(t *testing.T, w *world) {
				rec, err := w.journalNow().Get(w.ctx, w.artifact)
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range rec.Placements {
					if p.Medium == crashMedium && p.Status != state.PlacementDeletePending {
						t.Fatalf("the plaintext placement is %s, want DELETE_PENDING", p.Status)
					}
				}
			},
		},
		{
			name:   "R9 the instant the plaintext object is deleted, before anything is journaled",
			kill:   []string{"-kill-after-object-delete"},
			reason: "was removed, before anything was journaled",
			proves: "with the plaintext gone, the verified encrypted copy is already the only one, and the restart converges to DONE without a second delete",
			afterCrash: func(t *testing.T, w *world) {
				if len(w.plaintextObject()) != 0 {
					t.Fatal("the plaintext object is still on the first medium; the kill landed before the delete")
				}
				if !bytes.HasPrefix(w.ciphertextObject(), []byte("age-encryption.org/v1\n")) {
					t.Fatal("the plaintext is gone and there is no ciphertext: the only copy was lost")
				}
				if _, ok, err := w.journalNow().PlacementEncryption(w.ctx, w.artifact, crashSecondMedium); err != nil || !ok {
					t.Fatalf("the plaintext was deleted with no verified record behind it (ok=%v err=%v)", ok, err)
				}
			},
		},
	} {
		t.Run(cell.name, func(t *testing.T) {
			w := retroWorld(t)

			res := w.crashTo(crashSecondMedium, append([]string{"-plan", "-encrypt-existing"}, cell.kill...)...)
			if !res.killed() {
				t.Fatalf("the harness was not killed by SIGKILL (err=%v)\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
			}
			if !strings.Contains(res.stderr, "MOVECRASH_SELF_KILL: ") || !strings.Contains(res.stderr, cell.reason) {
				t.Fatalf("the process died, but not at %q, so this cell is not testing the boundary it names:\n%s", cell.reason, res.stderr)
			}
			if v := res.violations(); len(v) > 0 {
				t.Fatalf("the crashed process reported invariant violations: %v", v)
			}

			if cell.plaintextSurvives && !bytes.Equal(w.plaintextObject(), w.content) {
				t.Fatalf("the plaintext object is gone or changed at the crash\nthis cell proves: %s", cell.proves)
			}
			first := w.ciphertextObject()
			if cell.afterCrash != nil {
				cell.afterCrash(t, w)
			}

			j := w.journalNow()
			w.checkInvariantNow(j, "at the instant of the crash ("+cell.name+")")
			w.retroAgree(j, "at the instant of the crash ("+cell.name+")")

			keys := w.keysOver(j)
			engine, guard := w.encryptedRestart(j, keys)
			report, err := engine.EncryptExisting(w.ctx, w.retroPlans())
			if err != nil {
				t.Fatalf("the restart failed: %v", err)
			}
			guard.fail()
			if report.Resumed != 1 {
				t.Fatalf("the restart did not pick the move up: %+v\nthis cell proves: %s", report, cell.proves)
			}

			moves, err := j.MovesForArtifact(w.ctx, w.artifact)
			if err != nil || len(moves) != 2 || placement.Phase(moves[len(moves)-1].Phase) != placement.Done {
				t.Fatalf("the move did not converge to DONE: %+v (err=%v)\nthis cell proves: %s", moves, err, cell.proves)
			}
			w.retroAgree(j, "after the restart")

			obj := w.ciphertextObject()
			if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) {
				t.Fatalf("the object on the second medium is not age ciphertext\nthis cell proves: %s", cell.proves)
			}
			if err := keys.WithKey(w.ctx, mustActive(t, keys, w.ctx), func(k []byte) error {
				_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), k, w.hash)
				return err
			}); err != nil {
				t.Fatalf("the object on the second medium does not decrypt to the artifact: %v", err)
			}
			if cell.freshObject && bytes.Equal(obj, first) {
				t.Fatal("the object is byte-identical to the dead process's; a re-copy must be a fresh encryption")
			}
			enc, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashSecondMedium)
			if err != nil || !ok {
				t.Fatalf("no record after convergence (ok=%v err=%v)", ok, err)
			}
			if enc.StoredHash != hexSum(obj) || enc.StoredSize != int64(len(obj)) || enc.StoredHash == w.hash {
				t.Fatalf("recorded digest/size %s/%d do not describe the object at the key (%s/%d)", enc.StoredHash, enc.StoredSize, hexSum(obj), len(obj))
			}
			if left := w.stagingLeftovers(); len(left) != 0 {
				t.Errorf("the staging area holds %v after the restart\nthis cell proves: %s", left, cell.proves)
			}
			if n := w.objectCountOn(t, w.bucket); n != 0 {
				t.Errorf("%d plaintext object(s) left on the first medium after a completed run", n)
			}
			if n := w.objectCountOn(t, w.secondBucket); n != 1 {
				t.Errorf("%d object(s) on the second medium, want exactly one", n)
			}
		})
	}
}

// An ordinary cycle that finds a move nobody opted in to must not finish it as
// an encrypted copy, and must not finish it as a plaintext one: it abandons it
// with the plaintext untouched. Red if the engine treats a half-started
// encrypt-existing move as permission.
func TestAnOrdinaryCycleNeverCompletesAMoveWithNoOptIn_1291(t *testing.T) {
	w := retroWorld(t)
	res := w.crashTo(crashSecondMedium, "-plan", "-encrypt-existing", "-kill-after-plan")
	if !res.killed() || !strings.Contains(res.stderr, "the durable intent to move") {
		t.Fatalf("control: the process did not die after the plan:\n%s", res.stderr)
	}

	j := w.journalNow()
	keys := w.keysOver(j)
	engine, guard := w.encryptedRestart(j, keys)
	if _, err := engine.RunCycle(w.ctx, nil); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	guard.fail()

	moves, err := j.MovesForArtifact(w.ctx, w.artifact)
	if err != nil || len(moves) != 2 || placement.Phase(moves[len(moves)-1].Phase) != placement.Abandoned {
		t.Fatalf("the unconsented move is %+v (err=%v), want ABANDONED", moves, err)
	}
	if !bytes.Equal(w.plaintextObject(), w.content) {
		t.Fatal("the plaintext object is gone or changed")
	}
	if n := w.objectCountOn(t, w.secondBucket); n != 0 {
		t.Fatalf("%d object(s) on the second medium after an ordinary cycle with no opt-in", n)
	}
}

// A destination that keeps something other than what was uploaded must never
// cost the plaintext copy: the move never gets past verification, nothing is
// catalogued for the bad object, and the plaintext object is untouched.
// -corrupt-after-copy overwrites the destination object after every upload.
func TestACorruptedDestinationNeverRetiresThePlaintext_1291(t *testing.T) {
	w := retroWorld(t)

	res := w.crashTo(crashSecondMedium, "-plan", "-encrypt-existing", "-corrupt-after-copy")
	if res.killed() || !strings.Contains(res.stdout, "FINISHED") {
		t.Fatalf("the run was killed or did not finish:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	if v := res.violations(); len(v) > 0 {
		t.Fatalf("invariant violations: %v", v)
	}
	if !bytes.Equal(w.plaintextObject(), w.content) {
		t.Fatal("the plaintext object is gone or changed after a destination that failed verification")
	}
	j := w.journalNow()
	w.checkInvariantNow(j, "after a corrupted destination")
	if _, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashSecondMedium); err != nil || ok {
		t.Fatalf("a record exists for a corrupted object (ok=%v err=%v)", ok, err)
	}
	rec, err := j.Get(w.ctx, w.artifact)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rec.Placements {
		if p.Medium == crashSecondMedium && p.Status == state.PlacementActive {
			t.Fatalf("an ACTIVE placement exists for a corrupted object: %+v", p)
		}
		if p.Medium == crashMedium && p.Status != state.PlacementActive {
			t.Fatalf("the plaintext placement is %s, want ACTIVE", p.Status)
		}
	}
}

// The control the table cannot carry itself, for the two flags this change
// added: with the kill suppressed, the run reaches the opt-in kill point,
// finishes, and leaves ciphertext on the second medium and none of the
// plaintext on the first. A cell that passes only because the harness never
// ran EncryptExisting at all would fail here.
func TestTheRetroCrashCellsReallyEncryptSomething_1291(t *testing.T) {
	w := retroWorld(t)

	res := w.crashTo(crashSecondMedium, "-plan", "-encrypt-existing", "-kill-after-consent", "-suppress-kill")
	if res.killed() {
		t.Fatal("the harness died even with -suppress-kill set, so the cells' kill flags do not do what they rely on")
	}
	if !strings.Contains(res.stderr, "MOVECRASH_SELF_KILL_SUPPRESSED") {
		t.Fatalf("the harness never reached the opt-in kill point, so the cells are crashing somewhere else:\n%s", res.stderr)
	}
	if !strings.Contains(res.stdout, "FINISHED") {
		t.Fatalf("the suppressed run did not finish:\n%s\n%s", res.stdout, res.stderr)
	}
	obj := w.ciphertextObject()
	if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) {
		t.Fatalf("an uninterrupted encrypt-existing run left something other than age ciphertext on the second medium (%d bytes)", len(obj))
	}
	if bytes.Contains(obj, w.content[:32]) {
		t.Fatal("the plaintext appears in the stored object")
	}
	if len(w.plaintextObject()) != 0 {
		t.Fatal("the plaintext object survived a completed run")
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Errorf("an uninterrupted run left %v in the staging area", left)
	}
}
