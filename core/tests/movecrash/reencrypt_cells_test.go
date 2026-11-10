package movecrash_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
)

// The crash cells for #1317 (EPIC T, T2.2): re-encrypting an ENCRYPTED copy
// under the active key, the encrypted-source kind of Engine.EncryptExisting. A
// real process is killed at every durable boundary, and the real engine
// restarts over what it left.
//
// The world is built with the product's own moves: the artifact is moved for
// real onto the first medium under key 1 (an encrypted copy with its record),
// then custody rotates, so key 1 is superseded and key 2 is active, and the
// run re-encrypts onto the second medium. Both mediums give one artifact the
// same key, so every lookup is by bucket.
//
// What every cell asserts, beyond what the retro matrix asserts:
//
//   - the kill really fired inside the phase the cell names (the self-kill
//     line and its reason, and death by SIGKILL), because "the old copy
//     survived" is trivially true of a run that never started;
//   - until the retirement has been issued, the old ciphertext is still on
//     the first medium, byte-identical, and still recorded under key 1;
//   - after the restart the move is DONE, the object on the second medium
//     decrypts to the journal's digest under key 2 and NOT under key 1 (so it
//     was re-encrypted, not re-homed), its record names key 2 and describes
//     that object (the Content class requirement: decrypt-verify against
//     artifacts.local_hash), the old object is gone and the staging area is
//     empty.
//
// The tests were written by reading the harness and the engine and have not
// been run.

// reencWorld is a world whose artifact is encrypted on the first medium under
// a key that a rotation has since superseded.
func reencWorld(t *testing.T) (w *world, oldKey string, oldBytes []byte) {
	t.Helper()
	w = newWorldOver(t, multiChunkArtifact())
	w.enableEncryption()
	w.firstHop()
	oldBytes = w.plaintextObject() // the object on the first medium, here ciphertext
	if !bytes.HasPrefix(oldBytes, []byte("age-encryption.org/v1\n")) {
		t.Fatal("control: the object on the first medium is not age ciphertext, so this world holds no encrypted copy")
	}
	// The rotation goes through a journal opened and closed here, the way
	// enableEncryption mints the first key, so no handle is held while the
	// harness runs. Key 1 is superseded, not retired: it still opens its copy.
	j, err := state.Open(w.ctx, w.journal)
	if err != nil {
		t.Fatalf("opening the journal: %v", err)
	}
	defer func() { _ = j.Close() }()
	keys := w.keysOver(j)
	oldKey = mustActive(t, keys, w.ctx)
	if _, err := keys.Rotate(w.ctx, crashNow); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if mustActive(t, keys, w.ctx) == oldKey {
		t.Fatal("control: the rotation left key 1 active")
	}
	return w, oldKey, oldBytes
}

func (w *world) reencPlans() []placement.Plan {
	return []placement.Plan{{Artifact: w.artifact, DestinationMedium: crashSecondMedium, Reencrypt: true}}
}

// oldCopyIntact is the claim every cell before the delete makes.
func (w *world) oldCopyIntact(j *state.Journal, oldKey string, oldBytes []byte, when string) {
	w.t.Helper()
	if !bytes.Equal(w.plaintextObject(), oldBytes) {
		w.t.Fatalf("%s: the old ciphertext on the first medium is gone or changed", when)
	}
	enc, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashMedium)
	if err != nil || !ok || enc.KeyID != oldKey {
		w.t.Fatalf("%s: the old copy's record is %+v (ok=%v err=%v), want it under key 1", when, enc, ok, err)
	}
}

