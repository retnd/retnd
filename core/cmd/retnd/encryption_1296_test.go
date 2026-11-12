package main

import (
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1296: `encryption policy show` prints, for a kopia set, the sentence the
// web UI already draws, and prints nothing extra for a set the field is empty on.
//
// Written by reading; not run.

func policyOfOneSet(satisfiedBy string) apicontract.EncryptionSetPolicy {
	return apicontract.EncryptionSetPolicy{
		BackupSetID:          "production/uploads-tree",
		Mode:                 "inherit",
		EffectiveMode:        "required",
		EffectiveKeyProvider: apicontract.EncryptionKeyProviderState{Kind: "managed"},
		KeyProvider:          apicontract.EncryptionKeyProviderState{Kind: "none"},
		SatisfiedBy:          satisfiedBy,
	}
}

func TestPolicyShowEchoesThatARepositoryEncryptedSetIsSatisfiedByTheRepository_1296(t *testing.T) {
	out := captureStdout(t, func() { printEncryptionSet("", policyOfOneSet("repository")) })
	if !strings.Contains(out, service.SatisfiedByRepositoryStatement) {
		t.Errorf("the line for a kopia set does not carry the statement:\n%s", out)
	}
}

func TestPolicyShowSaysNothingExtraForASetNothingElseSatisfies_1296(t *testing.T) {
	out := captureStdout(t, func() { printEncryptionSet("", policyOfOneSet("")) })
	if strings.Contains(out, "repository") {
		t.Errorf("a set with an empty SatisfiedBy mentions the repository:\n%s", out)
	}
}
