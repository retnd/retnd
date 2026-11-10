// Package encryptedretention holds the system-level tests of EPIC N's
// retained-copy encryption that need no engine process: the managed key
// custody, the copy transform and the recovery bundle wired together the way
// the product wires them, over files on a real disk (#996).
//
// What is NOT here, because a test elsewhere already holds it over the real
// engine: the kill cells (core/tests/movecrash, E1 to E5), rotation and
// retirement over a real encrypted move and recovery from a bundle over what
// that move left (core/tests/movecrash/encrypted_lifecycle_996_test.go) and the
// route and verb equivalence (core/cmd/retnd/encryption_equivalence_996_test.go).
//
// Every test here was written by reading the packages it drives and has not
// been run. Each names the defect that turns it red.
package encryptedretention_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/secretref"
	"github.com/retnd/retnd/core/internal/state"
)

// chunk, chunkSealed and nonceLen are age's, spelled here because the library
// owns them and a test has to know where the boundaries are.
const (
	chunk       = 64 << 10
	chunkSealed = chunk + 16
	nonceLen    = 16
)

var mintedAt = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fixture is seeded so a failure reproduces and contains no zero byte. Its
// length is three full chunks and a partial one: a plaintext whose length
// equals its ciphertext's would pass a length-only check, and three chunks is
// the least a reorder can be falsified over.
func fixture(seed uint64) []byte {
	b := make([]byte, 3*chunk+4097)
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for i := range b {
		b[i] = byte(r.UintN(255)) + 1
	}
	return b
}

// headerEnd is the offset of the first payload nonce: the age header ends with
// the line that starts "--- ".
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

// rig is one deployment's disk: a managed key store over a real journal, a
// local directory holding the plaintext artifact, a staging directory, a
// directory standing for the off-site bucket and a place to recover into.
// The plaintext lives in srcDir, outside root, so a scan of root for
// plaintext is a scan of everything that must not hold any.
type rig struct {
	t       *testing.T
	ctx     context.Context
	root    string
	srcDir  string
	staging string
	bucket  string
	journal *state.Journal
	store   *keycustody.Store
	key     keycustody.KeyRecord
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
	key, err := store.Create(ctx, mintedAt)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	r := &rig{
		t: t, ctx: ctx, root: root, srcDir: t.TempDir(),
		staging: filepath.Join(root, "staging"), bucket: filepath.Join(root, "bucket"),
		journal: j, store: store, key: key,
	}
	if err := os.MkdirAll(r.bucket, 0o700); err != nil {
		t.Fatal(err)
	}
	return r
}

// encryptOffsite is the engine's hop as the transform sees it: read the local
// artifact, encrypt it into staging under rec's recipient, upload the staged
// bytes to the bucket and sweep staging. It returns the object as the bucket
// holds it.
func (r *rig) encryptOffsite(name string, plain []byte, rec keycustody.KeyRecord) ([]byte, copycrypt.Staged) {
	r.t.Helper()
	src := filepath.Join(r.srcDir, name)
	if err := os.WriteFile(src, plain, 0o600); err != nil {
		r.t.Fatal(err)
	}
	f, err := os.Open(src)
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()
	s, err := copycrypt.EncryptToStaging(r.ctx, r.staging, name, f, rec.Recipient, sum(plain))
	if err != nil {
		r.t.Fatalf("EncryptToStaging: %v", err)
	}
	obj, err := os.ReadFile(s.Path)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.bucket, name), obj, 0o600); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Remove(s.Path); err != nil {
		r.t.Fatal(err)
	}
	return obj, s
}

// recover is the restore path: the key is resolved through custody by the id
// recorded against the copy, and the object is written to dir/name only after
// its last chunk authenticated and its digest matched.
func (r *rig) recoverTo(dir, name string, id string, obj []byte, want string) (string, error) {
	r.t.Helper()
	var path string
	err := r.store.WithKey(r.ctx, id, func(k []byte) error {
		var derr error
		path, derr = copycrypt.DecryptToFile(r.ctx, bytes.NewReader(obj), k, want, dir, name)
		return derr
	})
	return path, err
}

// plaintextUnder lists every file under dir that holds a stretch of the
// plaintext, sampled at the start, across a chunk boundary, in the middle and
// at the end, so a partial write of any part is seen.
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