func TestCrashMatrixOverReencryptingAnEncryptedCopy_1317(t *testing.T) {
	for _, cell := range []struct {
		name   string
		kill   []string
		reason string
		proves string
		// oldSurvives is true for every boundary before the delete of the old
		// object has been issued.
		oldSurvives bool
		freshObject bool
		afterCrash  func(t *testing.T, w *world)
	}{
		{
			name:        "E1 the move is planned, the opt-in is not yet written",
			kill:        []string{"-kill-after-plan"},
			reason:      "the durable intent to move",
			proves:      "a re-encrypt move with no recorded opt-in is adopted by the next explicit run for the same artifact and destination, and nothing was copied meanwhile",
			oldSurvives: true,
			afterCrash: func(t *testing.T, w *world) {
				if n := w.objectCountOn(t, w.secondBucket); n != 0 {
					t.Fatalf("%d object(s) on the second medium; the kill landed after a copy", n)
				}
			},
		},
		{
			name:        "E2 the opt-in is durable, nothing has been copied",
			kill:        []string{"-kill-after-consent"},
			reason:      "the operator's opt-in for a move committed",
			proves:      "a recorded opt-in is what makes the resumed copy a re-encryption rather than a re-home",
			oldSurvives: true,
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
			name:        "E3 part-way through reading the old ciphertext into the decrypting pass",
			kill:        []string{"-kill-during-stage"},
			reason:      "was interrupted in flight",
			proves:      "an interrupted read of the old ciphertext leaves at most a temporary file in the staging area, uploads nothing, and the resumed copy starts the stream again from the old object",
			oldSurvives: true,
			afterCrash: func(t *testing.T, w *world) {
				for _, f := range w.stagingLeftovers() {
					if !strings.HasPrefix(f, copycrypt.TempPrefix) {
						t.Fatalf("the staging area holds %q, a finished file, after a kill in the middle of the read", f)
					}
				}
				if n := w.objectCountOn(t, w.secondBucket); n != 0 {
					t.Fatalf("%d object(s) on the second medium; the kill landed after the upload", n)
				}
			},
		},
		{
			name:        "E4 the instant the new ciphertext is staged, before the upload",
			kill:        []string{"-kill-before-upload"},
			reason:      "was about to be uploaded",
			proves:      "a finished staging object nobody uploaded is replaced by a fresh encryption, never resumed or trusted",
			oldSurvives: true,
			afterCrash: func(t *testing.T, w *world) {
				left := w.stagingLeftovers()
				if len(left) != 1 || strings.HasPrefix(left[0], copycrypt.TempPrefix) {
					t.Fatalf("the staging area holds %v; a finished encrypt leaves exactly one object under its own name", left)
				}
			},
		},
		{
			name:        "E5 the instant the upload returns, before anything is journaled",
			kill:        []string{"-kill-after-copy"},
			reason:      "returned, before anything was journaled",
			proves:      "new ciphertext at the key with no record naming it is replaced by a fresh encryption, and the recorded digest is the second object's",
			oldSurvives: true,
			freshObject: true,
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
			name:        "E6 after COPIED is durably recorded, before verification",
			kill:        []string{"-kill-after-phase=COPIED"},
			reason:      "the phase write",
			proves:      "an unverified new ciphertext has no placement and no record, and the restart decrypt-verifies it under the active key before recording anything",
			oldSurvives: true,
		},
		{
			name:        "E7 after VERIFIED is durably recorded",
			kill:        []string{"-kill-after-phase=VERIFIED"},
			reason:      "the phase write",
			proves:      "the new placement and its record under key 2 were written by one advance, and the old copy is still there because retiring it has not been decided",
			oldSurvives: true,
			afterCrash: func(t *testing.T, w *world) {
				if _, ok, err := w.journalNow().PlacementEncryption(w.ctx, w.artifact, crashSecondMedium); err != nil || !ok {
					t.Fatalf("no record at VERIFIED (ok=%v err=%v)", ok, err)
				}
			},
		},
		{
			name:        "E8 after the retirement of the old copy is decided, before it is issued",
			kill:        []string{"-kill-after-phase=SOURCE_DELETE_PENDING"},
			reason:      "the phase write",
			proves:      "the old ciphertext survives a crash between deciding to retire it and retiring it, and the restart re-verifies the new copy through its own recorded key before deleting",
			oldSurvives: true,
		},
		{
			name:   "E9 the instant the old object is deleted, before anything is journaled",
			kill:   []string{"-kill-after-object-delete"},
			reason: "was removed, before anything was journaled",
			proves: "with the old copy gone, the verified new copy is already the only one, and the restart converges to DONE without a second delete",
			afterCrash: func(t *testing.T, w *world) {
				if len(w.plaintextObject()) != 0 {
					t.Fatal("the old object is still on the first medium; the kill landed before the delete")
				}
				if _, ok, err := w.journalNow().PlacementEncryption(w.ctx, w.artifact, crashSecondMedium); err != nil || !ok {
					t.Fatalf("the old copy was deleted with no verified record behind the new one (ok=%v err=%v)", ok, err)
				}
			},
		},
	} {
		t.Run(cell.name, func(t *testing.T) {
			w, oldKey, oldBytes := reencWorld(t)

			res := w.crashTo(crashSecondMedium, append([]string{"-plan", "-encrypt-existing", "-reencrypt"}, cell.kill...)...)
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
			if cell.oldSurvives {
				w.oldCopyIntact(j, oldKey, oldBytes, "at the crash ("+cell.name+")")
			}
			first := w.ciphertextObject()
			if cell.afterCrash != nil {
				cell.afterCrash(t, w)
			}
			w.checkInvariantNow(j, "at the instant of the crash ("+cell.name+")")
			w.retroAgree(j, "at the instant of the crash ("+cell.name+")")

			keys := w.keysOver(j)
			engine, guard := w.encryptedRestart(j, keys)
			report, err := engine.EncryptExisting(w.ctx, w.reencPlans())
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
				t.Fatalf("the object on the second medium under key 1: %v, want ErrWrongKey (it was re-homed, not re-encrypted)", err)
			}
			if cell.freshObject && bytes.Equal(obj, first) {
				t.Fatal("the object is byte-identical to the dead process's; a re-copy must be a fresh encryption")
			}
			enc, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashSecondMedium)
			if err != nil || !ok || enc.KeyID != active {
				t.Fatalf("the record after convergence is %+v (ok=%v err=%v), want it under the active key %s", enc, ok, err, active)
			}
			if enc.StoredHash != hexSum(obj) || enc.StoredSize != int64(len(obj)) || enc.StoredHash == w.hash {
				t.Fatalf("recorded digest/size %s/%d do not describe the object at the key (%s/%d)", enc.StoredHash, enc.StoredSize, hexSum(obj), len(obj))
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

// The anti-vacuity control for the encrypted-source kind: with the kill
// suppressed, the run reaches the opt-in kill point, finishes, and leaves on
// the second medium ciphertext under key 2 that key 1 cannot open, and
// nothing on the first. A cell that passes only because the harness never ran
// a re-encryption (it re-homed, or never started) is red here.
func TestTheReencryptCrashCellsReallyReencryptSomething_1317(t *testing.T) {
	w, oldKey, oldBytes := reencWorld(t)

	res := w.crashTo(crashSecondMedium, "-plan", "-encrypt-existing", "-reencrypt", "-kill-after-consent", "-suppress-kill")
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
	if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) || bytes.Equal(obj, oldBytes) {
		t.Fatalf("an uninterrupted re-encrypt run left something other than new age ciphertext on the second medium (%d bytes, same as the old: %v)", len(obj), bytes.Equal(obj, oldBytes))
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
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Errorf("an uninterrupted run left %v in the staging area", left)
	}
}
