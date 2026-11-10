package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
)

// #1296: GET /encryption/policy and `retnd encryption policy show` say a
// kopia set is satisfied by the repository, the same thing the web UI says.
//
// Written by reading; not run.

func requiredPolicy() config.RetainedCopyEncryption {
	return config.RetainedCopyEncryption{
		Mode:        config.RetainedCopyEncryptionRequired,
		KeyProvider: config.RetainedCopyKeyProvider{Managed: true},
	}
}

func TestSetPolicyOfAKopiaSetSaysSatisfiedByTheRepository_1296(t *testing.T) {
	kopia := setPolicyOf("production", config.BackupSet{Name: "uploads-tree", Engine: model.EngineKopia, RetainedCopyEncryption: requiredPolicy()})
	if kopia.SatisfiedBy != "repository" {
		t.Errorf("a kopia set's SatisfiedBy = %q, want %q", kopia.SatisfiedBy, "repository")
	}
	if kopia.EffectiveMode != config.RetainedCopyEncryptionRequired {
		t.Errorf("the kopia set's effective mode = %q; the field is additive and the resolution still reads required", kopia.EffectiveMode)
	}

	for _, e := range []model.BackupEngine{"", model.EngineArtifact} {
		got := setPolicyOf("production", config.BackupSet{Name: "postgres-primary", Engine: e, RetainedCopyEncryption: requiredPolicy()})
		if got.SatisfiedBy != "" {
			t.Errorf("an engine %q set's SatisfiedBy = %q, want empty", e, got.SatisfiedBy)
		}
	}
}

// The UI draws its own sentence for a kopia set (SetEncryptionCard reads the
// engine on the client). This pins that sentence to the one the CLI prints
// from the API's field, so the two cannot drift: a reworded statement on one
// side fails here until the other side follows.
func TestTheKopiaStatementTheCLIPrintsIsTheOneTheWebUIDraws_1296(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "ui", "shared", "src", "encryption", "SetEncryptionCard.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), SatisfiedByRepositoryStatement) {
		t.Errorf("SetEncryptionCard.tsx does not carry %q", SatisfiedByRepositoryStatement)
	}
}
