package placement_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// These are #1317's engine tests (EPIC T, T2.2): re-encrypting an encrypted
// retained copy that is under a key (or suite) other than the active one,
// through #1291's own entry point with Plan.Reencrypt. The old ciphertext is
// streamed through its own suite and key straight into the active suite and
// key, the new copy goes to another medium, and the old one is retired only
// after the new one has been decrypt-verified.
//
// The world is built with the product's own moves: the artifact is moved for
// real from local disk onto medium A under key 1 (so the copy on A is a real
// age/v1 copy with a real record), then custody rotates, so key 1 is
// superseded and key 2 is active. Re-encryption across two suites needs the
// second suite (T1.3); the same path is exercised here across two keys, which
// is the half the copy record and the engine treat identically.
//
// They were written by reading the engine and the fixtures and have not been
// run. Each one names the defect that turns it red in its own comment.

type reencWorld struct {
	*encTwo
	oldKey, newKey keycustody.KeyRecord
	srcBytes       []byte
}

func newReencWorld(t *testing.T) *reencWorld {
	t.Helper()
	e := newEncryptedTwoMediumFixture(t, fixtureOpts{content: multiChunk()})
	src, ok := e.recorded(testMedium)
	if !ok || src.KeyID != e.ck.ID {
		t.Fatalf("control: the copy on %s is not recorded under key 1 (%+v, ok=%v)", testMedium, src, ok)
	}
	srcBytes := append([]byte(nil), e.medium.bytesAt(e.key)...)
	if !bytes.HasPrefix(srcBytes, []byte("age-encryption.org/v1\n")) {
		t.Fatal("control: the copy on the first medium is not age ciphertext")
	}
	newKey, err := e.keys.Rotate(e.ctx, testNow2)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	keys, err := e.keys.Keys(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.ID == e.ck.ID && k.Status != keycustody.StatusSuperseded {
			t.Fatalf("control: key 1 is %s after the rotation, want superseded", k.Status)
		}
	}
	return &reencWorld{encTwo: e, oldKey: e.ck, newKey: newKey, srcBytes: srcBytes}
}

func (w *reencWorld) plan(destination string) placement.Plan {
	return placement.Plan{Artifact: w.artifact, DestinationMedium: destination, Reencrypt: true}
}

func (w *reencWorld) reencrypt() placement.CycleReport {
	w.t.Helper()
	report, err := w.engine.EncryptExisting(w.ctx, []placement.Plan{w.plan(mediumB)})
	if err != nil {
		w.t.Fatalf("EncryptExisting: %v", err)
	}
	return report
}

func (w *reencWorld) lastMove() state.Move {
	w.t.Helper()
	moves := w.moves()
	return moves[len(moves)-1]
}

// sourceIntact is the standing claim: until the new copy is verified the old
// one is the only good one, so its object and its ACTIVE placement and its
// record are exactly as they were.
func (w *reencWorld) sourceIntact(when string) {
	w.t.Helper()
	if got := w.medium.bytesAt(w.key); !bytes.Equal(got, w.srcBytes) {
		w.t.Fatalf("%s: the old ciphertext on %s is gone or changed (%d bytes, want %d)", when, testMedium, len(got), len(w.srcBytes))
	}
	p, ok := w.placement(testMedium)
	if !ok || p.Status != state.PlacementActive {
		w.t.Fatalf("%s: the old copy's placement is %+v (ok=%v), want ACTIVE", when, p, ok)
	}
	if rec, ok := w.recorded(testMedium); !ok || rec.KeyID != w.oldKey.ID {
		w.t.Fatalf("%s: the old copy's record is %+v (ok=%v), want it untouched under key 1", when, rec, ok)
	}
}

// plaintextOnDisk walks the backup set's local_path (the staging directory is
// inside it) and returns every file that holds a run of the artifact's
// plaintext. The local copy is gone in this world, so any hit is plaintext
// the re-encryption wrote.
func (w *reencWorld) plaintextOnDisk() []string {
	w.t.Helper()
	needles := [][]byte{w.content[:64], w.content[64*1024 : 64*1024+4096], w.content[len(w.content)-512:]}
	var hits []string
	_ = filepath.WalkDir(w.localDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, n := range needles {
			if bytes.Contains(b, n) {
				hits = append(hits, path)
				break
			}
		}
		return nil
	})
	return hits
}

// watchedRead is the filesystem watch: every read of the old ciphertext, which
// is exactly when the encrypting pass is running, walks the local tree first.
type watchedRead struct {
	io.ReadCloser
	look func()
}

func (r *watchedRead) Read(p []byte) (int, error) {
	r.look()
	return r.ReadCloser.Read(p)
}

