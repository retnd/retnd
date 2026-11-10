package placement_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/recovery"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// These are EPIC N's N1.4 (#992) tests for the lifecycle of an encrypted
// copy once it exists: verifying it after the key it was written under has
// stopped being the active one, deleting it with no key at all, moving it
// between mediums without ever re-encrypting it, and the sidecar and feed
// facts that make it findable and countable.
//
// They were written by reading the engine and the fixtures and have not been
// run. Each one names the defect that turns it red in its own comment.

// multiChunk is an artifact of several 64 KiB age chunks and a short final
// one, with no zero bytes (ADR 0027 asks #991 and #996 for fixtures of this
// shape and the same is true here).
func multiChunk() []byte {
	b := make([]byte, 5*64*1024+777)
	for i := range b {
		b[i] = byte((i*131+i/251)%251 + 1)
	}
	return b
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// provisionKeys is newEncryptedFixture's key setup for a fixture the caller
// has already wired some other way.
func provisionKeys(t *testing.T, f *fixture) (*keycustody.Store, keycustody.KeyRecord) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	keys, err := keycustody.Open(dir, keycustody.NewJournalRegistry(f.journal), keycustody.AgeFormat{})
	if err != nil {
		t.Fatalf("keycustody.Open: %v", err)
	}
	rec, err := keys.Create(f.ctx, testNow2.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f.engine.Sets = fixedSets{set: config.BackupSet{
		Name: testSet, ID: f.artifact.Set, LocalPath: f.localDir,
		RetainedCopyEncryption: config.RetainedCopyEncryption{
			Mode:        config.RetainedCopyEncryptionRequired,
			KeyProvider: config.RetainedCopyKeyProvider{Managed: true},
		},
	}}
	f.engine.Keys = keys
	return keys, rec
}

// encTwo is the encrypted world with two mediums: the artifact has been
// moved, for real, from local disk onto medium A under the managed key, and
// the next hop is A to B.
type encTwo struct {
	*twoMediumFixture
	keys *keycustody.Store
	ck   keycustody.KeyRecord
}

func newEncryptedTwoMediumFixture(t *testing.T, opts fixtureOpts) *encTwo {
	t.Helper()
	f := newFixture(t, opts)
	keys, ck := provisionKeys(t, f)

	a := &transport.Medium{ID: testMedium, Type: transport.MediumTypeS3, Bucket: "nas-backups", Prefix: "retnd"}
	b := &transport.Medium{ID: mediumB, Type: transport.MediumTypeS3, Bucket: "nas-annual", Prefix: "annual"}
	f.engine.Mediums = twoMediums{a: a, b: b, class: placement.Content}
	local := &recordingLocal{LocalStore: f.engine.Local}
	f.engine.Local = local
	store := &recordingStore{MediumStore: f.engine.Store, opens: map[string]int{}}
	f.engine.Store = store
	keyB, err := transport.MediumKey(b.Prefix, f.artifact)
	if err != nil {
		t.Fatalf("computing the second medium's key: %v", err)
	}
	tf := &twoMediumFixture{fixture: f, a: a, b: b, keyB: keyB, local: local, store: store}
	if report := f.runCycle(); report.Completed != 1 {
		t.Fatalf("the encrypted local -> %s hop this world is built on did not complete: %+v", testMedium, report)
	}
	return &encTwo{twoMediumFixture: tf, keys: keys, ck: ck}
}

func (f *encTwo) recorded(medium string) (state.PlacementEncryption, bool) {
	f.t.Helper()
	e, ok, err := f.journal.PlacementEncryption(f.ctx, f.artifact, medium)
	if err != nil {
		f.t.Fatal(err)
	}
	return e, ok
}

// moveTo is the one move whose destination is medium. The fixture already
// made the first hop, so onlyMove (exactly one move) is the wrong question
// here; this asks for the hop under test and fails if there is not exactly one.
func (f *encTwo) moveTo(medium string) state.Move {
	f.t.Helper()
	var hops []state.Move
	for _, m := range f.moves() {
		if m.DestinationMedium == medium {
			hops = append(hops, m)
		}
	}
	if len(hops) != 1 {
		f.t.Fatalf("expected exactly one move to %q for %s, got %d", medium, f.artifact, len(hops))
	}
	return hops[0]
}

// keysWithout is custody that has lost one key: every other key still opens.
type keysWithout struct {
	placement.Keys
	gone string
}

func (k keysWithout) WithKey(ctx context.Context, id string, fn func([]byte) error) error {
	if id == k.gone {
		return keycustody.ErrUnknownKey
	}
	return k.Keys.WithKey(ctx, id, fn)
}

// --- re-homing an encrypted copy ---------------------------------------

// The defined semantics of a medium-to-medium move of an encrypted copy: the
// ciphertext is copied as it is (no key needed to copy, nothing is
// re-encrypted), the destination is accepted only by a decrypt-verify, and
// the destination row carries the same per-copy record as the source.
// Red if the engine still refuses with ErrClassRefused, or re-encrypts.
func TestAnEncryptedCopyIsRehomedAsTheSameCiphertextAndRecordedUnderTheSameKey_992(t *testing.T) {
	f := newEncryptedTwoMediumFixture(t, fixtureOpts{content: multiChunk()})
	before := append([]byte(nil), f.medium.bytesAt(f.key)...)
	srcRec, ok := f.recorded(testMedium)
	if !ok {
		t.Fatal("the first hop left no encryption record on the source copy")
	}

	report := f.moveToB()
	f.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("the re-home did not complete: %+v (move error %q)", report, f.moveTo(mediumB).Error)
	}

	after := f.medium.bytesAt(f.keyB)
	if !bytes.Equal(before, after) {
		t.Fatalf("the object on %s is not the ciphertext that was on %s: a re-home must not re-encrypt (a retry is a fresh encryption, a move is not a retry)", mediumB, testMedium)
	}
	if f.medium.has(f.key) {
		t.Errorf("the source object is still on %s after a completed move", testMedium)
	}

	dst, ok := f.recorded(mediumB)
	if !ok {
		t.Fatal("the destination copy has no encryption record, so it reads as plaintext")
	}
	if dst.KeyID != srcRec.KeyID || dst.FormatID != srcRec.FormatID || dst.FormatVersion != srcRec.FormatVersion {
		t.Errorf("destination record %+v, want the source's key and format %+v", dst, srcRec)
	}
	if dst.StoredHash != sum(after) || dst.StoredSize != int64(len(after)) {
		t.Errorf("destination stored digest/size %s/%d do not describe the bytes at the key (%s/%d)", dst.StoredHash, dst.StoredSize, sum(after), len(after))
	}
	row, ok := f.placement(mediumB)
	if !ok {
		t.Fatal("no placement row for the destination")
	}
	if row.Hash != f.hash || row.VerificationClass != state.VerificationContent {
		t.Errorf("destination placements.hash/class = %s/%s, want the plaintext digest %s and content", row.Hash, row.VerificationClass, f.hash)
	}
	if row.Size == nil || *row.Size != int64(len(f.content)) {
		t.Errorf("destination placements.size_bytes = %v, want the plaintext size %d", row.Size, len(f.content))
	}
	if left := f.stagingLeftovers(); len(left) != 0 {
		t.Errorf("staging was not swept after the re-home: %v", left)
	}
	if bytes.Contains(f.medium.bytesAt(f.keyB), f.content[:64]) {
		t.Error("plaintext appears in the re-homed object")
	}
}

