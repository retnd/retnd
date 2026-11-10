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
	ref, _ := externalRefFile(t) // an age/v1 key
	policy := config.RetainedCopyEncryption{
		Mode:        config.RetainedCopyEncryptionRequired,
		KeyProvider: config.RetainedCopyKeyProvider{External: &config.SecretSource{File: ref.File}},
		Suite:       config.RetainedCopyEncryptionSuiteDefault,
	}
	if got := f.b.effectiveSuite(context.Background(), policy); got != "age/v1" {
		t.Fatalf("the default policy under an age/v1 external key reports %q, want age/v1 (the build default would be a claim the engine does not honour)", got)
	}
	policy.KeyProvider.External.File = ref.File + ".gone"
	if got := f.b.effectiveSuite(context.Background(), policy); got != "" {
		t.Fatalf("an external reference that does not resolve reports suite %q, want none", got)
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
