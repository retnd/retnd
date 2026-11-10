// Package encryptionsuites holds EPIC T's system-level tests across the two
// shipped encryption suites (#1319, T2.4): the crypto negatives per suite and
// across suites, rotation from one suite to the other, the lifecycle
// statuses, clean-machine recovery per suite with the stock age tool, the
// canary corpus over the suite surfaces, and the feature-off regression.
//
// What is NOT here, because a test elsewhere holds it over the real engine:
// the mismatch and retired-suite refusals before any destructive step and the
// kill during a re-encryption stream (core/tests/movecrash/suite_cells_1319_test.go),
// catalog rebuild over two suites and an unknown one
// (core/internal/app/catalog_suites_1319_test.go), and the route and verb
// equivalence (core/cmd/retnd/suite_equivalence_1319_test.go and
// ui/shared/e2e/1319-suite-equivalence.spec.ts).
//
// Written by reading the packages and the issues; not run. Each test names
// the defect that turns it red.
package encryptionsuites_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/state"
)

// The two suite ids this epic ships. They are spelled here, not read from the
// registry, because a test that takes its expected ids from the code under
// test cannot see the code renaming one.
const (
	ageV1   = "age/v1"
	agePQV1 = "age-pq/v1"
)

// shipped is every suite a build of this epic writes, in a fixed order.
var shipped = []string{ageV1, agePQV1}

// chunk, chunkSealed and nonceLen are age's (the STREAM payload is the same
// for both suites; only the recipient stanza differs).
const (
	chunk       = 64 << 10
	chunkSealed = chunk + 16
	nonceLen    = 16
)

var mintedAt = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fixture crosses three chunk boundaries, ends in a short final chunk and has
// no zero byte, seeded so a failure reproduces.
func fixture(seed uint64) []byte {
	b := make([]byte, 3*chunk+4097)
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for i := range b {
		b[i] = byte(r.UintN(255)) + 1
	}
	return b
}

// headerEnd is the offset of the first payload nonce: the age header ends
// with the line that starts "--- ".
func headerEnd(t *testing.T, ct []byte) int {
	t.Helper()
	i := bytes.Index(ct, []byte("\n--- "))
	if i < 0 {
		t.Fatalf("no MAC line in what should be an age header: %q", ct[:min(len(ct), 80)])
	}
	nl := bytes.IndexByte(ct[i+1:], '\n')
	if nl < 0 {
		t.Fatal("unterminated MAC line")
	}
	return i + 1 + nl + 1
}

// --- the seam: every registry and suite-aware custody call ---------------

// suiteOf looks a shipped suite up in the build's registry, failing the test
// when the build does not know it.
func suiteOf(t *testing.T, id string) copycrypt.Suite {
	t.Helper()
	s, ok := copycrypt.Lookup(id)
	if !ok {
		t.Fatalf("copycrypt.Lookup(%q): this build does not register the suite", id)
	}
	return s
}

// mintUnder creates the deployment's first managed key, under suite id. There
// is one active managed key deployment-wide (migration 0015's index, kept by
// T1.2), so this is only ever the first key.
func mintUnder(t *testing.T, ctx context.Context, store *keycustody.Store, id string, at time.Time) keycustody.KeyRecord {
	t.Helper()
	rec, err := store.CreateIn(ctx, at, id)
	if err != nil {
		t.Fatalf("minting a key under %s: %v", id, err)
	}
	if rec.SuiteID != id {
		t.Fatalf("a key minted under %s records suite %q", id, rec.SuiteID)
	}
	return rec
}

// rotateTo makes a new managed key under suite id the active one,
// superseding the active key, whatever its suite, in the same write.
func rotateTo(t *testing.T, ctx context.Context, store *keycustody.Store, id string, at time.Time) keycustody.KeyRecord {
	t.Helper()
	rec, err := store.RotateTo(ctx, at, id)
	if err != nil {
		t.Fatalf("rotating to %s: %v", id, err)
	}
	if rec.SuiteID != id {
		t.Fatalf("a key rotated into %s records suite %q", id, rec.SuiteID)
	}
	return rec
}

// activeUnder is the active managed key under suite id, if there is one.
func activeUnder(t *testing.T, ctx context.Context, store *keycustody.Store, id string) (keycustody.KeyRecord, bool) {
	t.Helper()
	rec, ok, err := store.ActiveFor(ctx, id)
	if err != nil {
		t.Fatalf("ActiveFor(%s): %v", id, err)
	}
	return rec, ok
}

// sealUnder encrypts plain into dir under suite and recipient and returns the
// object exactly as a medium would hold it, with the staging file swept.
func sealUnder(t *testing.T, ctx context.Context, suite copycrypt.Suite, recipient, dir, name string, plain []byte) ([]byte, copycrypt.Staged) {
	t.Helper()
	s, err := copycrypt.EncryptToStaging(ctx, dir, name, bytes.NewReader(plain), suite, recipient, sum(plain))
	if err != nil {
		t.Fatalf("EncryptToStaging under %s: %v", suite.ID(), err)
	}
	obj, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.Path); err != nil {
		t.Fatal(err)
	}
	return obj, s
}

// --- one deployment's disk ------------------------------------------------

