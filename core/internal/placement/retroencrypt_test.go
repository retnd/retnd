package placement_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// These are #1291's engine tests: encrypting a retained copy that already
// sits on a medium in the clear, under a backup set whose retained-copy
// encryption became "required" after the copy was made. EPIC N left that case
// refused (a plaintext copy on a medium may not hop medium to medium under a
// required set, because the hop would stage and upload it in the clear), and
// this change adds the one explicit way out: Engine.EncryptExisting, started
// by an operator, never by a policy change.
//
// The world is built the way the lifecycle tests build theirs: the artifact
// is moved for real from local disk onto medium A under a set that does NOT
// require encryption (so the copy on A is genuinely plaintext), and only then
// are keys provisioned and the set flipped to "required". The encrypted copy
// goes to medium B, because the journal holds one placement per artifact per
// medium, so an encrypted sibling cannot live beside its plaintext source on
// the same medium.
//
// They were written by reading the engine and the fixtures and have not been
// run. Each one names the defect that turns it red in its own comment.

type retroWorld struct {
	*twoMediumFixture
	keys     *keycustody.Store
	ck       keycustody.KeyRecord
	srcKey   string
	srcBytes []byte
}

func newRetroWorld(t *testing.T) *retroWorld {
	t.Helper()
	f := newTwoMediumFixture(t, fixtureOpts{content: multiChunk()})
	src, ok := f.placement(testMedium)
	if !ok || src.Status != state.PlacementActive {
		t.Fatalf("control: the first hop left no ACTIVE copy on %s (%+v)", testMedium, src)
	}
	srcBytes := append([]byte(nil), f.medium.bytesAt(src.Location)...)
	if !bytes.Equal(srcBytes, f.content) {
		t.Fatal("control: the copy on the first medium is not the plaintext artifact, so this world does not hold a plaintext retained copy")
	}
	if _, enc, err := f.journal.PlacementEncryption(f.ctx, f.artifact, testMedium); err != nil || enc {
		t.Fatalf("control: the plaintext copy has an encryption record (enc=%v err=%v)", enc, err)
	}
	keys, ck := provisionKeys(t, f.fixture)
	return &retroWorld{twoMediumFixture: f, keys: keys, ck: ck, srcKey: src.Location, srcBytes: srcBytes}
}

// plainSet is the same backup set with encryption off, which is how a set
// looks before its operator turns "required" on.
func plainSet(w *retroWorld) config.BackupSet {
	return config.BackupSet{Name: testSet, ID: w.artifact.Set, LocalPath: w.localDir}
}

func (w *retroWorld) plan() []placement.Plan {
	return []placement.Plan{{Artifact: w.artifact, DestinationMedium: mediumB}}
}

func (w *retroWorld) encryptExisting() placement.CycleReport {
	w.t.Helper()
	report, err := w.engine.EncryptExisting(w.ctx, w.plan())
	if err != nil {
		w.t.Fatalf("EncryptExisting: %v", err)
	}
	return report
}

func (w *retroWorld) lastMove() state.Move {
	w.t.Helper()
	moves := w.moves()
	return moves[len(moves)-1]
}

// plaintextIntact is the standing claim of this whole feature: the only good
// copy is the plaintext one until the encrypted one has been verified.
func (w *retroWorld) plaintextIntact(when string) {
	w.t.Helper()
	if got := w.medium.bytesAt(w.srcKey); !bytes.Equal(got, w.srcBytes) {
		w.t.Fatalf("%s: the plaintext object on %s is gone or changed (%d bytes, want %d)", when, testMedium, len(got), len(w.srcBytes))
	}
	p, ok := w.placement(testMedium)
	if !ok || p.Status != state.PlacementActive {
		w.t.Fatalf("%s: the plaintext copy's placement is %+v (ok=%v), want ACTIVE", when, p, ok)
	}
}

// --- the happy path ---------------------------------------------------------

