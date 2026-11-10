package movecrash_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/placement"
)

// EPIC N's N2.3 (#996) cells that start from what a REAL encrypted move left
// behind (a harness process, the real engine, the real journal), not from a
// sealed fixture: rotation after the move, retirement of the key the copy
// still needs, and recovery on a machine that has the bundle and the object
// and nothing else.
//
// They were written by reading the world helpers in this package and have not
// been run. Each names the defect that turns it red.

// multiChunkArtifact is three full age chunks and a short final one, with no
// zero byte: a plaintext as long as its ciphertext would pass a length check,
// an all-zero one is a fixed point for a broken keystream, and a
// single-chunk one cannot falsify chunk-index binding.
func multiChunkArtifact() []byte {
	b := make([]byte, 3*65536+4097)
	for i := range b {
		b[i] = byte((i*131+i/251)%251 + 1)
	}
	return b
}

// encryptedWorld is a world whose artifact has been moved for real onto the
// medium under the managed key, with the plaintext local copy gone (a move
// deletes its source once the destination is decrypt-verified).
func encryptedWorld(t *testing.T) *world {
	t.Helper()
	w := newWorldOver(t, multiChunkArtifact())
	w.enableEncryption()
	w.firstHop()
	return w
}

// Rotation leaves the copy a real move wrote recoverable through the key id
// recorded against it, a new copy would use the new key, and retiring the old
// key is refused naming the copy, with the key still in service afterwards.
// Red if verification goes through the active key (the old copy stops
// decrypting after the rotation), if the record is re-pointed at the new key,
// or if the retirement succeeds or names no copy.
func TestRotationAfterARealEncryptedMoveKeepsTheCopyRecoverableAndRetirementIsRefused_996(t *testing.T) {
	w := encryptedWorld(t)
	j := w.journalNow()
	keys := w.keysOver(j)
	obj := w.objectAtKey()

	old, ok, err := keys.Active(w.ctx)
	if err != nil || !ok {
		t.Fatalf("no active key before the rotation (ok=%v err=%v)", ok, err)
	}
	fresh, err := keys.Rotate(w.ctx, crashNow.Add(1))
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if fresh.ID == old.ID {
		t.Fatal("control: the rotation minted no different key, so this test cannot tell the two apart")
	}

	rec, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashMedium)
	if err != nil || !ok {
		t.Fatalf("no encryption record on the copy (ok=%v err=%v)", ok, err)
	}
	if rec.KeyID != old.ID {
		t.Fatalf("the record names key %s after a rotation, want the key it was written under, %s", rec.KeyID, old.ID)
	}

	for _, id := range []string{rec.KeyID} {
		if err := keys.WithKey(w.ctx, id, func(k []byte) error {
			n, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), k, w.hash)
			if err == nil && n != int64(len(w.content)) {
				t.Errorf("decrypt-verify counted %d plaintext bytes, want %d", n, len(w.content))
			}
			return err
		}); err != nil {
			t.Fatalf("the pre-rotation copy does not decrypt through its recorded key: %v", err)
		}
	}
	// The new key is not the one that opens it: the control that the check
	// above is about the recorded key and not about any key.
	if err := keys.WithKey(w.ctx, fresh.ID, func(k []byte) error {
		_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), k, w.hash)
		return err
	}); !errors.Is(err, copycrypt.ErrWrongKey) {
		t.Fatalf("the new key opening the old copy: err = %v, want ErrWrongKey", err)
	}

	err = placement.RetireKey(w.ctx, j, obs.New(io.Discard, obs.LevelDebug), old.ID, crashNow.Add(2))
	if !errors.Is(err, placement.ErrKeyInUse) {
		t.Fatalf("retiring a key a live copy records: err = %v, want ErrKeyInUse", err)
	}
	var inUse *placement.KeyInUseError
	if !errors.As(err, &inUse) || len(inUse.Copies) != 1 || inUse.Copies[0].Artifact != w.artifact || inUse.Copies[0].Medium != crashMedium {
		t.Fatalf("the refusal does not name the one copy that needs the key: %+v", inUse)
	}
	// Refused means unchanged: the key still opens the copy.
	if err := keys.WithKey(w.ctx, old.ID, func(k []byte) error {
		_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), k, w.hash)
		return err
	}); err != nil {
		t.Fatalf("after a refused retirement the key no longer opens the copy: %v", err)
	}
}

