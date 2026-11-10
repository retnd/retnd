package movecrash_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
)

// The crash cells for EPIC N's N1.3 (#991): a real process is killed at each
// new boundary of the encrypting copy, and the real engine restarts over
// what it left. The hop under test is local to medium, which is the one
// that encrypts: the artifact is read, written through the staging area as
// ciphertext, uploaded, read back, decrypt-verified and journaled.
//
// What every cell asserts after the restart, beyond what the plaintext
// matrix asserts:
//
//   - the object at the key is age ciphertext that decrypts, under the key
//     custody holds, to the artifact the journal recorded;
//   - the encrypted copy's record (stored digest) is the digest of THAT
//     object, not of any earlier attempt's;
//   - the staging area is empty, temporary files included;
//   - at no point, including the instant of the crash, is there a record
//     without its placement or a placement recorded as encrypted without a
//     decrypt-verify behind it.
//
// The control at the bottom proves the kills are landing in an encrypting
// pass at all, because "the staging area ends empty" is trivially true of
// an engine that never encrypted anything.

// enableEncryption mints the managed key before any harness runs. The
// registry is keycustody.JournalRegistry over the cell's own journal file, so
// the harness process and the restarting engine see the same encryption_keys
// row, and the test closes its handle before a harness opens the file.
func (w *world) enableEncryption() {
	w.t.Helper()
	w.keyDir = filepath.Join(w.dir, "keys")
	if err := os.MkdirAll(w.keyDir, 0o700); err != nil {
		w.t.Fatal(err)
	}
	j, err := state.Open(w.ctx, w.journal)
	if err != nil {
		w.t.Fatalf("opening the journal: %v", err)
	}
	defer func() { _ = j.Close() }()
	if _, err := w.keysOver(j).Create(w.ctx, crashNow); err != nil {
		w.t.Fatalf("Create: %v", err)
	}
}

func (w *world) keysOver(j *state.Journal) *keycustody.Store {
	w.t.Helper()
	keys, err := keycustody.Open(w.keyDir, keycustody.NewJournalRegistry(j), keycustody.AgeFormat{})
	if err != nil {
		w.t.Fatalf("keycustody.Open: %v", err)
	}
	return keys
}

func (w *world) encryptedRestart(j *state.Journal, keys *keycustody.Store) (*placement.Engine, *restartGuard) {
	w.t.Helper()
	engine, guard := w.restartEngine(j, false)
	engine.Sets = crashSets{set: config.BackupSet{
		Name: crashSet, ID: w.artifact.Set, LocalPath: w.root,
		RetainedCopyEncryption: config.RetainedCopyEncryption{
			Mode:        config.RetainedCopyEncryptionRequired,
			KeyProvider: config.RetainedCopyKeyProvider{Managed: true},
		},
	}}
	engine.Keys = keys
	return engine, guard
}