// One artifact ends with a plaintext local copy and an encrypted off-site
// copy; the two digests are distinct, the copies are different sizes, the
// off-site object is age and carries no stretch of the plaintext, both verify,
// and the verification can fail. Red if the transform writes the plaintext to
// the bucket, records the plaintext digest as the stored one, or if
// decrypt-verify accepts a digest that is not the artifact's.
//
// What this does not do: catalogue both copies. A move deletes its source, so
// no engine path leaves the local placement ACTIVE beside an ACTIVE encrypted
// medium placement (the movecrash cells assert exactly that), and this test
// states the claim at the byte and digest level only. The journal half of
// that acceptance line needs a seam the engine does not have.
func TestOneArtifactHasAPlaintextLocalCopyBesideAnEncryptedOffsiteCopyWithDistinctDigests_996(t *testing.T) {
	r := newRig(t)
	plain := fixture(1)
	obj, s := r.encryptOffsite("artifact.dump", plain, r.key)

	local, err := os.ReadFile(filepath.Join(r.srcDir, "artifact.dump"))
	if err != nil {
		t.Fatal(err)
	}
	if sum(local) != sum(plain) {
		t.Fatal("the encrypting pass changed the plaintext local copy")
	}
	if s.StoredHash != sum(obj) || s.StoredSize != int64(len(obj)) {
		t.Fatalf("the staged digest/size %s/%d do not describe the object in the bucket (%s/%d)", s.StoredHash, s.StoredSize, sum(obj), len(obj))
	}
	if s.StoredHash == sum(plain) || s.PlaintextHash != sum(plain) {
		t.Fatalf("the two digests are not kept apart: stored %s, plaintext %s, artifact %s", s.StoredHash, s.PlaintextHash, sum(plain))
	}
	if len(obj) <= len(plain) {
		t.Fatalf("the object is %d bytes for a %d byte artifact: ciphertext with an authenticated header cannot be that small", len(obj), len(plain))
	}
	if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) {
		t.Fatal("the off-site object is not an age v1 file")
	}
	if found := plaintextUnder(t, r.bucket, plain); len(found) != 0 {
		t.Fatalf("plaintext found in the bucket: %v", found)
	}

	// Both verify. The off-site one through the key custody holds.
	if err := r.store.WithKey(r.ctx, r.key.ID, func(k []byte) error {
		n, err := copycrypt.DecryptVerify(r.ctx, bytes.NewReader(obj), k, sum(plain))
		if err == nil && n != int64(len(plain)) {
			t.Errorf("decrypt-verify counted %d plaintext bytes, want %d", n, len(plain))
		}
		return err
	}); err != nil {
		t.Fatalf("the off-site copy does not decrypt-verify to the artifact: %v", err)
	}

	// The control: the same verification refuses a digest that is not the
	// artifact's, so a pass above is a finding.
	if err := r.store.WithKey(r.ctx, r.key.ID, func(k []byte) error {
		_, err := copycrypt.DecryptVerify(r.ctx, bytes.NewReader(obj), k, sum(fixture(2)))
		return err
	}); !errors.Is(err, copycrypt.ErrDigestMismatch) {
		t.Fatalf("decrypt-verify against another artifact's digest: err = %v, want ErrDigestMismatch", err)
	}
}