// A rotation between the first hop and the second must not matter: the copy
// is read through the key id recorded against it, never the active key.
// Red if the destination is verified through Active() (it fails to decrypt,
// the move never completes) or recorded under the new key id.
func TestARehomeAfterARotationUsesTheRecordedKeyNotTheActiveOne_992(t *testing.T) {
	f := newEncryptedTwoMediumFixture(t, fixtureOpts{})
	rotated, err := f.keys.Rotate(f.ctx, testNow2)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if rotated.ID == f.ck.ID {
		t.Fatal("control: the rotation did not mint a different key, so this test cannot tell the two apart")
	}

	report := f.moveToB()
	f.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("a re-home after a rotation did not complete: %+v (move error %q)", report, f.moveTo(mediumB).Error)
	}
	dst, ok := f.recorded(mediumB)
	if !ok || dst.KeyID != f.ck.ID {
		t.Errorf("destination record %+v (ok=%v), want it under the key it was written with, %s, not the active %s", dst, ok, f.ck.ID, rotated.ID)
	}
}

// Without the recorded key the destination cannot be accepted, so the source
// must survive: nothing weaker than a completed decrypt-verify stands in for
// content, and the refusal comes before any delete.
func TestARehomeThatCannotDecryptTheDestinationKeepsTheSource_992(t *testing.T) {
	f := newEncryptedTwoMediumFixture(t, fixtureOpts{})
	f.engine.Keys = keysWithout{Keys: f.keys, gone: f.ck.ID}

	report := f.moveToB()
	f.guard.fail()
	if report.Completed != 0 {
		t.Fatalf("a re-home completed with no way to decrypt the destination: %+v", report)
	}
	if !f.medium.has(f.key) {
		t.Fatal("the source object was deleted although the destination was never decrypt-verified")
	}
	if p, ok := f.placement(testMedium); !ok || p.Status != state.PlacementActive {
		t.Errorf("source placement = %+v (ok=%v), want it still ACTIVE", p, ok)
	}
	if p, ok := f.placement(mediumB); ok && p.Status == state.PlacementActive {
		t.Errorf("an ACTIVE destination placement exists (%+v) without a decrypt-verify", p)
	}
}