func (w *world) objectAtKey() []byte {
	b, err := os.ReadFile(filepath.Join(w.bucket, filepath.FromSlash(w.key)))
	if err != nil {
		return nil
	}
	return b
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// recordAndPlacementAgree is the atomicity check: the encrypted record and
// the ACTIVE destination placement it describes are written by one advance,
// so a journal read at any instant has both or neither.
func (w *world) recordAndPlacementAgree(j *state.Journal, when string) {
	w.t.Helper()
	rec, err := j.Get(w.ctx, w.artifact)
	if err != nil {
		w.t.Fatalf("reading the journal %s: %v", when, err)
	}
	var active bool
	for _, p := range rec.Placements {
		if p.Medium == crashMedium && p.Status == state.PlacementActive {
			active = true
		}
	}
	_, hasRecord, err := j.PlacementEncryption(w.ctx, w.artifact, crashMedium)
	if err != nil {
		w.t.Fatalf("reading the encryption record %s: %v", when, err)
	}
	if active != hasRecord {
		w.t.Fatalf("%s: ACTIVE destination placement = %t but placement_encryption record = %t; the two are one write", when, active, hasRecord)
	}
}

func TestCrashMatrixOverAnEncryptedCopy(t *testing.T) {
	for _, cell := range []struct {
		name   string
		kill   []string
		proves string
		// afterCrash inspects what the dead process left, before the
		// restart tidies it away.
		afterCrash func(t *testing.T, w *world)
		// freshObject says the object at the key after the restart must
		// differ from the one the dead process left (ciphertext is not
		// deterministic, so a re-copy writes different bytes at one key).
		freshObject bool
	}{
		{
			name:   "E1 part-way through reading the artifact into the encrypting pass",
			kill:   []string{"-plan", "-kill-during-encrypt"},
			proves: "an interrupted encrypt leaves only a temporary file in the staging area, uploads nothing, and the resumed copy re-encrypts from scratch",
			afterCrash: func(t *testing.T, w *world) {
				left := w.stagingLeftovers()
				if len(left) != 1 || !strings.HasPrefix(left[0], copycrypt.TempPrefix) {
					t.Fatalf("the staging area holds %v; an interrupted encrypt leaves exactly one temporary file", left)
				}
				if n := w.objectCountOn(t, w.bucket); n != 0 {
					t.Fatalf("%d object(s) on the medium; the kill landed after the upload", n)
				}
				if !w.localExists() {
					t.Fatal("the local source is gone")
				}
			},
		},
		{
			name:   "E2 the instant the staged ciphertext is complete, before the upload",
			kill:   []string{"-plan", "-kill-before-upload"},
			proves: "a finished staging object nobody uploaded is replaced by a fresh encryption, never resumed or trusted, and the staging area ends empty",
			afterCrash: func(t *testing.T, w *world) {
				left := w.stagingLeftovers()
				if len(left) != 1 || strings.HasPrefix(left[0], copycrypt.TempPrefix) {
					t.Fatalf("the staging area holds %v; a finished encrypt leaves exactly one object under its own name", left)
				}
				if n := w.objectCountOn(t, w.bucket); n != 0 {
					t.Fatalf("%d object(s) on the medium; the kill landed after the upload", n)
				}
			},
		},
		{
			name:        "E3 the instant the upload returns, before anything is journaled",
			kill:        []string{"-plan", "-kill-after-copy"},
			proves:      "ciphertext at the key with no record naming it is replaced by a fresh encryption, and the recorded stored digest is the second object's, not the first's",
			freshObject: true,
			afterCrash: func(t *testing.T, w *world) {
				if len(w.objectAtKey()) == 0 {
					t.Fatal("no object at the key; the kill landed before the upload returned")
				}
			},
		},
		{
			name:        "E4 after COPIED is durably recorded, before verification",
			kill:        []string{"-plan", "-kill-after-phase=COPIED"},
			proves:      "an unverified ciphertext has no placement and no record, so nothing can rely on it, and the restart decrypt-verifies before anything is recorded",
			freshObject: false,
		},
		{
			name:   "E5 after VERIFIED is durably recorded",
			kill:   []string{"-plan", "-kill-after-phase=VERIFIED"},
			proves: "the placement and the encrypted record were written by one advance, so a journal read at the crash has both",
			afterCrash: func(t *testing.T, w *world) {
				j := w.journalNow()
				if _, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashMedium); err != nil || !ok {
					t.Fatalf("no placement_encryption record at VERIFIED (ok=%v err=%v)", ok, err)
				}
			},
		},
	} {
		t.Run(cell.name, func(t *testing.T) {
			w := newWorld(t)
			w.enableEncryption()

			res := w.crashTo(crashMedium, cell.kill...)
			if !res.killed() {
				t.Fatalf("the harness was not killed by SIGKILL (err=%v)\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
			}
			if v := res.violations(); len(v) > 0 {
				t.Fatalf("the crashed process reported invariant violations: %v", v)
			}
			first := w.objectAtKey()
			if cell.afterCrash != nil {
				cell.afterCrash(t, w)
			}

			j := w.journalNow()
			w.checkInvariantNow(j, "at the instant of the crash ("+cell.name+")")
			w.recordAndPlacementAgree(j, "at the instant of the crash ("+cell.name+")")

			keys := w.keysOver(j)
			engine, guard := w.encryptedRestart(j, keys)
			report, err := engine.RunCycle(w.ctx, nil)
			if err != nil {
				t.Fatalf("the restart failed: %v", err)
			}
			guard.fail()
			if report.Resumed != 1 {
				t.Fatalf("the restart did not pick the move up: %+v\nthis cell proves: %s", report, cell.proves)
			}

			moves, err := j.MovesForArtifact(w.ctx, w.artifact)
			if err != nil || len(moves) != 1 || placement.Phase(moves[0].Phase) != placement.Done {
				t.Fatalf("the move did not converge to DONE: %+v (err=%v)\nthis cell proves: %s", moves, err, cell.proves)
			}
			w.recordAndPlacementAgree(j, "after the restart")

			obj := w.objectAtKey()
			if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) {
				t.Fatalf("the object at the key is not age ciphertext\nthis cell proves: %s", cell.proves)
			}
			if err := keys.WithKey(w.ctx, mustActive(t, keys, w.ctx), func(k []byte) error {
				_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), k, w.hash)
				return err
			}); err != nil {
				t.Fatalf("the object at the key does not decrypt to the artifact: %v", err)
			}
			if cell.freshObject && bytes.Equal(obj, first) {
				t.Fatal("the object at the key is byte-identical to the dead process's; a re-copy must be a fresh encryption")
			}

			enc, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashMedium)
			if err != nil || !ok {
				t.Fatalf("no placement_encryption record after convergence (ok=%v err=%v)", ok, err)
			}
			if enc.StoredHash != hexSum(obj) || enc.StoredSize != int64(len(obj)) {
				t.Fatalf("recorded stored digest/size %s/%d do not describe the object at the key (%s/%d)", enc.StoredHash, enc.StoredSize, hexSum(obj), len(obj))
			}
			if len(first) > 0 && cell.freshObject && enc.StoredHash == hexSum(first) {
				t.Fatal("the recorded stored digest is the earlier attempt's, which describes an object that is gone")
			}
			if enc.StoredHash == w.hash {
				t.Fatal("the stored-bytes digest equals the plaintext digest")
			}
			if left := w.stagingLeftovers(); len(left) != 0 {
				t.Errorf("the staging area holds %v after the restart\nthis cell proves: %s", left, cell.proves)
			}
			if n := w.objectCountOn(t, w.bucket); n != 1 {
				t.Errorf("%d object(s) on the medium, want one at the single key", n)
			}
			if w.localExists() {
				t.Error("the plaintext local copy survived a completed move")
			}
		})
	}
}