// Red until Engine.EncryptExisting exists. Also red if the copy on B is not
// ciphertext, does not decrypt to the artifact the journal recorded, if its
// record carries the plaintext digest where the stored digest belongs, or if
// the plaintext copy and the staging area are not both cleaned up at the end.
func TestEncryptExistingTurnsAPlaintextMediumCopyIntoAnEncryptedOneOnTheDestination_1291(t *testing.T) {
	w := newRetroWorld(t)

	report := w.encryptExisting()
	w.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("the run did not complete: %+v (move error %q)", report, w.lastMove().Error)
	}

	obj := w.medium.bytesAt(w.keyB)
	if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) {
		t.Fatalf("the object on %s is not age ciphertext (%d bytes)", mediumB, len(obj))
	}
	if bytes.Contains(obj, w.content[:64]) {
		t.Fatal("plaintext appears in the object on the destination")
	}
	if err := w.keys.WithKey(w.ctx, w.ck.ID, func(k []byte) error {
		_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), k, w.hash)
		return err
	}); err != nil {
		t.Fatalf("the encrypted copy does not decrypt to the artifact: %v", err)
	}

	enc, ok, err := w.journal.PlacementEncryption(w.ctx, w.artifact, mediumB)
	if err != nil || !ok {
		t.Fatalf("no encryption record on %s (ok=%v err=%v)", mediumB, ok, err)
	}
	if enc.KeyID != w.ck.ID || enc.StoredHash != sum(obj) || enc.StoredSize != int64(len(obj)) {
		t.Fatalf("record %+v does not describe the object at the key (%s/%d) under key %s", enc, sum(obj), len(obj), w.ck.ID)
	}
	if enc.StoredHash == w.hash {
		t.Fatal("the stored-bytes digest equals the plaintext digest")
	}
	row, ok := w.placement(mediumB)
	if !ok || row.Status != state.PlacementActive || row.Hash != w.hash || row.VerificationClass != state.VerificationContent {
		t.Fatalf("destination placement %+v (ok=%v), want ACTIVE, content class, plaintext digest %s", row, ok, w.hash)
	}

	if w.medium.has(w.srcKey) {
		t.Errorf("the plaintext object is still on %s after a completed run", testMedium)
	}
	if p, ok := w.placement(testMedium); !ok || p.Status != state.PlacementGone {
		t.Errorf("the plaintext copy's placement is %+v (ok=%v), want GONE", p, ok)
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Errorf("the staging area holds %v after the run", left)
	}
	consented, err := w.journal.EncryptExistingConsented(w.ctx, w.lastMove().ID)
	if err != nil || !consented {
		t.Fatalf("the move that did this has no recorded opt-in (consented=%v err=%v)", consented, err)
	}
}

// --- explicit opt-in --------------------------------------------------------

// A policy change must not rewrite anything. The ordinary cycle, planned
// against the same artifact and destination, still refuses the plaintext hop,
// leaves the plaintext copy alone and now names the verb that is the way out.
// Red if the cycle starts encrypting on its own (an object on B, or a record),
// or refuses without saying how to proceed.
func TestAPlaintextHopUnderARequiredSetStillNeedsTheExplicitVerb_1291(t *testing.T) {
	w := newRetroWorld(t)

	report := w.moveToB()
	w.guard.fail()

	if report.Completed != 0 {
		t.Fatalf("an ordinary cycle completed a plaintext hop under a required set: %+v", report)
	}
	mv := w.lastMove()
	if placement.Phase(mv.Phase) != placement.Abandoned {
		t.Fatalf("the refused hop is at %s, want ABANDONED (a refusal no retry can change)", mv.Phase)
	}
	if !strings.Contains(mv.Error, "encrypt-existing") {
		t.Errorf("the refusal does not name the explicit way out: %q", mv.Error)
	}
	w.plaintextIntact("after the refused hop")
	if w.medium.has(w.keyB) {
		t.Error("bytes reached the second medium without an opt-in")
	}
	if _, ok, err := w.journal.PlacementEncryption(w.ctx, w.artifact, mediumB); err != nil || ok {
		t.Fatalf("an encryption record exists on %s without an opt-in (ok=%v err=%v)", mediumB, ok, err)
	}
	if consented, err := w.journal.EncryptExistingConsented(w.ctx, mv.ID); err != nil || consented {
		t.Fatalf("an ordinary cycle's move reads as opted in (consented=%v err=%v)", consented, err)
	}
}

// --- the plaintext source survives until verify passes ----------------------

