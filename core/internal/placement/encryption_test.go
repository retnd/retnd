package placement_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
)

// These are the engine-side tests for EPIC N's N1.3 (#991): what the move
// engine does with a backup set whose retained_copy_encryption is
// "required". They use the same real journal, real local root and medium
// double as every other engine test, plus a real keycustody.Store, so the
// key the engine encrypts to and the key it decrypt-verifies with are the
// ones custody holds.
//
// The journal is deliberately NOT told about the key: the engine has to put
// the registry row there itself before it records a copy under it, because
// nothing else in this tree mirrors keycustody's registry into
// encryption_keys yet.

type encFixture struct {
	*fixture
	keys *keycustody.Store
	ck   keycustody.KeyRecord
}

func newEncryptedFixture(t *testing.T, opts fixtureOpts) *encFixture {
	t.Helper()
	f := newFixture(t, opts)

	dir := filepath.Join(t.TempDir(), "keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	reg, err := keycustody.OpenFileRegistry(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatalf("OpenFileRegistry: %v", err)
	}
	keys, err := keycustody.Open(dir, reg, keycustody.AgeFormat{})
	if err != nil {
		t.Fatalf("keycustody.Open: %v", err)
	}
	rec, err := keys.Create(f.ctx, testNow2.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	bs := config.BackupSet{
		Name: testSet, ID: f.artifact.Set, LocalPath: f.localDir,
		RetainedCopyEncryption: config.RetainedCopyEncryption{
			Mode:        config.RetainedCopyEncryptionRequired,
			KeyProvider: config.RetainedCopyKeyProvider{Managed: true},
		},
	}
	f.engine.Sets = fixedSets{set: bs}
	f.engine.Keys = keys
	return &encFixture{fixture: f, keys: keys, ck: rec}
}

func (f *encFixture) staging() []string {
	f.t.Helper()
	es, err := os.ReadDir(filepath.Join(f.localDir, placement.StagingDirName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func (f *encFixture) encryption() (state.PlacementEncryption, bool) {
	f.t.Helper()
	e, ok, err := f.journal.PlacementEncryption(f.ctx, f.artifact, testMedium)
	if err != nil {
		f.t.Fatal(err)
	}
	return e, ok
}

func (e *encFixture) objectKey() string { return e.fixture.key }

func TestTheStoredObjectIsAgeCiphertextThatDecryptsToTheArtifact(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	f.runCycle()
	f.guard.fail()

	obj := f.medium.bytesAt(f.objectKey())
	if len(obj) == 0 {
		t.Fatalf("nothing at %q on the medium (move error: %q)", f.objectKey(), f.onlyMove().Error)
	}
	if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) {
		t.Fatal("the object at the key is not an age v1 stream")
	}
	if bytes.Contains(obj, f.content[:32]) {
		t.Fatal("the artifact's plaintext appears in the stored object")
	}
	err := f.keys.WithKey(f.ctx, f.ck.ID, func(k []byte) error {
		_, err := copycrypt.DecryptVerify(f.ctx, bytes.NewReader(obj), k, f.hash)
		return err
	})
	if err != nil {
		t.Fatalf("the stored object does not decrypt to the artifact: %v", err)
	}
}

func TestBothDigestColumnsAreWrittenInTheSameAdvanceAndNeverConfused(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	f.runCycle()
	f.guard.fail()

	obj := f.medium.bytesAt(f.objectKey())
	dst, ok := f.placement(testMedium)
	if !ok {
		t.Fatal("no placement records the destination copy")
	}
	if dst.Hash != f.hash || dst.HashAlg != "sha256" {
		t.Fatalf("placements.hash = %s/%s, want the artifact's plaintext digest %s", dst.Hash, dst.HashAlg, f.hash)
	}
	if dst.Size == nil || *dst.Size != int64(len(f.content)) {
		t.Fatalf("placements.size_bytes = %v, want the plaintext size %d", dst.Size, len(f.content))
	}
	if dst.VerificationClass != state.VerificationContent {
		t.Fatalf("class = %q, want content", dst.VerificationClass)
	}

	enc, ok := f.encryption()
	if !ok {
		t.Fatal("an ACTIVE encrypted copy has no placement_encryption record")
	}
	if enc.FormatID != copycrypt.FormatID || enc.FormatVersion != copycrypt.FormatVersion {
		t.Fatalf("format = %s/%d, want %s/%d", enc.FormatID, enc.FormatVersion, copycrypt.FormatID, copycrypt.FormatVersion)
	}
	if enc.KeyID != f.ck.ID {
		t.Fatalf("key id = %q, want %q", enc.KeyID, f.ck.ID)
	}
	if enc.StoredHash != sha256Hex(obj) || enc.StoredSize != int64(len(obj)) || enc.StoredHashAlg != "sha256" {
		t.Fatalf("stored digest/size = %s/%d, but the object at the key is %s/%d", enc.StoredHash, enc.StoredSize, sha256Hex(obj), len(obj))
	}
	if enc.StoredHash == f.hash {
		t.Fatal("the stored-bytes digest equals the plaintext digest")
	}
}

func TestTheStagingAreaIsEmptyAfterAnEncryptedMove(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})

	// A temporary file an earlier, killed pass left behind is swept by the
	// same sweep a staged medium-to-medium hop uses.
	dir := filepath.Join(f.localDir, placement.StagingDirName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".artifactstore-leftover"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	f.runCycle()
	f.guard.fail()
	if left := f.staging(); len(left) != 0 {
		t.Fatalf("the staging area holds %v after an encrypted move", left)
	}
}

func TestAnArtifactThatNoLongerHashesToTheJournalIsNeverEncryptedOrUploaded(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	// The local bytes drift after the journal recorded the artifact: the
	// recorded artifact digest no longer describes what is on disk.
	if err := os.WriteFile(f.localPath(), []byte("not the bytes the journal recorded"), 0o600); err != nil {
		t.Fatal(err)
	}

	f.runCycle()

	if n := f.medium.uploadCount(); n != 0 {
		t.Fatalf("%d upload(s) of bytes that do not match the recorded artifact digest", n)
	}
	if _, ok := f.placement(testMedium); ok {
		t.Fatal("a destination placement was recorded for a copy of bytes that do not match the journal")
	}
	if _, ok := f.encryption(); ok {
		t.Fatal("a placement_encryption record exists for a copy that was never made")
	}
	if !f.localExists() {
		t.Fatal("the local source is gone")
	}
	if left := f.staging(); len(left) != 0 {
		t.Fatalf("the staging area holds %v", left)
	}
}

type noKeys struct{}

func (noKeys) Active(context.Context) (keycustody.KeyRecord, bool, error) {
	return keycustody.KeyRecord{}, false, nil
}

func (noKeys) WithKey(context.Context, string, func([]byte) error) error {
	return errors.New("no such key")
}

type unreadableKeys struct{ keycustody.KeyRecord }

func (u unreadableKeys) Active(context.Context) (keycustody.KeyRecord, bool, error) {
	return u.KeyRecord, true, nil
}

func (unreadableKeys) WithKey(context.Context, string, func([]byte) error) error {
	return errors.New("the key file is not readable")
}

func TestAnUnresolvableKeyIsClassUnavailableAndRecordsNoClass(t *testing.T) {
	for name, keys := range map[string]func(*encFixture) placement.Keys{
		"no engine key source": func(*encFixture) placement.Keys { return nil },
		"no active key":        func(*encFixture) placement.Keys { return noKeys{} },
		"active key unreadable": func(f *encFixture) placement.Keys {
			return unreadableKeys{f.ck}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newEncryptedFixture(t, fixtureOpts{})
			f.engine.Keys = keys(f)

			f.runCycle()
			f.guard.fail()

			mv := f.onlyMove()
			if placement.Phase(mv.Phase) == placement.Done {
				t.Fatal("a move completed with an unresolvable key")
			}
			if n := f.medium.uploadCount(); n != 0 {
				t.Fatalf("%d upload(s) before the key was known to resolve", n)
			}
			if p, ok := f.placement(testMedium); ok {
				t.Fatalf("a destination placement was recorded (class %q); an unresolvable key must record no class", p.VerificationClass)
			}
			if _, ok := f.encryption(); ok {
				t.Fatal("a placement_encryption record was written")
			}
			if mv.Error == "" {
				t.Fatal("the move row says nothing about why it did not advance")
			}
		})
	}
}

func TestAnEncryptedCopyIsNeverRecordedAtExistenceOrAttested(t *testing.T) {
	for _, class := range []placement.Class{placement.Existence, placement.Attested} {
		t.Run(string(class), func(t *testing.T) {
			f := newEncryptedFixture(t, fixtureOpts{class: class, attests: true})

			f.runCycle()

			if p, ok := f.placement(testMedium); ok && p.Status == state.PlacementActive {
				t.Fatalf("an encrypted copy was recorded ACTIVE at class %q with no decrypt-verify", p.VerificationClass)
			}
			if _, ok := f.encryption(); ok {
				t.Fatal("a placement_encryption record was written for a copy no decrypt-verify passed")
			}
			if !f.localExists() {
				t.Fatal("the local source was deleted on the strength of a class that does not prove the copy decrypts")
			}
		})
	}
}

func TestACiphertextDamagedAtTheDestinationFailsAndRecordsNothing(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	f.medium.truncate = 200 // header plus a little: no final chunk

	f.runCycle()
	f.guard.fail()

	if placement.Phase(f.onlyMove().Phase) == placement.Done {
		t.Fatal("the move completed over a truncated ciphertext")
	}
	if p, ok := f.placement(testMedium); ok && p.Status == state.PlacementActive {
		t.Fatalf("an ACTIVE destination placement exists over a truncated ciphertext (class %q)", p.VerificationClass)
	}
	if _, ok := f.encryption(); ok {
		t.Fatal("a placement_encryption record exists over a truncated ciphertext")
	}
	if !f.localExists() {
		t.Fatal("the local source was deleted")
	}
}

func TestTwoSuccessiveCopiesToOneKeyLeaveOneObjectAndTheDigestOfThatObject(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})

	// First attempts are truncated and fail verification, so the engine
	// has written different ciphertext at the key more than once before the
	// attempt that sticks.
	f.medium.truncate = 200
	f.runCycle()
	if _, ok := f.encryption(); ok {
		t.Fatal("a record was written by an attempt that failed")
	}
	f.medium.truncate = 0
	f.runCycle()
	f.guard.fail()

	if n := f.medium.uploadCount(); n < 3 {
		t.Fatalf("only %d upload(s); this test needs a re-copy over an earlier attempt", n)
	}
	if n := f.medium.keyCount(); n != 1 {
		t.Fatalf("%d objects on the medium, want one at the single deterministic key", n)
	}
	enc, ok := f.encryption()
	if !ok {
		t.Fatalf("no placement_encryption record after the move that completed (move error %q)", f.onlyMove().Error)
	}
	if got := sha256Hex(f.medium.bytesAt(f.objectKey())); enc.StoredHash != got {
		t.Fatalf("the recorded stored digest %s describes an object that is not the one at the key (%s)", enc.StoredHash, got)
	}
}