// Every crypto negative, through the custody the product resolves keys with
// and over the object as the medium holds it, fails closed with no stretch of
// plaintext anywhere under the deployment's disk, and the untouched object is
// the control: it recovers, and the scan finds the plaintext it wrote, so an
// empty scan on the others is not an empty scanner. Red if any case recovers,
// leaves a partial file, or returns a different error class than the one named
// (a wrong key must not read as corruption, nor corruption as a wrong key).
func TestEveryCryptoNegativeFailsClosedThroughCustodyWithNoPartialPlaintextOnDisk_996(t *testing.T) {
	plain := fixture(7)
	base := newRig(t)
	good, _ := base.encryptOffsite("artifact.dump", plain, base.key)
	hdr := headerEnd(t, good)
	payload := hdr + nonceLen

	// A copy of the same plaintext sealed under another key, for the swapped
	// header, and that other key's own custody for the wrong-key case.
	foreign := newRig(t)
	foreignObj, _ := foreign.encryptOffsite("artifact.dump", plain, foreign.key)
	foreignHdr := headerEnd(t, foreignObj)

	swapped := bytes.Clone(good)
	a, c := swapped[payload:payload+chunkSealed], swapped[payload+chunkSealed:payload+2*chunkSealed]
	tmp := bytes.Clone(a)
	copy(a, c)
	copy(c, tmp)

	flip := func(at int) []byte { b := bytes.Clone(good); b[at] ^= 0x01; return b }

	cases := []struct {
		name string
		obj  []byte
		// rig whose custody resolves the key, and which key id it asks for.
		by   *rig
		want error
	}{
		{"wrong key", good, foreign, copycrypt.ErrWrongKey},
		{"bit flip in the second chunk", flip(payload + chunkSealed + 1000), base, copycrypt.ErrCorrupt},
		{"bit flip in the final tag", flip(len(good) - 1), base, copycrypt.ErrCorrupt},
		{"bit flip in the header MAC line", flip(hdr - 3), base, copycrypt.ErrCorrupt},
		{"truncated by one byte", good[:len(good)-1], base, copycrypt.ErrCorrupt},
		{"truncated mid chunk", good[:payload+chunkSealed+5], base, copycrypt.ErrCorrupt},
		{"truncated exactly on a chunk boundary", good[:payload+3*chunkSealed], base, copycrypt.ErrCorrupt},
		{"truncated inside the header", good[:hdr-10], base, copycrypt.ErrCorrupt},
		{"trailing bytes after the final chunk", append(bytes.Clone(good), 0x00), base, copycrypt.ErrCorrupt},
		{"two of three or more chunks reordered", swapped, base, copycrypt.ErrCorrupt},
		{"header swapped for another encryption's", append(bytes.Clone(foreignObj[:foreignHdr]), good[hdr:]...), base, copycrypt.ErrCorrupt},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := filepath.Join(base.root, "recovered")
			// The key id asked for is the one recorded against the copy
			// (base's), resolved through whichever custody this case names:
			// for the wrong-key case that is the other deployment's key
			// opening this deployment's object.
			id := c.by.key.ID
			path, err := c.by.recoverTo(out, "artifact.dump", id, c.obj, sum(plain))
			if !errors.Is(err, c.want) {
				t.Fatalf("recovery err = %v, want %v", err, c.want)
			}
			if path != "" {
				t.Fatalf("a failed recovery returned the path %q", path)
			}
			if found := plaintextUnder(t, base.root, plain); len(found) != 0 {
				t.Fatalf("a failed recovery left plaintext on disk: %v", found)
			}
			if found := plaintextUnder(t, c.by.root, plain); len(found) != 0 {
				t.Fatalf("a failed recovery left plaintext on the other deployment's disk: %v", found)
			}
			if entries, _ := os.ReadDir(out); len(entries) != 0 {
				t.Fatalf("a failed recovery left %d entries in the recovery directory", len(entries))
			}
		})
	}

	t.Run("control: the untouched object recovers and the scan can see what it wrote", func(t *testing.T) {
		out := filepath.Join(base.root, "recovered-control")
		path, err := base.recoverTo(out, "artifact.dump", base.key.ID, good, sum(plain))
		if err != nil {
			t.Fatalf("the untouched object does not recover: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("the recovered file is not the artifact (err=%v)", err)
		}
		if found := plaintextUnder(t, base.root, plain); len(found) != 1 || found[0] != path {
			t.Fatalf("the plaintext scan found %v after a successful recovery, want exactly %s: a scan that cannot find plaintext proves nothing above", found, path)
		}
	})
}