// The recovery bundle opens the object a real encrypted move left on the
// medium, on a machine that has the bundle and the object and nothing else:
// no state database, no configuration, no key directory. It does it three
// ways the documentation names (the shipped reader, the age library as a
// stand-in for any other implementation of the format, and the age command
// line tool where this host has one), writes to a temporary name, and renames
// it only once the plaintext digest matches the one the sidecar would record.
// Red if the bundle needs anything from the deployment, if the object is not
// plain age, or if the recovered bytes are not the artifact's.
func TestTheRecoveryBundleOpensWhatARealEncryptedMoveLeftOnTheMediumWithNoStateAtAll_996(t *testing.T) {
	w := encryptedWorld(t)
	j := w.journalNow()
	keys := w.keysOver(j)
	obj := w.objectAtKey()

	rec, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashMedium)
	if err != nil || !ok {
		t.Fatalf("no encryption record on the copy (ok=%v err=%v)", ok, err)
	}
	var bundle bytes.Buffer
	if _, err := keys.Export(w.ctx, rec.KeyID, &bundle, crashNow.Add(1)); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// The clean machine: one directory, holding the bundle and the object.
	clean := t.TempDir()
	bundlePath := filepath.Join(clean, "bundle.json")
	objectPath := filepath.Join(clean, "copy.age")
	if err := os.WriteFile(bundlePath, bundle.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath, obj, 0o600); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(clean); len(entries) != 2 {
		t.Fatalf("the clean machine holds %d entries, want the bundle and the object only", len(entries))
	}

	read := func() keycustody.Bundle {
		f, err := os.Open(bundlePath)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		b, err := keycustody.ReadBundle(f)
		if err != nil {
			t.Fatalf("ReadBundle: %v", err)
		}
		if b.KeyID != rec.KeyID {
			t.Fatalf("the bundle holds key %s, the copy's record names %s", b.KeyID, rec.KeyID)
		}
		return b
	}
	digestOf := func(b []byte) string {
		s := sha256.Sum256(b)
		return hex.EncodeToString(s[:])
	}
	// finish is the documented last step: a temporary name, a digest check,
	// then the rename. A mismatch leaves nothing under the artifact's name.
	finish := func(t *testing.T, tmp, final string) {
		t.Helper()
		got, err := os.ReadFile(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if digestOf(got) != w.hash {
			t.Fatalf("the recovered bytes hash to %s, the artifact is %s", digestOf(got), w.hash)
		}
		if err := os.Rename(tmp, final); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, w.content) {
			t.Fatal("the digest matched and the bytes differ, which a sha256 cannot do")
		}
	}

	t.Run("the shipped reader", func(t *testing.T) {
		tmp, final := filepath.Join(clean, ".recovering"), filepath.Join(clean, "shipped.out")
		out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := read().Decrypt(keycustody.AgeFormat{}, bytes.NewReader(obj), out); err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
		finish(t, tmp, final)
	})

	t.Run("the age library, as any other implementation of the format", func(t *testing.T) {
		id, err := age.ParseX25519Identity(read().Material)
		if err != nil {
			t.Fatalf("the bundle's material is not an age identity: %v", err)
		}
		r, err := age.Decrypt(bytes.NewReader(obj), id)
		if err != nil {
			t.Fatalf("age.Decrypt: %v", err)
		}
		tmp := filepath.Join(clean, ".recovering-age")
		out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(out, r); err != nil {
			t.Fatalf("reading the plaintext: %v", err)
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
		finish(t, tmp, filepath.Join(clean, "age-lib.out"))
	})

	t.Run("the stock age command line tool", func(t *testing.T) {
		// The age on this host if there is one, otherwise the same program
		// built from the module the repository already depends on
		// (filippo.io/age/cmd/age, v1.x, from the module cache), so the claim
		// "the stock tool opens a bundle's copy" is exercised on every host
		// and a missing binary is a build failure here, not a skip.
		bin, err := exec.LookPath("age")
		if err != nil {
			bin = filepath.Join(t.TempDir(), "age")
			build := exec.Command("go", "build", "-o", bin, "filippo.io/age/cmd/age")
			build.Env = append(os.Environ(), "GOWORK=off")
			if out, berr := build.CombinedOutput(); berr != nil {
				t.Fatalf("building the stock age tool from the module cache: %v\n%s", berr, out)
			}
		}
		identity := filepath.Join(clean, "key.txt")
		if err := os.WriteFile(identity, []byte(read().Material+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		tmp := filepath.Join(clean, ".recovering-cli")
		if out, err := exec.Command(bin, "-d", "-i", identity, "-o", tmp, objectPath).CombinedOutput(); err != nil {
			t.Fatalf("age -d: %v\n%s", err, out)
		}
		finish(t, tmp, filepath.Join(clean, "age-cli.out"))
	})

	// The negatives, on the same clean machine: a bundle for a different key
	// recovers nothing, and nothing is left under a recovered name.
	t.Run("a bundle for another key recovers nothing", func(t *testing.T) {
		other := w.keysOver(j)
		if _, err := other.Rotate(w.ctx, crashNow.Add(3)); err != nil {
			t.Fatalf("Rotate: %v", err)
		}
		active, ok, err := other.Active(context.Background())
		if err != nil || !ok || active.ID == rec.KeyID {
			t.Fatalf("no second key to export (ok=%v err=%v)", ok, err)
		}
		var wrong bytes.Buffer
		if _, err := other.Export(w.ctx, active.ID, &wrong, crashNow.Add(4)); err != nil {
			t.Fatalf("Export: %v", err)
		}
		b, err := keycustody.ReadBundle(&wrong)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := b.Decrypt(keycustody.AgeFormat{}, bytes.NewReader(obj), &out); err == nil {
			t.Fatal("a bundle for a different key opened the copy")
		}
		if out.Len() != 0 {
			t.Fatalf("a failed recovery wrote %d plaintext bytes", out.Len())
		}
	})
}