// The staged ciphertext is accepted only if it hashes to the stored-bytes
// digest the journal recorded, not the plaintext digest (which no ciphertext
// can match). A source object that was altered at rest is therefore refused at
// the staging step, before anything reaches the destination.
func TestAStagedCiphertextThatIsNotTheRecordedStoredBytesIsRefused_992(t *testing.T) {
	f := newEncryptedTwoMediumFixture(t, fixtureOpts{})
	f.medium.mu.Lock()
	obj := f.medium.objects[f.key]
	obj[len(obj)/2] ^= 0xff
	f.medium.mu.Unlock()

	report := f.moveToB()
	f.guard.fail()
	if report.Completed != 0 {
		t.Fatalf("a re-home completed from a source whose bytes are not the recorded ciphertext: %+v", report)
	}
	if f.medium.has(f.keyB) {
		t.Error("an object reached the destination from a source that failed its stored-bytes check")
	}
	if !f.medium.has(f.key) {
		t.Error("the source object is gone")
	}
}

// A staging file left by a killed pass (here: a truncated one at the staging
// name) is not trusted. It does not hash to the stored bytes, so it is
// replaced, and the move completes.
func TestARehomeOverAStaleStagingFileStillCompletes_992(t *testing.T) {
	f := newEncryptedTwoMediumFixture(t, fixtureOpts{content: multiChunk()})
	if err := os.MkdirAll(f.stagingDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.stagingPath(), f.medium.bytesAt(f.key)[:100], 0o600); err != nil {
		t.Fatal(err)
	}
	report := f.moveToB()
	f.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("a stale staging file blocked the re-home: %+v (move error %q)", report, f.moveTo(mediumB).Error)
	}
	if _, ok := f.recorded(mediumB); !ok {
		t.Error("the destination has no encryption record")
	}
}

// Moving an encrypted copy back to local disk stays refused, now for a stated
// reason: a local placement is plaintext by design, so bringing the artifact
// home is a decrypt (a restore), not a move of the copy.
func TestAnEncryptedCopyIsNeverMovedToLocalAsCiphertext_992(t *testing.T) {
	f := newEncryptedTwoMediumFixture(t, fixtureOpts{})
	report, err := f.engine.RunCycle(f.ctx, []placement.Plan{{Artifact: f.artifact, DestinationMedium: state.MediumLocal}})
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	f.guard.fail()
	if report.Completed != 0 {
		t.Fatalf("an encrypted copy was moved to local disk: %+v", report)
	}
	if f.localExists() {
		t.Error("ciphertext was written to the artifact's local path")
	}
	if !f.medium.has(f.key) {
		t.Error("the encrypted source was deleted")
	}
	if msg := f.moveTo(state.MediumLocal).Error; !strings.Contains(msg, "restore") {
		t.Errorf("the refusal does not say that bringing an encrypted copy home is a restore: %q", msg)
	}
}

// --- verification through the recorded key -----------------------------