// Red if the plaintext object or its ACTIVE placement is touched before the
// decrypt-verify of the new copy has passed. Every read of the new object
// (the verify, and the re-read before the source delete) checks the plaintext
// is still on the medium at that instant, and the first one also checks the
// plaintext placement is still ACTIVE, not yet DELETE_PENDING.
func TestThePlaintextSourceSurvivesUntilTheEncryptedCopyHasBeenVerified_1291(t *testing.T) {
	w := newRetroWorld(t)

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
		reads = append(reads, seen{objectThere: w.medium.has(w.srcKey), status: p.Status})
	}}

	report := w.encryptExisting()
	w.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("control: the run did not complete: %+v", report)
	}
	if len(reads) < 2 {
		t.Fatalf("the new object was read %d time(s); the verify and the re-read before the delete are two", len(reads))
	}
	for i, r := range reads {
		if !r.objectThere {
			t.Errorf("read #%d of the new object found the plaintext already gone", i+1)
		}
	}
	if reads[0].status != state.PlacementActive {
		t.Errorf("the plaintext placement was %s when the new object was first verified, want ACTIVE", reads[0].status)
	}
}

type openProbe struct {
	placement.MediumStore
	onOpen func(key string)
}

func (p *openProbe) OpenObject(ctx context.Context, medium transport.Medium, key string) (io.ReadCloser, error) {
	p.onOpen(key)
	return p.MediumStore.OpenObject(ctx, medium, key)
}

// A destination that stores something other than what was uploaded must never
// cost the plaintext copy. Red if a failed decrypt-verify retires the source,
// records a placement or record for the bad object, or leaves it catalogued.
func TestAFailedDecryptVerifyLeavesThePlaintextCopyAsTheOnlyGoodOne_1291(t *testing.T) {
	w := newRetroWorld(t)
	w.medium.truncate = 200 // header plus a little: no final chunk

	report := w.encryptExisting()
	w.guard.fail()

	if report.Completed != 0 {
		t.Fatalf("a run over a truncated ciphertext completed: %+v", report)
	}
	if placement.Phase(w.lastMove().Phase) == placement.Done {
		t.Fatal("the move reached DONE over a truncated ciphertext")
	}
	w.plaintextIntact("after a failed verify")
	if p, ok := w.placement(mediumB); ok && p.Status == state.PlacementActive {
		t.Fatalf("an ACTIVE placement exists over a truncated ciphertext: %+v", p)
	}
	if _, ok, err := w.journal.PlacementEncryption(w.ctx, w.artifact, mediumB); err != nil || ok {
		t.Fatalf("an encryption record exists over a truncated ciphertext (ok=%v err=%v)", ok, err)
	}
}

// A plaintext object that no longer hashes to the journal's digest must not be
// encrypted into a catalogued copy: the encrypting pass verifies its source.
// Red if ciphertext of the wrong bytes is uploaded or catalogued.
func TestAPlaintextObjectThatNoLongerMatchesTheJournalIsNeverEncrypted_1291(t *testing.T) {
	w := newRetroWorld(t)
	w.medium.mu.Lock()
	damaged := append([]byte(nil), w.srcBytes...)
	damaged[len(damaged)/2] ^= 0xff
	w.medium.putLocked(w.srcKey, damaged)
	w.medium.mu.Unlock()
	uploadsBefore := w.medium.uploadCount()

	report := w.encryptExisting()
	w.guard.fail()

	if report.Completed != 0 {
		t.Fatalf("a damaged plaintext copy was encrypted into a completed move: %+v", report)
	}
	if got := w.medium.uploadCount() - uploadsBefore; got != 0 {
		t.Errorf("%d upload(s) of ciphertext made from a source that does not match the journal", got)
	}
	if _, ok, err := w.journal.PlacementEncryption(w.ctx, w.artifact, mediumB); err != nil || ok {
		t.Fatalf("an encryption record exists for a damaged source (ok=%v err=%v)", ok, err)
	}
	if p, ok := w.placement(testMedium); !ok || p.Status != state.PlacementActive {
		t.Fatalf("the source placement is %+v (ok=%v), want it untouched and ACTIVE", p, ok)
	}
}

// --- cancel -----------------------------------------------------------------