// cancelingLocal cancels the engine's context once the artifact has been
// read part-way, which is an interruption the encrypt pass cannot ignore.
type cancelingLocal struct {
	*guardedLocal
	cancel context.CancelFunc
}

func (c *cancelingLocal) Open(ctx context.Context, locator string) (io.ReadCloser, error) {
	rc, err := c.guardedLocal.Open(ctx, locator)
	if err != nil {
		return nil, err
	}
	return &cancelAfterRead{ReadCloser: rc, cancel: c.cancel}, nil
}

type cancelAfterRead struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelAfterRead) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p[:min(len(p), 8)])
	r.cancel()
	return n, err
}

func TestAnInterruptedEncryptCannotBecomeACataloguedGoodCopy(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.engine.Local = &cancelingLocal{guardedLocal: f.local, cancel: cancel}

	_, _ = f.engine.RunCycle(ctx, []placement.Plan{{Artifact: f.artifact, DestinationMedium: testMedium}})

	if n := f.medium.uploadCount(); n != 0 {
		t.Fatalf("%d upload(s) of a ciphertext whose encryption was interrupted", n)
	}
	if _, ok := f.placement(testMedium); ok {
		t.Fatal("a destination placement exists for an interrupted encrypt")
	}
	if _, ok := f.encryption(); ok {
		t.Fatal("a placement_encryption record exists for an interrupted encrypt")
	}
	mv := f.onlyMove()
	if placement.Phase(mv.Phase) != placement.Copying {
		t.Fatalf("the move is at %s, want it still at COPYING so the next cycle re-encrypts from scratch", mv.Phase)
	}
	if left := f.staging(); len(left) != 0 {
		t.Fatalf("the staging area holds %v after an interrupted encrypt", left)
	}
	if !f.localExists() {
		t.Fatal("the local source is gone")
	}
}

