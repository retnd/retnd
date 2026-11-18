package placement_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/secretref"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// These are the engine-side tests for #1292: a backup set whose
// retained_copy_encryption.key_provider is an external reference writes
// encrypted copies, through a registry row custody records. They were
// written by reading the engine and the fixtures and have not been run.

// externalKeyAt writes an age identity under custody secretref accepts and
// returns its reference and material.
func externalKeyAt(t *testing.T, name string) (secretref.Ref, []byte) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	material, _, err := keycustody.AgeFormat{}.Generate()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(append([]byte(nil), material...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return secretref.Ref{File: path}, material
}

// provisionExternal wires f so its set requires encryption under ref. The
// store has no managed key at all: the external key is the only one.
func provisionExternal(t *testing.T, f *fixture, ref secretref.Ref) *keycustody.Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	keys, err := keycustody.Open(dir, keycustody.NewJournalRegistry(f.journal), keycustody.AgeFormat{})
	if err != nil {
		t.Fatalf("keycustody.Open: %v", err)
	}
	keys.UseExternalRefs(func() []secretref.Ref { return []secretref.Ref{ref} })
	f.engine.Sets = fixedSets{set: config.BackupSet{
		Name: testSet, ID: f.artifact.Set, LocalPath: f.localDir,
		RetainedCopyEncryption: config.RetainedCopyEncryption{
			Mode:        config.RetainedCopyEncryptionRequired,
			KeyProvider: config.RetainedCopyKeyProvider{External: &config.SecretSource{File: ref.File}},
		},
	}}
	f.engine.Keys = keys
	return keys
}

func newExternalFixture(t *testing.T) (*fixture, *keycustody.Store, secretref.Ref, []byte) {
	t.Helper()
	f := newFixture(t, fixtureOpts{content: multiChunk()})
	ref, material := externalKeyAt(t, "offsite.key")
	return f, provisionExternal(t, f, ref), ref, material
}