// A cancel before the new copy is verified undoes it: the move is ABANDONED,
// the destination object and the staging area are gone, and the plaintext copy
// is exactly as it was. This is the one outcome the CLI's signal and the
// browser's cancel both reach, because both end in a cancelled context handed
// to EncryptExisting.
// Red if a cancelled run leaves a live move, a half-written object on B, a
// staging file, or touches the plaintext.
func TestACancelWhileEncryptingLeavesNothingButTheUntouchedPlaintext_1291(t *testing.T) {
	w := newRetroWorld(t)
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	w.engine.Store = &cancelOnRead{MediumStore: w.engine.Store, key: w.srcKey, cancel: cancel}

	report, err := w.engine.EncryptExisting(ctx, w.plan())
	if err != nil {
		t.Fatalf("EncryptExisting: %v", err)
	}
	w.guard.fail()

	if report.Completed != 0 {
		t.Fatalf("a cancelled run completed: %+v", report)
	}
	if got := placement.Phase(w.lastMove().Phase); got != placement.Abandoned {
		t.Fatalf("the cancelled move is at %s, want ABANDONED", got)
	}
	w.plaintextIntact("after a cancel")
	if w.medium.has(w.keyB) {
		t.Error("an object is left on the destination after a cancel")
	}
	if _, ok, err := w.journal.PlacementEncryption(w.ctx, w.artifact, mediumB); err != nil || ok {
		t.Fatalf("an encryption record exists after a cancel (ok=%v err=%v)", ok, err)
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Errorf("the staging area holds %v after a cancel", left)
	}
	live, err := w.journal.ListMoves(w.ctx, placement.NonTerminalPhaseStrings()...)
	if err != nil || len(live) != 0 {
		t.Fatalf("%d live move(s) after a cancel (err=%v)", len(live), err)
	}
}

// A cancel that lands while the new copy is being verified ends in exactly one
// of the two documented states, never a third: the move is ABANDONED with the
// plaintext untouched and nothing on B, or the verify had already passed and
// the move is at VERIFIED or later with the encrypted copy catalogued and
// recorded (the next run finishes retiring the plaintext).
// Red if a cancel here leaves a destination object with no placement, or a
// placement with no record.
func TestACancelWhileVerifyingConvergesToOneOfTwoDocumentedStates_1291(t *testing.T) {
	w := newRetroWorld(t)
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	w.engine.Store = &cancelOnRead{MediumStore: w.engine.Store, key: w.keyB, cancel: cancel}

	if _, err := w.engine.EncryptExisting(ctx, w.plan()); err != nil {
		t.Fatalf("EncryptExisting: %v", err)
	}
	w.guard.fail()

	mv := w.lastMove()
	_, hasRecord, err := w.journal.PlacementEncryption(w.ctx, w.artifact, mediumB)
	if err != nil {
		t.Fatal(err)
	}
	b, hasB := w.placement(mediumB)
	switch placement.Phase(mv.Phase) {
	case placement.Abandoned:
		w.plaintextIntact("after a cancel during verify")
		if w.medium.has(w.keyB) || hasRecord || (hasB && b.Status == state.PlacementActive) {
			t.Fatalf("ABANDONED, but the destination is not clean (object=%v record=%v placement=%+v)", w.medium.has(w.keyB), hasRecord, b)
		}
	case placement.Verified, placement.SourceDeletePending, placement.Done:
		if !hasB || b.Status != state.PlacementActive || !hasRecord || !w.medium.has(w.keyB) {
			t.Fatalf("at %s the encrypted copy must be catalogued, recorded and present (placement=%+v record=%v)", mv.Phase, b, hasRecord)
		}
	default:
		t.Fatalf("a cancel during verify left the move at %s, which is neither documented end state", mv.Phase)
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Errorf("the staging area holds %v after a cancel", left)
	}
}

type cancelOnRead struct {
	placement.MediumStore
	key    string
	cancel context.CancelFunc
}

func (c *cancelOnRead) OpenObject(ctx context.Context, medium transport.Medium, key string) (io.ReadCloser, error) {
	rc, err := c.MediumStore.OpenObject(ctx, medium, key)
	if err != nil || key != c.key {
		return rc, err
	}
	return &cancelAfterRead{ReadCloser: rc, cancel: c.cancel}, nil
}

// --- resume -----------------------------------------------------------------