func TestAnEncryptedCopyIsNotHandedBackAsCiphertextByAMoveHome(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	f.runCycle()
	f.guard.fail()
	if _, ok := f.encryption(); !ok {
		t.Fatalf("setup: the first move did not leave an encrypted copy (move error %q)", f.moves()[0].Error)
	}

	_, _ = f.engine.RunCycle(f.ctx, []placement.Plan{{Artifact: f.artifact, DestinationMedium: config.MediumLocal}})

	if f.localExists() {
		got, _ := os.ReadFile(f.localPath())
		if !bytes.Equal(got, f.content) {
			t.Fatal("ciphertext was written to the artifact's local path")
		}
	}
	if !f.medium.has(f.objectKey()) {
		t.Fatal("the only copy, on the medium, is gone")
	}
}

func TestAPlaintextSetStillMovesExactlyAsBefore(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	f.runCycle()
	f.guard.fail()

	if got := f.medium.bytesAt(f.key); !bytes.Equal(got, f.content) {
		t.Fatal("a set without encryption no longer stores the artifact verbatim")
	}
	if _, ok, err := f.journal.PlacementEncryption(f.ctx, f.artifact, testMedium); err != nil || ok {
		t.Fatalf("a plaintext copy has a placement_encryption record (ok=%v err=%v)", ok, err)
	}
}