func (f *encFixture) medium0(t *testing.T) transport.Medium {
	t.Helper()
	m, _, err := f.engine.Mediums.Resolve(testMedium)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func encryptedCopy(t *testing.T, f *encFixture) (state.Placement, state.PlacementEncryption) {
	t.Helper()
	f.runCycle()
	p, ok := f.placement(testMedium)
	if !ok {
		t.Fatalf("no placement on %s (move error %q)", testMedium, f.onlyMove().Error)
	}
	enc, ok := f.encryption()
	if !ok {
		t.Fatal("no encryption record for the copy")
	}
	return p, enc
}

// Rotation orphans nothing (N1.11): every pre-rotation copy still
// decrypt-verifies through its recorded key id, after one and after two
// rotations. Red if verification goes through the active key.
func TestARotationLeavesEveryPreRotationCopyDecryptVerifyingThroughItsRecordedKey_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	p, enc := encryptedCopy(t, f)
	medium := f.medium0(t)

	for i := 0; i < 2; i++ {
		rotated, err := f.keys.Rotate(f.ctx, testNow2.Add(time.Duration(i+1)*time.Hour))
		if err != nil {
			t.Fatalf("Rotate: %v", err)
		}
		if rotated.ID == enc.KeyID {
			t.Fatal("control: rotation did not change the active key")
		}
		res, err := placement.VerifyCopy(f.ctx, f.medium, f.keys, medium, p, &enc, placement.Content, testNow2)
		if err != nil {
			t.Fatalf("after rotation %d: %v", i+1, err)
		}
		if !res.Passed || res.Class != placement.Content {
			t.Fatalf("after rotation %d the copy no longer decrypt-verifies: %+v", i+1, res)
		}
		if res.Encryption == nil || res.Encryption.KeyID != enc.KeyID {
			t.Errorf("verified through %+v, want the recorded key %s", res.Encryption, enc.KeyID)
		}
	}
}

// The generic ladder compares a plaintext size and digest to the object's,
// which for an encrypted copy are always different numbers. Existence on an
// encrypted copy compares the stored size. The control half asserts the
// generic ladder does give the wrong answer, so the right one is not
// vacuous.
func TestExistenceOfAnEncryptedCopyComparesTheStoredSize_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	p, enc := encryptedCopy(t, f)
	medium := f.medium0(t)

	naive, err := placement.Verify(f.ctx, f.medium, medium, p, placement.Existence, testNow2)
	if err != nil {
		t.Fatal(err)
	}
	if naive.Passed {
		t.Fatal("control: the plaintext-size comparison passed an encrypted copy, so the stored-size comparison proves nothing")
	}
	res, err := placement.VerifyCopy(f.ctx, f.medium, f.keys, medium, p, &enc, placement.Existence, testNow2)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || res.Class != placement.Existence {
		t.Errorf("existence of an intact encrypted copy: %+v", res)
	}
	if res.Encryption != nil {
		t.Error("an existence check must not carry a per-copy record: only a decrypt-verify writes one")
	}
}

// An unresolvable key is "could not check", never a weaker class: no HEAD,
// no read is made in its place.
func TestContentOfAnEncryptedCopyWithoutItsKeyIsUnavailableNotWeaker_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	p, enc := encryptedCopy(t, f)
	medium := f.medium0(t)
	stats, checks := f.medium.statCount(), f.medium.checksumCount()

	for name, keys := range map[string]placement.Keys{
		"no custody":      nil,
		"the key is gone": keysWithout{Keys: f.keys, gone: enc.KeyID},
	} {
		res, err := placement.VerifyCopy(f.ctx, f.medium, keys, medium, p, &enc, placement.Content, testNow2)
		if !errors.Is(err, placement.ErrClassUnavailable) || !errors.Is(err, placement.ErrKeyUnresolvable) {
			t.Errorf("%s: err = %v, want ErrClassUnavailable wrapping ErrKeyUnresolvable", name, err)
		}
		if res.Class != "" || res.Passed {
			t.Errorf("%s: a result was produced: %+v", name, res)
		}
	}
	if f.medium.statCount() != stats || f.medium.checksumCount() != checks {
		t.Error("a weaker check was run in place of the decrypt-verify that could not be")
	}
}

// Attested compares the endpoint's digest of the object to a recorded
// digest, and the recorded plaintext digest is not the object's. Unavailable,
// not a quiet comparison against stored_hash dressed as the same class.
func TestAttestedIsUnavailableForAnEncryptedCopy_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{attests: true})
	p, enc := encryptedCopy(t, f)
	_, err := placement.VerifyCopy(f.ctx, f.medium, f.keys, f.medium0(t), p, &enc, placement.Attested, testNow2)
	if !errors.Is(err, placement.ErrClassUnavailable) {
		t.Errorf("err = %v, want ErrClassUnavailable", err)
	}
}