func mustActive(t *testing.T, keys *keycustody.Store, ctx context.Context) string {
	t.Helper()
	rec, ok, err := keys.Active(ctx)
	if err != nil || !ok {
		t.Fatalf("no active key (ok=%v err=%v)", ok, err)
	}
	return rec.ID
}

// TestTheEncryptedCrashCellsReallyEncryptSomething is the control the table
// above cannot carry itself: a suppressed-kill run reaches the kill point,
// names the staged ciphertext, finishes, and leaves ciphertext on the
// medium and nothing in the staging area.
func TestTheEncryptedCrashCellsReallyEncryptSomething(t *testing.T) {
	w := newWorld(t)
	w.enableEncryption()

	res := w.crashTo(crashMedium, "-plan", "-kill-before-upload", "-suppress-kill")
	if res.killed() {
		t.Fatal("the harness died even with -suppress-kill set, so the encrypted cells' kill flag does not do what they rely on")
	}
	if !strings.Contains(res.stderr, "MOVECRASH_SELF_KILL_SUPPRESSED") {
		t.Fatalf("the harness never reached the pre-upload kill point, so the encrypted cells are crashing somewhere else:\n%s", res.stderr)
	}
	if !strings.Contains(res.stderr, filepath.Join(w.root, placement.StagingDirName)) {
		t.Errorf("the kill point named %q rather than a file in the staging area", res.stderr)
	}
	if !strings.Contains(res.stdout, "FINISHED") {
		t.Fatalf("the suppressed run did not finish:\n%s\n%s", res.stdout, res.stderr)
	}
	obj := w.objectAtKey()
	if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) {
		t.Fatalf("an uninterrupted encrypted move left something other than age ciphertext at the key (%d bytes)", len(obj))
	}
	if bytes.Contains(obj, w.content[:32]) {
		t.Fatal("the plaintext appears in the stored object")
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Errorf("an uninterrupted encrypted move left %v in the staging area", left)
	}
}
