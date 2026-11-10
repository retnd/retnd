package service

import (
	"context"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/config"
)

// EPIC T, T1.5 (#1315): for an external key the policy names the suite of
// the key the reference yields, and a pinned suite that key is not of says
// so without pointing at a managed rotation that could not help. Written by
// reading; not run.

func TestTheDefaultPolicyUnderAnExternalKeyNamesThatKeysSuite_1315(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	fileBacked(t, f)
	ctx := context.Background()
	ref, _ := externalRefFile(t) // an age/v1 key
	policy := config.RetainedCopyEncryption{
		Mode:        config.RetainedCopyEncryptionRequired,
		KeyProvider: config.RetainedCopyKeyProvider{External: &config.SecretSource{File: ref.File}},
		Suite:       config.RetainedCopyEncryptionSuiteDefault,
	}
	// A read never resolves the reference: before anything in this process
	// has, the answer is empty rather than a guess, and asking does not
	// resolve it as a side effect.
	if got := f.b.effectiveSuite(ctx, policy); got != "" {
		t.Fatalf("the policy read named suite %q for an external key nothing has resolved yet", got)
	}
	if got := f.b.effectiveSuite(ctx, policy); got != "" {
		t.Fatalf("the first read resolved the reference: the second one says %q", got)
	}
	// Custody resolves it, as the alert tick's probe or a patch does; the
	// read then names that key's suite and not the build default.
	if _, err := f.b.keys.ExternalKeySuite(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if got := f.b.effectiveSuite(ctx, policy); got != "age/v1" {
		t.Fatalf("the default policy under an age/v1 external key reports %q after a resolution, want age/v1", got)
	}
}

func TestPinningASuiteTheExternalKeyIsNotOfAdvisesNoManagedRotation_1315(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	fileBacked(t, f)
	ref, _ := externalRefFile(t) // an age/v1 key
	got, err := f.b.PatchEncryptionPolicy(context.Background(), apicontract.EncryptionPolicyPatch{
		Mode:        config.RetainedCopyEncryptionRequired,
		KeyProvider: &apicontract.EncryptionKeyProvider{External: &apicontract.EncryptionKeyReference{File: ref.File}},
		Suite:       "age-pq/v1",
	})
	if err != nil {
		t.Fatalf("PatchEncryptionPolicy: %v", err)
	}
	if !got.RotationNeeded {
		t.Error("an age/v1 external key under a pinned age-pq/v1 policy did not say copies are refused")
	}
	if got.RotateCommand != "" {
		t.Errorf("rotate_command = %q; a managed rotation cannot change an external key", got.RotateCommand)
	}
	if got.Suite != "age-pq/v1" || got.SuiteConfigured != "age-pq/v1" {
		t.Errorf("suite = %q configured %q, want age-pq/v1 both", got.Suite, got.SuiteConfigured)
	}
}