// Altered bytes at the key are a failed verification (a fact about the
// object), not an error.
func TestAnEncryptedCopyThatWasAlteredAtRestFailsContent_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{content: multiChunk()})
	p, enc := encryptedCopy(t, f)
	f.medium.mu.Lock()
	f.medium.objects[f.objectKey()][70000] ^= 0x01
	f.medium.mu.Unlock()

	res, err := placement.VerifyCopy(f.ctx, f.medium, f.keys, f.medium0(t), p, &enc, placement.Content, testNow2)
	if err != nil {
		t.Fatalf("a failed authentication must be a result, got error %v", err)
	}
	if res.Passed || res.Class != placement.Content {
		t.Errorf("an altered object verified: %+v", res)
	}
}

// --- retention needs no key --------------------------------------------

// Deleting an encrypted copy under retention succeeds with no key resolver
// configured at all: the Reclaimer has no Keys field, the key custody
// directory is removed for good measure, and the object is not even read.
// Red if the Reclaimer compares the plaintext size or digest to the object
// (it refuses every encrypted copy) or asks the key anything.
func TestDeletingAnEncryptedCopyNeedsNoKeyAtAll_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{content: multiChunk()})
	encryptedCopy(t, f)
	opens := f.medium.openCount()
	f.engine.Keys = nil

	r := &placement.Reclaimer{Store: f.medium, Mediums: f.engine.Mediums, Encryption: f.journal, Now: func() time.Time { return testNow2 }}
	if err := r.DeleteFromMedium(f.ctx, f.record(), testMedium); err != nil {
		t.Fatalf("retention could not delete an encrypted copy with no key: %v", err)
	}
	if f.medium.has(f.objectKey()) {
		t.Error("the object is still on the medium")
	}
	if f.medium.openCount() != opens {
		t.Error("deleting an encrypted copy read it back; the guard ladder never asks for content")
	}
}

// With an endpoint that can attest a SHA-256, the attestation is compared to
// the stored-bytes digest and the delete proceeds on it: key-free identity
// evidence that does not mistake the stored digest for the artifact's.
func TestAnAttestingEndpointIsComparedAgainstTheStoredBytesDigest_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{attests: true})
	encryptedCopy(t, f)
	before := f.medium.checksumCount()

	r := &placement.Reclaimer{Store: f.medium, Mediums: f.engine.Mediums, Encryption: f.journal, Now: func() time.Time { return testNow2 }}
	if err := r.DeleteFromMedium(f.ctx, f.record(), testMedium); err != nil {
		t.Fatalf("delete refused although the attestation matches the stored bytes: %v", err)
	}
	if f.medium.checksumCount() == before {
		t.Error("the endpoint was never asked to attest")
	}
}

// A reclaimer that cannot see the encryption record would judge the object
// against the plaintext numbers, which can never match, and so refuses. It
// fails closed rather than misjudging, and the object stays.
func TestAReclaimerThatCannotSeeTheRecordRefusesRatherThanMisjudge_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	encryptedCopy(t, f)

	r := &placement.Reclaimer{Store: f.medium, Mediums: f.engine.Mediums, Now: func() time.Time { return testNow2 }}
	if err := r.DeleteFromMedium(f.ctx, f.record(), testMedium); err == nil {
		t.Fatal("a reclaimer with no encryption reader deleted an encrypted copy on plaintext numbers")
	}
	if !f.medium.has(f.objectKey()) {
		t.Error("the object was deleted")
	}
}

// --- the sidecar ---------------------------------------------------------