// A run that stopped before the copy (the opt-in recorded, nothing else) is
// picked up by the next run for the same artifact and destination, and a move
// planned without an opt-in (a crash between the plan and the opt-in write) is
// adopted by it too, because the operator is asking for exactly that.
// Red if the second run plans a second move (state refuses it) instead of
// resuming, or abandons the first.
func TestARunResumesAMoveAnEarlierRunPlannedButNeverOptedIn_1291(t *testing.T) {
	w := newRetroWorld(t)

	mv, err := w.journal.PlanMove(w.ctx, state.MovePlan{
		Artifact: w.artifact, SourceMedium: testMedium,
		DestinationMedium: mediumB, DestinationKey: w.keyB, OccurredAt: w.clock,
	})
	if err != nil {
		t.Fatalf("planning the half-started move: %v", err)
	}

	report := w.encryptExisting()
	w.guard.fail()

	if report.Completed != 1 || report.Resumed != 1 {
		t.Fatalf("the run did not resume and finish the planned move: %+v", report)
	}
	if got := len(w.moves()); got != 2 {
		t.Fatalf("%d moves for the artifact, want the first hop and the one resumed move", got)
	}
	if w.lastMove().ID != mv.ID {
		t.Fatal("the run planned a new move instead of resuming the one that was waiting")
	}
	if consented, err := w.journal.EncryptExistingConsented(w.ctx, mv.ID); err != nil || !consented {
		t.Fatalf("the adopted move was not recorded as opted in (consented=%v err=%v)", consented, err)
	}
}

// --- refusals ---------------------------------------------------------------

// One named error per refusal, each returned by CheckEncryptExisting before
// any move row exists, and each leaving the plaintext copy alone. The verb's
// dry run and a real run share this function, so the dry run's table and the
// run's refusals cannot disagree.
func TestEveryRefusalHasItsOwnNamedErrorAndTouchesNothing_1291(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) *retroWorld
		want  error
	}{
		{
			name: "the set does not require encryption",
			setup: func(t *testing.T) *retroWorld {
				w := newRetroWorld(t)
				w.engine.Sets = fixedSets{set: plainSet(w)}
				return w
			},
			want: placement.ErrEncryptExistingNotRequired,
		},
		{
			name: "the copy is already encrypted",
			setup: func(t *testing.T) *retroWorld {
				e := newEncryptedTwoMediumFixture(t, fixtureOpts{content: multiChunk()})
				return &retroWorld{twoMediumFixture: e.twoMediumFixture, keys: e.keys, ck: e.ck}
			},
			want: placement.ErrEncryptExistingAlreadyEncrypted,
		},
		{
			name: "the only copy is on local disk",
			setup: func(t *testing.T) *retroWorld {
				f := newFixture(t, fixtureOpts{})
				provisionKeys(t, f)
				return &retroWorld{twoMediumFixture: &twoMediumFixture{fixture: f, b: nil}}
			},
			want: placement.ErrEncryptExistingNoPlaintextCopy,
		},
		{
			name: "no key is available",
			setup: func(t *testing.T) *retroWorld {
				w := newRetroWorld(t)
				w.engine.Keys = noKeys{}
				return w
			},
			want: placement.ErrEncryptExistingNoKey,
		},
		{
			name: "the destination can only verify at existence",
			setup: func(t *testing.T) *retroWorld {
				w := newRetroWorld(t)
				w.engine.Mediums = twoMediums{a: w.a, b: w.b, class: placement.Existence}
				return w
			},
			want: placement.ErrEncryptExistingDestinationClass,
		},
		{
			name: "the staging area has no room for the ciphertext",
			setup: func(t *testing.T) *retroWorld {
				w := newRetroWorld(t)
				w.engine.StagingFreeBytes = func(string) (int64, error) { return 1, nil }
				return w
			},
			want: placement.ErrEncryptExistingNoRoom,
		},
		{
			name: "the plaintext copy is on an archive class that cannot be read",
			setup: func(t *testing.T) *retroWorld {
				w := newRetroWorld(t)
				w.medium.archiveRefusesReads = true
				w.a.StorageClass = config.StorageClassDeepArchive
				return w
			},
			want: placement.ErrEncryptExistingSourceUnreadable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.setup(t)
			dest := mediumB
			if w.b == nil {
				dest = testMedium
			}
			err := w.engine.CheckEncryptExisting(w.ctx, placement.Plan{Artifact: w.artifact, DestinationMedium: dest})
			if !errors.Is(err, tc.want) {
				t.Fatalf("CheckEncryptExisting = %v, want errors.Is %v", err, tc.want)
			}
			for _, other := range []error{
				placement.ErrEncryptExistingNotRequired, placement.ErrEncryptExistingAlreadyEncrypted,
				placement.ErrEncryptExistingNoPlaintextCopy, placement.ErrEncryptExistingNoKey,
				placement.ErrEncryptExistingDestinationClass, placement.ErrEncryptExistingNoRoom,
				placement.ErrEncryptExistingSourceUnreadable,
			} {
				if other != tc.want && errors.Is(err, other) {
					t.Errorf("the refusal also matches %v, so two states share one error", other)
				}
			}
			if n := len(w.moves()); w.b != nil && n != 1 {
				t.Errorf("%d moves after a refusal, want only the first hop's", n)
			}
			if w.b != nil {
				if got := w.medium.bytesAt(w.srcKey); w.srcBytes != nil && !bytes.Equal(got, w.srcBytes) {
					t.Error("a refusal touched the plaintext object")
				}
			}
		})
	}
}