type watchStore struct {
	placement.MediumStore
	key  string
	look func()
}

func (s *watchStore) OpenObject(ctx context.Context, medium transport.Medium, key string) (io.ReadCloser, error) {
	rc, err := s.MediumStore.OpenObject(ctx, medium, key)
	if err != nil || key != s.key {
		return rc, err
	}
	return &watchedRead{ReadCloser: rc, look: s.look}, nil
}

// --- the happy path ---------------------------------------------------------

// Red until an encrypted source can be re-encrypted. Also red if the new copy
// is not age ciphertext under key 2 (it opens under key 1, or not at all), if
// its record names key 1 or carries the plaintext digest, if any plaintext is
// on local disk at any read of the old ciphertext or after the run, or if the
// old copy and the staging area are not cleaned up at the end.
func TestReencryptStreamsOldCiphertextIntoNewCiphertextWithoutLocalPlaintext_1317(t *testing.T) {
	w := newReencWorld(t)
	var reads int
	var seen []string
	w.engine.Store = &watchStore{MediumStore: w.engine.Store, key: w.key, look: func() {
		reads++
		seen = append(seen, w.plaintextOnDisk()...)
	}}

	report := w.reencrypt()
	w.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("the run did not complete: %+v (move error %q)", report, w.lastMove().Error)
	}
	if reads == 0 {
		t.Fatal("control: the watch never saw a read of the old ciphertext, so it proves nothing")
	}
	if len(seen) != 0 {
		t.Fatalf("plaintext was on local disk while the old copy was being read: %v", seen)
	}
	if left := w.plaintextOnDisk(); len(left) != 0 {
		t.Fatalf("plaintext is on local disk after the run: %v", left)
	}

	obj := w.medium.bytesAt(w.keyB)
	if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) || bytes.Contains(obj, w.content[:64]) {
		t.Fatalf("the object on %s is not age ciphertext of the artifact (%d bytes)", mediumB, len(obj))
	}
	if bytes.Equal(obj, w.srcBytes) {
		t.Fatal("the new object is the old ciphertext copied as it is; that is a re-home, not a re-encryption")
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
	if !ok || rec.KeyID != w.newKey.ID || rec.FormatID != "age/v1" || rec.StoredHash != sum(obj) || rec.StoredSize != int64(len(obj)) || rec.StoredHash == w.hash {
		t.Fatalf("record %+v (ok=%v) does not describe the object at the key (%s/%d) under key %s", rec, ok, sum(obj), len(obj), w.newKey.ID)
	}
	row, ok := w.placement(mediumB)
	if !ok || row.Status != state.PlacementActive || row.Hash != w.hash || row.VerificationClass != state.VerificationContent {
		t.Fatalf("destination placement %+v (ok=%v), want ACTIVE, content class, plaintext digest %s", row, ok, w.hash)
	}
	if w.medium.has(w.key) {
		t.Errorf("the old ciphertext is still on %s after a completed run", testMedium)
	}
	if p, ok := w.placement(testMedium); !ok || p.Status != state.PlacementGone {
		t.Errorf("the old copy's placement is %+v (ok=%v), want GONE", p, ok)
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Errorf("the staging area holds %v after the run", left)
	}
	if consented, err := w.journal.EncryptExistingConsented(w.ctx, w.lastMove().ID); err != nil || !consented {
		t.Fatalf("the move that did this has no recorded opt-in (consented=%v err=%v)", consented, err)
	}
}

// --- refusals ---------------------------------------------------------------