func writeSidecar(t *testing.T, f *fixture) string {
	t.Helper()
	m := recovery.Manifest{
		FormatVersion: recovery.CurrentFormatVersion, Source: testSource, BackupSet: testSet,
		ArtifactName: f.artifact.Name, RemotePath: "incoming/" + f.artifact.Name,
		ReceivedTimestamp: testNow2.Add(-48 * time.Hour), RetentionTimestamp: testNow2.Add(-48 * time.Hour),
		SizeBytes: int64(len(f.content)), Checksum: f.hash, ChecksumAlgorithm: "sha256",
		Placements: []recovery.ManifestPlacement{{Medium: state.MediumLocal, Location: f.localPath(), Status: "ACTIVE"}},
	}
	if err := recovery.WriteManifest(f.localDir, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	return recovery.ManifestPath(f.localDir, f.artifact.Name)
}

// The sidecar learns about the encrypted copy before the source is deleted,
// carrying the key id, both digests' roles, both sizes and the time of the
// decrypt-verify that admitted the copy. Red if the sidecar is left as the
// commit wrote it.
func TestTheSidecarIsRefreshedWithTheEncryptedCopyBeforeTheSourceGoes_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{content: multiChunk()})
	path := writeSidecar(t, f.fixture)

	f.runCycle()
	f.guard.fail()
	m, err := recovery.ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	var got *recovery.ManifestPlacement
	for i := range m.Placements {
		if m.Placements[i].Medium == testMedium {
			got = &m.Placements[i]
		}
	}
	if got == nil || got.Encryption == nil {
		t.Fatalf("the sidecar does not describe the encrypted copy on %s: %+v", testMedium, m.Placements)
	}
	obj := f.medium.bytesAt(f.objectKey())
	e := got.Encryption
	if e.FormatID != "age/v1" || e.FormatVersion != 1 || e.KeyID != f.ck.ID {
		t.Errorf("format/key = %s/%d/%s, want age/v1/1/%s", e.FormatID, e.FormatVersion, e.KeyID, f.ck.ID)
	}
	if e.PlaintextDigest != f.hash || e.PlaintextSize != int64(len(f.content)) {
		t.Errorf("plaintext digest/size = %s/%d, want %s/%d", e.PlaintextDigest, e.PlaintextSize, f.hash, len(f.content))
	}
	if e.StoredDigest != sum(obj) || e.StoredSize != int64(len(obj)) || e.StoredDigest == e.PlaintextDigest {
		t.Errorf("stored digest/size = %s/%d, want %s/%d and not the plaintext digest", e.StoredDigest, e.StoredSize, sum(obj), len(obj))
	}
	if e.LastDecryptVerified == nil || e.LastDecryptVerified.IsZero() {
		t.Error("the sidecar carries no last decrypt-verification time")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.keys.WithKey(f.ctx, f.ck.ID, func(k []byte) error {
		if bytes.Contains(raw, bytes.TrimSpace(k)) {
			t.Error("the sidecar contains key material")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A sidecar that cannot be written stops the move before the source is
// deleted (a copy whose key a state-less recovery cannot learn is not yet a
// durable copy), loses nothing, and the next cycle completes it. The cell is
// the reconcile half of "a crash at each new durable boundary".
func TestASidecarThatCannotBeWrittenKeepsTheSourceAndTheNextCycleCompletes_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	path := writeSidecar(t, f.fixture)
	block := path + ".tmp"
	if err := os.Mkdir(block, 0o700); err != nil {
		t.Fatal(err)
	}

	report := f.runCycle()
	f.guard.fail()
	if report.Completed != 0 {
		t.Fatalf("the move completed although the sidecar could not be written: %+v", report)
	}
	if !f.localExists() {
		t.Fatal("the local source was deleted before the sidecar described the encrypted copy")
	}
	if p, ok := f.placement(testMedium); ok && p.Status == state.PlacementGone {
		t.Errorf("destination placement is GONE: %+v", p)
	}

	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	report = f.runCycle()
	f.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("the next cycle did not complete the move: %+v (move error %q)", report, f.moveTo(mediumB).Error)
	}
	m, err := recovery.ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range m.Placements {
		found = found || (p.Medium == testMedium && p.Encryption != nil)
	}
	if !found {
		t.Errorf("the sidecar still does not describe the encrypted copy: %+v", m.Placements)
	}
}

// --- the feed ------------------------------------------------------------

func stageEvents(t *testing.T, buf *bytes.Buffer) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not a JSON log line: %q", line)
		}
		ev, _ := rec["event"].(string)
		out[ev] = append(out[ev], rec)
	}
	return out
}

// One event per copy per stage, never per chunk: over a six-chunk artifact
// each of the four stages is logged exactly once for the whole move (the
// pre-delete re-read is not a stage of its own), and a chunk-level event
// would make this count at least six.
func TestAnEncryptedCopyEmitsOneEventPerStageNotPerChunk_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{content: multiChunk()})
	var buf bytes.Buffer
	f.engine.Logger = obs.New(&buf, obs.LevelDebug)

	if r := f.runCycle(); r.Completed != 1 {
		t.Fatalf("the move did not complete: %+v", r)
	}
	events := stageEvents(t, &buf)
	for _, name := range []string{obs.EventCopyEncrypting, obs.EventCopyUploadingEncrypted, obs.EventCopyVerifyingStored, obs.EventCopyDecryptVerifying} {
		rows := events[name]
		if len(rows) != 1 {
			t.Errorf("%s was logged %d times for one copy, want exactly 1", name, len(rows))
			continue
		}
		if rows[0]["result"] != string(obs.ResultSuccess) || rows[0]["medium"] != testMedium {
			t.Errorf("%s: %#v", name, rows[0])
		}
	}
	if rows := events[obs.EventCopyDecryptVerifying]; len(rows) == 1 && rows[0]["key_id"] != f.ck.ID {
		t.Errorf("the decrypt-verify event names key %v, want %s", rows[0]["key_id"], f.ck.ID)
	}
	if blob := buf.String(); strings.Contains(blob, "AGE-SECRET-KEY") {
		t.Error("key material reached the log stream")
	}
}

