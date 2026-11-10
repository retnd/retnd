package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/secretref"
	"github.com/retnd/retnd/core/internal/state"
)

// #1304: dropping the reference of an external key that ACTIVE copies still
// record. Written by reading; not run.

// externalRefFile writes a fresh age key to a 0600 file in a 0700 directory
// and returns the reference to it and the material, for the canary scan.
func externalRefFile(t *testing.T) (secretref.Ref, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	material, _, err := keycustody.AgeFormat{}.Generate()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "offsite.key")
	if err := os.WriteFile(path, append(material, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return secretref.Ref{File: path}, string(material)
}

// declaring makes the service's custody see a configuration that declares
// exactly refs as the global external provider(s). Only the first can be the
// global one; the rest ride a set override, which externalRefs also reads.
func declaring(f *recoveryFixture, refs ...secretref.Ref) {
	cfg := &config.Config{}
	if len(refs) > 0 {
		cfg.RetainedCopyEncryption.KeyProvider = config.RetainedCopyKeyProvider{External: &config.SecretSource{File: refs[0].File}}
	}
	if len(refs) > 1 {
		set := config.BackupSet{}
		set.RetainedCopyEncryption.KeyProvider = config.RetainedCopyKeyProvider{External: &config.SecretSource{File: refs[1].File}}
		cfg.Sources = []config.Source{{Name: "production", BackupSets: []config.BackupSet{set}}}
	}
	f.b.keys.UseConfig(func() *config.Config { return cfg })
}

// externalCopy registers external key A, rotates to B, and records one
// encrypted copy under A on "warm". It returns the keys, the copy and A's
// material.
func externalCopy(t *testing.T, withCopy bool) (f *recoveryFixture, a keycustody.KeyRecord, art model.ArtifactID, refA, refB secretref.Ref, material string) {
	t.Helper()
	f = newRecoveryFixture(t, fixtureOpts{})
	fileBacked(t, f)
	ctx := context.Background()
	refA, material = externalRefFile(t)
	refB, _ = externalRefFile(t)
	declaring(f, refA, refB)
	a, err := f.b.keys.RegisterExternal(ctx, refA, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store, err := f.b.keys.Store(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateExternal(ctx, a.ID, refB, time.Now()); err != nil {
		t.Fatal(err)
	}
	if withCopy {
		art = seedComplete(t, f, "enc.dump", "warm")
		if err := f.b.journal.RecordPlacementEncryption(ctx, art, "warm", state.PlacementEncryption{
			FormatID: "age", FormatVersion: 1, KeyID: a.ID,
			StoredHash: strings.Repeat("e", 64), StoredHashAlg: "sha256", StoredSize: int64(len(f.data)) + 200,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return f, a, art, refA, refB, material
}

func TestADroppedExternalReferenceWithAnActiveCopyIsReportedAndRetireIsRefused_1304(t *testing.T) {
	f, a, art, refA, refB, material := externalCopy(t, true)
	ctx := context.Background()

	// While the reference is declared, nothing is at risk.
	if got, err := f.b.keys.ExternalKeysAtRisk(ctx); err != nil || len(got) != 0 {
		t.Fatalf("with the reference declared: %+v, %v, want nothing at risk", got, err)
	}

	// Drop A's reference, keep B's.
	declaring(f, refB)
	got, err := f.b.keys.ExternalKeysAtRisk(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].KeyID != a.ID || len(got[0].Copies) != 1 || got[0].Copies[0].Artifact != art || got[0].Copies[0].Medium != "warm" {
		t.Fatalf("at risk = %+v, want key %s with the copy of %s on warm", got, a.ID, art)
	}

	// Retire is refused naming the copy, and the key stays unretired.
	_, err = f.b.RetireEncryptionKey(ctx, a.ID)
	if !errors.Is(err, ErrEncryptionKeyInUse) || !strings.Contains(err.Error(), art.String()+" on warm") {
		t.Fatalf("retire = %v, want ErrEncryptionKeyInUse naming %s on warm", err, art)
	}

	// No reference text and no material anywhere in what was said. The
	// control proves the scan can fail.
	raw, _ := json.Marshal(got)
	say := string(raw) + err.Error()
	if !strings.Contains(say+material, material) {
		t.Fatal("control: the scan did not see a planted canary")
	}
	for _, leak := range []string{material, strings.TrimSpace(material), refA.File} {
		if strings.Contains(say, leak) {
			t.Errorf("the messages carry %q", leak)
		}
	}
}

func TestADroppedExternalReferenceWithNoActiveCopyIsNotReportedAndRetireSucceeds_1304(t *testing.T) {
	f, a, _, _, refB, _ := externalCopy(t, false)
	ctx := context.Background()
	declaring(f, refB)

	if got, err := f.b.keys.ExternalKeysAtRisk(ctx); err != nil || len(got) != 0 {
		t.Fatalf("at risk = %+v, %v, want nothing: no ACTIVE copy records the key", got, err)
	}
	got, err := f.b.RetireEncryptionKey(ctx, a.ID)
	if err != nil || got.Status != "retired" {
		t.Fatalf("retire = %+v, %v, want the key retired", got, err)
	}
}

func TestNoExternalKeyRowsMeansNoStoreIsOpenedToLookForRisk_1304(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	dir := fileBacked(t, f)
	got, err := f.b.keys.ExternalKeysAtRisk(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("at risk = %+v, %v, want nothing", got, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, keycustody.DirName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("looking for risk created the key directory (stat: %v): a deployment that uses no external key must stay byte identical", statErr)
	}
}