// The same medium is refused before any move row exists, and the refusal says
// why and what to do instead. Red if it is the generic not-eligible refusal,
// or if it plans anything.
func TestReencryptRefusesTheSameMediumAndSaysWhy_1317(t *testing.T) {
	w := newReencWorld(t)
	err := w.engine.CheckEncryptExisting(w.ctx, w.plan(testMedium))
	if !errors.Is(err, placement.ErrEncryptExistingSameMedium) {
		t.Fatalf("CheckEncryptExisting onto the copy's own medium = %v, want ErrEncryptExistingSameMedium", err)
	}
	for _, want := range []string{"one copy per medium", "second medium", "local copy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	report, runErr := w.engine.EncryptExisting(w.ctx, []placement.Plan{w.plan(testMedium)})
	if runErr != nil {
		t.Fatalf("EncryptExisting: %v", runErr)
	}
	if report.Refused != 1 || report.Planned != 0 {
		t.Fatalf("report %+v, want one refusal and nothing planned", report)
	}
	if n := len(w.moves()); n != 1 {
		t.Fatalf("%d moves, want only the first hop's", n)
	}
	w.sourceIntact("after a same-medium refusal")
}

// A copy whose old key cannot be resolved is left exactly as it is and named.
// Red if the refusal is not its own error (or not also ErrClassUnavailable),
// does not name the key, or if anything is planned, read or uploaded.
func TestReencryptLeavesACopyWhoseKeyIsUnavailable_1317(t *testing.T) {
	w := newReencWorld(t)
	w.engine.Keys = keysWithout{Keys: w.keys, gone: w.oldKey.ID}
	uploadsBefore := w.medium.uploadCount()

	err := w.engine.CheckEncryptExisting(w.ctx, w.plan(mediumB))
	if !errors.Is(err, placement.ErrReencryptSourceKeyUnavailable) || !errors.Is(err, placement.ErrClassUnavailable) {
		t.Fatalf("CheckEncryptExisting = %v, want ErrReencryptSourceKeyUnavailable and ErrClassUnavailable", err)
	}
	if !strings.Contains(err.Error(), w.oldKey.ID) || !strings.Contains(err.Error(), w.artifact.String()) {
		t.Errorf("the refusal does not name the copy and its key: %v", err)
	}

	report := w.reencrypt()
	w.guard.fail()
	if report.Refused != 1 || report.Planned != 0 || !errors.Is(report.Outcomes[0].Err, placement.ErrReencryptSourceKeyUnavailable) {
		t.Fatalf("report %+v, want one refusal for the unavailable key and nothing planned", report)
	}
	if n := len(w.moves()); n != 1 {
		t.Fatalf("%d moves, want only the first hop's", n)
	}
	if got := w.medium.uploadCount() - uploadsBefore; got != 0 {
		t.Errorf("%d upload(s) for a copy whose key is gone", got)
	}
	w.sourceIntact("after an unavailable-key refusal")
}

// A copy already under the active suite and key is refused as current. Red if
// it is rewritten for nothing, or reported as plain "already encrypted", which
// is #1291's word for a plaintext verb.
func TestReencryptRefusesACopyAlreadyUnderTheActiveSuiteAndKey_1317(t *testing.T) {
	e := newEncryptedTwoMediumFixture(t, fixtureOpts{content: multiChunk()})
	err := e.engine.CheckEncryptExisting(e.ctx, placement.Plan{Artifact: e.artifact, DestinationMedium: mediumB, Reencrypt: true})
	if !errors.Is(err, placement.ErrReencryptAlreadyCurrent) {
		t.Fatalf("CheckEncryptExisting = %v, want ErrReencryptAlreadyCurrent", err)
	}
	// Without Reencrypt the encrypted copy keeps #1291's refusal.
	err = e.engine.CheckEncryptExisting(e.ctx, placement.Plan{Artifact: e.artifact, DestinationMedium: mediumB})
	if !errors.Is(err, placement.ErrEncryptExistingAlreadyEncrypted) || errors.Is(err, placement.ErrReencryptAlreadyCurrent) {
		t.Fatalf("CheckEncryptExisting without Reencrypt = %v, want ErrEncryptExistingAlreadyEncrypted only", err)
	}
}

// --- the source survives until verify passes --------------------------------

// Red if the old object or its ACTIVE placement is touched before the
// decrypt-verify of the new copy has passed, or if a failed verify (a
// destination that keeps a truncated object) retires it or catalogues the
// bad object.
func TestReencryptRetiresTheSourceOnlyAfterDecryptVerify_1317(t *testing.T) {
	t.Run("every read of the new copy finds the old one still there", func(t *testing.T) {
		w := newReencWorld(t)
		type seen struct {
			objectThere bool
			status      string
		}
		var reads []seen
		w.engine.Store = &openProbe{MediumStore: w.engine.Store, onOpen: func(key string) {
			if key != w.keyB {
				return
			}
			p, _ := w.placement(testMedium)
			reads = append(reads, seen{objectThere: w.medium.has(w.key), status: p.Status})
		}}
		report := w.reencrypt()
		w.guard.fail()
		if report.Completed != 1 {
			t.Fatalf("control: the run did not complete: %+v", report)
		}
		// On the nominal path the verify is the only read: the pre-delete proof
		// (#439, predelete.go) replaces the second full read with a metadata check
		// of the same key, and a resumed move reads again. Either way every read
		// there is has to find the source still in place.
		if len(reads) < 1 {
			t.Fatal("the new object was never read, so it was never decrypt-verified")
		}
		for i, r := range reads {
			if !r.objectThere {
				t.Errorf("read #%d of the new object found the old ciphertext already gone", i+1)
			}
		}
		if reads[0].status != state.PlacementActive {
			t.Errorf("the old placement was %s when the new object was first verified, want ACTIVE", reads[0].status)
		}
	})
	t.Run("a failed verify leaves the old copy as the only good one", func(t *testing.T) {
		w := newReencWorld(t)
		w.medium.truncate = 200 // header plus a little: no final chunk
		report := w.reencrypt()
		w.guard.fail()
		if report.Completed != 0 || placement.Phase(w.lastMove().Phase) == placement.Done {
			t.Fatalf("a run over a truncated ciphertext completed: %+v", report)
		}
		w.sourceIntact("after a failed verify")
		if p, ok := w.placement(mediumB); ok && p.Status == state.PlacementActive {
			t.Fatalf("an ACTIVE placement exists over a truncated ciphertext: %+v", p)
		}
		if _, ok := w.recorded(mediumB); ok {
			t.Fatal("an encryption record exists over a truncated ciphertext")
		}
	})
}

// An old ciphertext that does not open under its own key is never turned into
// a catalogued copy: nothing is uploaded, the move is abandoned (no retry can
// change it), and the old copy is left as it is. Red if anything reaches the
// destination or the old copy is touched.
func TestAnOldCiphertextThatDoesNotOpenIsLeftAsItIs_1317(t *testing.T) {
	w := newReencWorld(t)
	damaged := append([]byte(nil), w.srcBytes...)
	damaged[len(damaged)/2] ^= 0xff
	w.medium.mu.Lock()
	w.medium.putLocked(w.key, damaged)
	w.medium.mu.Unlock()
	w.srcBytes = damaged
	uploadsBefore := w.medium.uploadCount()

	report := w.reencrypt()
	w.guard.fail()
	if report.Completed != 0 {
		t.Fatalf("a damaged old copy was re-encrypted into a completed move: %+v", report)
	}
	if got := placement.Phase(w.lastMove().Phase); got != placement.Abandoned {
		t.Fatalf("the move is at %s, want ABANDONED", got)
	}
	if got := w.medium.uploadCount() - uploadsBefore; got != 0 {
		t.Errorf("%d upload(s) made from an old copy that does not authenticate", got)
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Errorf("the staging area holds %v", left)
	}
	w.sourceIntact("after a damaged old copy")
}

// --- explicit opt-in --------------------------------------------------------

// A key rotation must not rewrite anything on its own. The ordinary cycle,
// planned against the same artifact and destination, re-homes the old
// ciphertext as it is, under the old key, and writes no opt-in. Red if the
// cycle re-encrypts (a new key id on the destination, or new bytes), or if
// any opt-in row exists for its move.
func TestTheRetentionCycleNeverReencryptsOnItsOwn_1317(t *testing.T) {
	w := newReencWorld(t)
	report, err := w.engine.RunCycle(w.ctx, []placement.Plan{w.plan(mediumB)})
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	w.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("control: the re-home did not complete: %+v", report)
	}
	if consented, err := w.journal.EncryptExistingConsented(w.ctx, w.lastMove().ID); err != nil || consented {
		t.Fatalf("the cycle's move reads as opted in (consented=%v err=%v)", consented, err)
	}
	if rec, ok := w.recorded(mediumB); !ok || rec.KeyID != w.oldKey.ID {
		t.Fatalf("the destination's record is %+v (ok=%v), want the old key: a cycle re-homes, it never re-encrypts", rec, ok)
	}
	if got := w.medium.bytesAt(w.keyB); !bytes.Equal(got, w.srcBytes) {
		t.Fatal("the cycle wrote new ciphertext; a re-home copies the old bytes as they are")
	}
}

// A run that stopped after the plan (no opt-in written) is adopted by the next
// re-encrypt run for the same artifact and destination. Red if the second run
// plans a second move or abandons the first.
func TestAReencryptRunAdoptsAMovePlannedButNeverOptedIn_1317(t *testing.T) {
	w := newReencWorld(t)
	mv, err := w.journal.PlanMove(w.ctx, state.MovePlan{
		Artifact: w.artifact, SourceMedium: testMedium,
		DestinationMedium: mediumB, DestinationKey: w.keyB, OccurredAt: w.clock,
	})
	if err != nil {
		t.Fatalf("planning the half-started move: %v", err)
	}
	report := w.reencrypt()
	w.guard.fail()
	if report.Completed != 1 || report.Resumed != 1 {
		t.Fatalf("the run did not adopt and finish the planned move: %+v", report)
	}
	if w.lastMove().ID != mv.ID {
		t.Fatal("the run planned a new move instead of adopting the one that was waiting")
	}
	if rec, ok := w.recorded(mediumB); !ok || rec.KeyID != w.newKey.ID {
		t.Fatalf("the adopted move's record is %+v (ok=%v), want the active key", rec, ok)
	}
}