// A key that cannot be resolved fails closed before any byte of the copy is
// read: an id the registry never minted, a key file that is gone, a key file
// swapped for another key's, and a key file the custody rule refuses. None
// of them reaches the transform, each is its own error and none is a
// corruption or a wrong key, and none leaves plaintext. The resolvable
// state first is the control. Red if any case falls through to a decrypt or is
// reported as the object being damaged.
func TestAKeyThatCannotBeResolvedFailsClosedBeforeTheCopyIsRead_996(t *testing.T) {
	plain := fixture(11)

	setup := func(t *testing.T) (*rig, []byte, string) {
		t.Helper()
		r := newRig(t)
		obj, _ := r.encryptOffsite("artifact.dump", plain, r.key)
		keyFile := filepath.Join(r.root, keycustody.DirName, r.key.ID+".key")
		if err := r.store.WithKey(r.ctx, r.key.ID, func([]byte) error { return nil }); err != nil {
			t.Fatalf("control: the key does not resolve before it is damaged: %v", err)
		}
		return r, obj, keyFile
	}
	attempt := func(r *rig, id string, obj []byte) (called bool, err error) {
		err = r.store.WithKey(r.ctx, id, func([]byte) error { called = true; return nil })
		if found := plaintextUnder(t, r.root, plain); len(found) != 0 {
			t.Errorf("plaintext on disk after a key that did not resolve: %v", found)
		}
		return called, err
	}

	t.Run("an id the registry never minted", func(t *testing.T) {
		r, obj, _ := setup(t)
		called, err := attempt(r, "k0000000000000000", obj)
		if called || !errors.Is(err, keycustody.ErrUnknownKey) {
			t.Fatalf("called=%v err=%v, want the callback not run and ErrUnknownKey", called, err)
		}
	})
	t.Run("the key file is gone", func(t *testing.T) {
		r, obj, keyFile := setup(t)
		if err := os.Remove(keyFile); err != nil {
			t.Fatal(err)
		}
		called, err := attempt(r, r.key.ID, obj)
		if called || err == nil {
			t.Fatalf("called=%v err=%v, want the callback not run and an error", called, err)
		}
		if errors.Is(err, copycrypt.ErrCorrupt) || errors.Is(err, copycrypt.ErrWrongKey) {
			t.Fatalf("a missing key is reported as %v: nothing was tried, so it is neither", err)
		}
	})
	t.Run("the key file is another key's", func(t *testing.T) {
		r, obj, keyFile := setup(t)
		other := newRig(t)
		var material []byte
		if err := other.store.WithKey(other.ctx, other.key.ID, func(k []byte) error { material = bytes.Clone(k); return nil }); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyFile, append(material, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		called, err := attempt(r, r.key.ID, obj)
		if called || !errors.Is(err, keycustody.ErrKeyMismatch) {
			t.Fatalf("called=%v err=%v, want the callback not run and ErrKeyMismatch", called, err)
		}
	})
	t.Run("the key file is readable by others", func(t *testing.T) {
		r, obj, keyFile := setup(t)
		if err := os.Chmod(keyFile, 0o644); err != nil {
			t.Fatal(err)
		}
		called, err := attempt(r, r.key.ID, obj)
		if called || !errors.Is(err, secretref.ErrCustody) {
			t.Fatalf("called=%v err=%v, want the callback not run and secretref.ErrCustody", called, err)
		}
	})
}

// Rotation leaves every earlier copy recoverable and new copies on the new
// key, at the byte level: two copies under two keys both recover through the
// id recorded against each, and neither key opens the other's copy.
// (Retirement of a key a live copy needs is refused naming the copy in
// core/tests/movecrash, over a copy a real move wrote.) Red if recovery goes
// through the active key, or a rotation re-keys an existing copy.
func TestRotationLeavesEveryPreRotationCopyRecoverableAndNewCopiesOnTheNewKey_996(t *testing.T) {
	r := newRig(t)
	plain := fixture(21)
	first, _ := r.encryptOffsite("first.dump", plain, r.key)

	fresh, err := r.store.Rotate(r.ctx, mintedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	active, ok, err := r.store.Active(r.ctx)
	if err != nil || !ok || active.ID != fresh.ID || fresh.ID == r.key.ID {
		t.Fatalf("after a rotation the active key is %+v (ok=%v err=%v), want the new one %s", active, ok, err, fresh.ID)
	}
	second, _ := r.encryptOffsite("second.dump", plain, active)

	for _, c := range []struct {
		name string
		obj  []byte
		id   string
		want error
	}{
		{"the pre-rotation copy through its recorded key", first, r.key.ID, nil},
		{"the post-rotation copy through its recorded key", second, fresh.ID, nil},
		{"the pre-rotation copy through the active key", first, fresh.ID, copycrypt.ErrWrongKey},
		{"the post-rotation copy through the superseded key", second, r.key.ID, copycrypt.ErrWrongKey},
	} {
		t.Run(c.name, func(t *testing.T) {
			path, err := r.recoverTo(filepath.Join(r.root, "out-"+c.id), "x.dump", c.id, c.obj, sum(plain))
			if !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if c.want == nil {
				if got, rerr := os.ReadFile(path); rerr != nil || !bytes.Equal(got, plain) {
					t.Fatalf("the recovered file is not the artifact (err=%v)", rerr)
				}
			}
		})
	}
}
