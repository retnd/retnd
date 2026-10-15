// Package localrepo makes a real local repository for a test that needs one
// to exist. It is its own package, and not part of testenv, for two reasons:
// the boundary test keeps the engine's vendor name out of core/cmd, so a
// cmd test reaches the engine through this and not by importing it, and
// testenv is imported by a test inside the package the engine itself imports.
package localrepo

import (
	"context"
	"testing"

	"github.com/retnd/retnd/core/internal/backupengine"
	"github.com/retnd/retnd/core/internal/backupengine/kopia"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/secretref"
)

// CreateLocal creates the repository the product would create for domain
// under root, with the passphrase in passphraseFile, through the same engine.
func CreateLocal(t *testing.T, root, domain, passphraseFile string) {
	t.Helper()
	id, err := model.NewRepositoryDomainID(domain)
	if err != nil {
		t.Fatalf("NewRepositoryDomainID: %v", err)
	}
	loc := backupengine.RepositoryLocation{
		Kind:       backupengine.LocationLocal,
		Domain:     id,
		Root:       root,
		Passphrase: secretref.Ref{File: passphraseFile},
	}
	if err := kopia.New().CreateRepository(context.Background(), loc); err != nil {
		t.Fatalf("creating the repository: %v", err)
	}
}