// The key refusal is also ErrClassUnavailable, the class every key problem
// already is, so callers that handle "could not check" keep handling it.
func TestTheNoKeyRefusalIsAlsoClassUnavailable_1291(t *testing.T) {
	w := newRetroWorld(t)
	w.engine.Keys = noKeys{}
	err := w.engine.CheckEncryptExisting(w.ctx, placement.Plan{Artifact: w.artifact, DestinationMedium: mediumB})
	if !errors.Is(err, placement.ErrEncryptExistingNoKey) || !errors.Is(err, placement.ErrClassUnavailable) {
		t.Fatalf("CheckEncryptExisting = %v, want both ErrEncryptExistingNoKey and ErrClassUnavailable", err)
	}
}

// A run over a refused plan reports it as refused, names the reason, and plans
// nothing. Red if a refusal becomes a move row.
func TestARefusedRunPlansNothingAndSaysWhy_1291(t *testing.T) {
	w := newRetroWorld(t)
	w.engine.Keys = noKeys{}

	report := w.encryptExisting()

	if report.Refused != 1 || report.Planned != 0 || len(report.Outcomes) != 1 {
		t.Fatalf("report %+v, want one refusal and nothing planned", report)
	}
	if !errors.Is(report.Outcomes[0].Err, placement.ErrEncryptExistingNoKey) {
		t.Fatalf("the outcome's error is %v, want ErrEncryptExistingNoKey", report.Outcomes[0].Err)
	}
	if n := len(w.moves()); n != 1 {
		t.Fatalf("%d moves, want only the first hop's", n)
	}
	w.plaintextIntact("after a refused run")
}

// A consented move whose set has stopped requiring encryption is abandoned,
// not carried out as a plaintext hop: the opt-in was for an encrypted copy.
// Red if the copy goes through as plaintext.
func TestAnOptedInMoveIsAbandonedIfTheSetStopsRequiringEncryption_1291(t *testing.T) {
	w := newRetroWorld(t)
	mv, err := w.journal.PlanMove(w.ctx, state.MovePlan{
		Artifact: w.artifact, SourceMedium: testMedium,
		DestinationMedium: mediumB, DestinationKey: w.keyB, OccurredAt: w.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.journal.RecordEncryptExisting(w.ctx, mv.ID, w.clock); err != nil {
		t.Fatal(err)
	}
	w.engine.Sets = fixedSets{set: plainSet(w)}

	if _, err := w.engine.RunCycle(w.ctx, nil); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	w.guard.fail()

	if got := placement.Phase(w.lastMove().Phase); got != placement.Abandoned {
		t.Fatalf("the move is at %s, want ABANDONED", got)
	}
	if w.medium.has(w.keyB) {
		t.Error("the artifact reached the destination in the clear")
	}
	w.plaintextIntact("after the abandon")
}

// --- the hop that was refused now works -------------------------------------

// After a run, the artifact's one ACTIVE copy is the encrypted one on B, and a
// medium-to-medium hop from it (a re-home) is defined and completes. This is
// the "no longer refused" half of the issue.
// Red if the encrypted copy cannot be re-homed afterwards.
func TestAfterTheRunTheCopyCanBeRehomedLikeAnyEncryptedCopy_1291(t *testing.T) {
	w := newRetroWorld(t)
	if report := w.encryptExisting(); report.Completed != 1 {
		t.Fatalf("control: the run did not complete: %+v", report)
	}

	report, err := w.engine.RunCycle(w.ctx, []placement.Plan{{Artifact: w.artifact, DestinationMedium: testMedium}})
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	w.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("re-homing the now-encrypted copy back to %s did not complete: %+v", testMedium, report)
	}
}