// rig is one deployment holding a managed key under each shipped suite (the
// age/v1 key minted first and superseded by a rotation into age-pq/v1, the
// path an EPIC N deployment takes), over a real journal, a staging directory and a directory standing for the
// off-site bucket. The plaintext is written nowhere under root, so a scan of
// root for plaintext is a scan of everything that must not hold any.
type rig struct {
	t       *testing.T
	ctx     context.Context
	root    string
	staging string
	bucket  string
	journal *state.Journal
	store   *keycustody.Store
	keys    map[string]keycustody.KeyRecord // suite id -> its active key
}

func newRig(t *testing.T) *rig {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	j, err := state.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	store, err := keycustody.Open(filepath.Join(root, keycustody.DirName), keycustody.NewJournalRegistry(j), keycustody.AgeFormat{})
	if err != nil {
		t.Fatalf("keycustody.Open: %v", err)
	}
	r := &rig{
		t: t, ctx: ctx, root: root,
		staging: filepath.Join(root, "staging"), bucket: filepath.Join(root, "bucket"),
		journal: j, store: store, keys: map[string]keycustody.KeyRecord{},
	}
	r.keys[ageV1] = mintUnder(t, ctx, store, ageV1, mintedAt)
	r.keys[agePQV1] = rotateTo(t, ctx, store, agePQV1, mintedAt.Add(time.Minute))
	if err := os.MkdirAll(r.bucket, 0o700); err != nil {
		t.Fatal(err)
	}
	return r
}

// seal is the engine's hop as the transform sees it, under one suite's key.
// Sealing needs only the recipient, so the superseded age/v1 key still seals
// here; the engine itself writes only under the active key.
func (r *rig) seal(id, name string, plain []byte) ([]byte, copycrypt.Staged) {
	r.t.Helper()
	obj, s := sealUnder(r.t, r.ctx, suiteOf(r.t, id), r.keys[id].Recipient, r.staging, name, plain)
	if err := os.WriteFile(filepath.Join(r.bucket, name), obj, 0o600); err != nil {
		r.t.Fatal(err)
	}
	return obj, s
}

// recoverTo is the restore path: the suite comes from the copy's record (here
// the caller names it), the key is resolved through custody by the id
// recorded against the copy, and dir/name exists only once the last chunk
// authenticated and the digest matched.
func (r *rig) recoverTo(dir, name, suiteID, keyID string, obj []byte, want string) (string, error) {
	r.t.Helper()
	var path string
	err := r.store.WithKey(r.ctx, keyID, func(k []byte) error {
		var derr error
		path, derr = copycrypt.DecryptToFile(r.ctx, bytes.NewReader(obj), suiteOf(r.t, suiteID), k, want, dir, name)
		return derr
	})
	return path, err
}

// plaintextUnder lists every file under dir holding a stretch of plain,
// sampled at the start, across each chunk boundary, in the middle and at the
// end, so a partial write of any part is seen.
func plaintextUnder(t *testing.T, dir string, plain []byte) []string {
	t.Helper()
	n := len(plain)
	windows := [][]byte{plain[:64], plain[chunk-32 : chunk+32], plain[n/2 : n/2+64], plain[2*chunk-32 : 2*chunk+32], plain[n-64:]}
	var found []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, w := range windows {
			if bytes.Contains(b, w) {
				found = append(found, path)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return found
}

// The fixtures this package leans on have the properties the issue asks of
// them, for both suites: the plaintext crosses a chunk boundary, ends in a
// short final chunk and is not all zero, and the age-pq/v1 object carries a
// PQ header (the mlkem768x25519 stanza, about 1.4 KB) while the age/v1 one
// carries an X25519 stanza. Red if a suite writes the other's stanza, or if a
// fixture change makes the chunk-level negatives below vacuous.
func TestTheFixturesCrossAChunkBoundaryAndIncludeAPQHeader_1319(t *testing.T) {
	plain := fixture(1)
	if len(plain)%chunk == 0 || len(plain) < 3*chunk {
		t.Fatalf("the fixture is %d bytes: it must cross at least three chunk boundaries and end short", len(plain))
	}
	if bytes.IndexByte(plain, 0) >= 0 {
		t.Fatal("the fixture contains a zero byte")
	}
	r := newRig(t)
	stanza := map[string]string{ageV1: "-> X25519 ", agePQV1: "-> mlkem768x25519 "}
	for _, id := range shipped {
		t.Run(id, func(t *testing.T) {
			obj, s := r.seal(id, "fixture.dump", plain)
			hdr := obj[:headerEnd(t, obj)]
			if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) {
				t.Fatalf("a %s object is not an age v1 file", id)
			}
			for other, st := range stanza {
				has := bytes.Contains(hdr, []byte(st))
				if has != (other == id) {
					t.Fatalf("the %s header carries the %s stanza = %v:\n%s", id, other, has, hdr)
				}
			}
			if id == agePQV1 && len(hdr) < 1024 {
				t.Fatalf("the age-pq/v1 header is %d bytes; an ML-KEM-768 stanza alone is over a kilobyte", len(hdr))
			}
			if s.StoredSize <= s.PlaintextSize {
				t.Fatalf("stored %d <= plaintext %d", s.StoredSize, s.PlaintextSize)
			}
			if s.SuiteID != id {
				t.Fatalf("the staged copy says suite %q, it was sealed under %s", s.SuiteID, id)
			}
		})
	}
}