// A stage that fails says so with a result, and the stages after it are not
// claimed: an upload that fails never reaches stored-bytes verification.
func TestAFailedUploadIsAnErrorRowAndNoLaterStageIsClaimed_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	var buf bytes.Buffer
	f.engine.Logger = obs.New(&buf, obs.LevelDebug)
	f.medium.uploadErr = errors.New("endpoint down")

	f.runCycle()
	events := stageEvents(t, &buf)
	up := events[obs.EventCopyUploadingEncrypted]
	if len(up) == 0 || up[0]["result"] != string(obs.ResultError) {
		t.Fatalf("a failed upload has no error row: %#v", up)
	}
	if len(events[obs.EventCopyVerifyingStored]) != 0 || len(events[obs.EventCopyDecryptVerifying]) != 0 {
		t.Error("a stage after the failed upload was logged as if it ran")
	}
}

// #1303: a context that carries a read-progress hook hears the plaintext bytes
// of a content decrypt-verify, ending at the plaintext size, and a fresh
// context (the unattended pass) pays for no hook at all.
func TestAContentDecryptVerifyReportsPlaintextBytesThroughTheContextHook_1303(t *testing.T) {
	content := multiChunk()
	f := newEncryptedFixture(t, fixtureOpts{content: content})
	p, enc := encryptedCopy(t, f)

	var total int64
	ctx := placement.WithReadProgress(f.ctx, func(delta int64) {
		if delta <= 0 {
			t.Errorf("reading of %d", delta)
		}
		total += delta
	})
	res, err := placement.VerifyCopy(ctx, f.medium, f.keys, f.medium0(t), p, &enc, placement.Content, testNow2)
	if err != nil || !res.Passed {
		t.Fatalf("verify = %+v, %v", res, err)
	}
	if total != int64(len(content)) {
		t.Errorf("hook heard %d bytes, want the plaintext size %d", total, len(content))
	}

	// Existence reads no object, so the hook hears nothing.
	total = 0
	if _, err := placement.VerifyCopy(ctx, f.medium, f.keys, f.medium0(t), p, &enc, placement.Existence, testNow2); err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Errorf("an existence check reported %d bytes read", total)
	}
}

// A rotation that lands after the destination was verified and before the
// source is deleted must not cost the verified copy: the re-read before the
// delete opens it through the key recorded for that copy, not the active one.
// The medium reports no mod time so the second read really happens (the
// proof cache would otherwise skip it and the test would pass for no reason).
// Red if the re-check goes through the active key. Written by reading; not run.
func TestARotationBetweenVerifyAndDeleteKeepsTheVerifiedCopy_992(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	f.medium.noModTime = true
	var opens int
	var rotated string
	f.medium.afterOpen = func(*fakeMedium) {
		opens++
		if opens == 1 {
			k, err := f.keys.Rotate(f.ctx, testNow2)
			if err != nil {
				t.Errorf("Rotate: %v", err)
				return
			}
			rotated = k.ID
		}
	}

	report := f.runCycle()
	f.guard.fail()
	if rotated == "" || rotated == f.ck.ID {
		t.Fatal("control: no rotation happened between the verify and the delete")
	}
	if opens < 2 {
		t.Fatalf("control: the object was opened %d times, so the pre-delete re-read never ran", opens)
	}
	if report.Completed != 1 {
		t.Fatalf("a rotation after verify threw the verified copy away: %+v (move error %q)", report, f.onlyMove().Error)
	}
	if enc, ok := f.encryption(); !ok || enc.KeyID != f.ck.ID {
		t.Errorf("record = %+v (ok=%v), want the key the copy was written with, %s", enc, ok, f.ck.ID)
	}
}