func externalRow(t *testing.T, f *fixture, s *keycustody.Store) keycustody.KeyRecord {
	t.Helper()
	keys, err := s.Keys(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []keycustody.KeyRecord
	for _, k := range keys {
		if k.Provider == keycustody.ProviderExternal {
			out = append(out, k)
		}
	}
	if len(out) != 1 {
		t.Fatalf("the registry holds %d external rows, want 1 (all rows: %+v)", len(out), keys)
	}
	return out[0]
}

// Red while activeKey refuses an external provider as ErrClassUnavailable.
func TestAnExternalKeyWritesAnEncryptedCopyRecordedUnderItsRegistryRow_1292(t *testing.T) {
	f, keys, _, material := newExternalFixture(t)

	report := f.runCycle()
	f.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("the move did not complete: %+v (move error %q)", report, f.onlyMove().Error)
	}

	obj := f.medium.bytesAt(f.key)
	if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) {
		t.Fatal("the object on the medium is not an age v1 stream")
	}
	if bytes.Contains(obj, f.content[:64]) {
		t.Fatal("plaintext appears in the stored object")
	}
	row := externalRow(t, f, keys)
	enc, ok, err := f.journal.PlacementEncryption(f.ctx, f.artifact, testMedium)
	if err != nil || !ok {
		t.Fatalf("PlacementEncryption = %+v, %v, %v, want a record", enc, ok, err)
	}
	if enc.KeyID != row.ID {
		t.Fatalf("the per-copy record names key %q, the registry's external row is %q", enc.KeyID, row.ID)
	}
	p, ok := f.placement(testMedium)
	if !ok || p.VerificationClass != state.VerificationContent || p.Hash != f.hash {
		t.Fatalf("placement = %+v, ok %v, want content class and the plaintext digest", p, ok)
	}
	// The recorded key opens the object: this is the stock `age -d -i`
	// recovery, through the operator's own key.
	err = keys.WithKey(f.ctx, row.ID, func(k []byte) error {
		if !bytes.Equal(k, bytes.TrimSpace(material)) {
			t.Error("the registered key does not resolve to the operator's material")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithKey(%s): %v", row.ID, err)
	}
}

// Every way the reference can fail is refused before any byte is encrypted
// or uploaded, records no class and no encryption row, and writes no
// registry row for a key that never resolved.
// Red while an external provider is refused for the wrong reason, or while
// any of these reaches the upload.
func TestAnExternalKeyThatDoesNotResolveIsRefusedBeforeAnyUpload_1292(t *testing.T) {
	cases := map[string]func(t *testing.T, ref secretref.Ref){
		"file removed": func(t *testing.T, ref secretref.Ref) {
			if err := os.Remove(ref.File); err != nil {
				t.Fatal(err)
			}
		},
		"file empty": func(t *testing.T, ref secretref.Ref) {
			if err := os.WriteFile(ref.File, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"not an age identity": func(t *testing.T, ref secretref.Ref) {
			if err := os.WriteFile(ref.File, []byte("hunter2-not-a-key\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"file readable by others": func(t *testing.T, ref secretref.Ref) {
			if err := os.Chmod(ref.File, 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			f, keys, ref, _ := newExternalFixture(t)
			breakIt(t, ref)

			f.runCycle()
			f.guard.fail()

			mv := f.onlyMove()
			if placement.Phase(mv.Phase) == placement.Done {
				t.Fatal("a move completed with an unresolvable external key")
			}
			if n := f.medium.uploadCount(); n != 0 {
				t.Fatalf("%d upload(s) before the key was known to resolve", n)
			}
			if p, ok := f.placement(testMedium); ok {
				t.Fatalf("a destination placement was recorded (class %q)", p.VerificationClass)
			}
			if _, ok, _ := f.journal.PlacementEncryption(f.ctx, f.artifact, testMedium); ok {
				t.Fatal("a placement_encryption record was written")
			}
			if all, _ := keys.Keys(f.ctx); len(all) != 0 {
				t.Fatalf("%d registry row(s) written for a key that never resolved", len(all))
			}
			if !strings.Contains(mv.Error, "unavailable") && !strings.Contains(mv.Error, "resolve") {
				t.Fatalf("the move row's error %q does not say why", mv.Error)
			}
			if strings.Contains(mv.Error, "hunter2") {
				t.Fatal("the move row quotes the secret")
			}
			if left := f.localExists(); !left {
				t.Fatal("the local source is gone")
			}
		})
	}
}

// The reference starts yielding a different key after copies were written:
// verifying the old copy through its recorded id fails closed and the
// callback is never called. (A new copy would register the new key as its
// own row; the registry keeps no reference to compare against.)
func TestAnExternalKeyThatChangedUnderItsReferenceIsNotHandedOutForTheOldRow_1292(t *testing.T) {
	f, keys, ref, _ := newExternalFixture(t)
	f.runCycle()
	f.guard.fail()
	row := externalRow(t, f, keys)

	other, _, err := keycustody.AgeFormat{}.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ref.File, append(other, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	err = keys.WithKey(f.ctx, row.ID, func([]byte) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("WithKey over a swapped external key: err %v, called %v, want a refusal", err, called)
	}
	if !errors.Is(err, keycustody.ErrExternalKeyUnresolvable) {
		t.Fatalf("err = %v, want ErrExternalKeyUnresolvable", err)
	}
}

// A re-home reads through the key id recorded on the source copy. For an
// external key that id resolves through the declared reference.
func TestAnExternalKeyCopyIsRehomedUnderTheSameRecordedKey_1292(t *testing.T) {
	f := newFixture(t, fixtureOpts{content: multiChunk()})
	ref, _ := externalKeyAt(t, "offsite.key")
	keys := provisionExternal(t, f, ref)

	a := &transport.Medium{ID: testMedium, Type: transport.MediumTypeS3, Bucket: "nas-backups", Prefix: "retnd"}
	b := &transport.Medium{ID: mediumB, Type: transport.MediumTypeS3, Bucket: "nas-annual", Prefix: "annual"}
	f.engine.Mediums = twoMediums{a: a, b: b, class: placement.Content}
	keyB, err := transport.MediumKey(b.Prefix, f.artifact)
	if err != nil {
		t.Fatal(err)
	}
	if report := f.runCycle(); report.Completed != 1 {
		t.Fatalf("the first hop did not complete: %+v", report)
	}
	row := externalRow(t, f, keys)
	before := append([]byte(nil), f.medium.bytesAt(f.key)...)

	report, err := f.engine.RunCycle(f.ctx, []placement.Plan{{Artifact: f.artifact, DestinationMedium: mediumB}})
	if err != nil {
		t.Fatal(err)
	}
	f.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("the re-home did not complete: %+v (move error %q)", report, f.onlyMove().Error)
	}
	if !bytes.Equal(before, f.medium.bytesAt(keyB)) {
		t.Fatal("the re-homed object is not the same ciphertext")
	}
	dst, ok, err := f.journal.PlacementEncryption(f.ctx, f.artifact, mediumB)
	if err != nil || !ok || dst.KeyID != row.ID {
		t.Fatalf("destination record %+v ok %v err %v, want key %s", dst, ok, err, row.ID)
	}
}

// Rotating the external key moves new copies to the new row; the copy
// written under the old row still verifies through it, because the old
// reference is still declared.
func TestARotatedExternalKeyKeepsTheOldCopyVerifiable_1292(t *testing.T) {
	f, keys, ref, _ := newExternalFixture(t)
	f.runCycle()
	f.guard.fail()
	old := externalRow(t, f, keys)

	newRef, _ := externalKeyAt(t, "offsite-2.key")
	keys.UseExternalRefs(func() []secretref.Ref { return []secretref.Ref{ref, newRef} })
	next, err := keys.RotateExternal(f.ctx, old.ID, newRef, testNow2)
	if err != nil {
		t.Fatalf("RotateExternal: %v", err)
	}
	if next.ID == old.ID {
		t.Fatal("control: the rotation did not produce a different key")
	}
	if err := keys.WithKey(f.ctx, old.ID, func([]byte) error { return nil }); err != nil {
		t.Fatalf("the superseded external key no longer resolves: %v", err)
	}
	all, _ := keys.Keys(f.ctx)
	for _, k := range all {
		if k.ID == old.ID && k.Status != keycustody.StatusSuperseded {
			t.Fatalf("old row status %q, want superseded", k.Status)
		}
	}
}
