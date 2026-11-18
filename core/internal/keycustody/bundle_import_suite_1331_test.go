package keycustody_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/keycustody"
)

// EPIC T's TV.14 (#1315, closed on the release PR #1331): a bundle whose
// format names a suite this build has no adapter for is refused naming it,
// and nothing is registered or written. The digest is recomputed, so the
// refusal is the suite check and not the integrity check: a digest anybody
// can compute is no defence, and a bundle edited this way is exactly what
// an import from a newer build looks like.
//
// The guard is the s.formatOf call in Import. Its planted violation, "a
// bundle under an unregistered suite imported as age/v1" in
// scripts/bdtools/encryption/selftest.py, falls back to age/v1 there, and
// this test is what goes red.
func TestImportRefusesABundleUnderASuiteThisBuildDoesNotHave_1331(t *testing.T) {
	ctx := context.Background()
	src := openStore(t, t.TempDir())
	_, bundleBytes, _, _ := exportBundle(t, src)
	b := readBundle(t, bundleBytes)
	b.FormatID = "nonsense/v1"
	b.Digest = redigest(t, b)

	root := t.TempDir()
	s := openStore(t, root)
	_, imported, err := s.Import(ctx, b, fixedNow)
	if !errors.Is(err, keycustody.ErrUnknownSuite) || imported {
		t.Fatalf("Import of a nonsense/v1 bundle = %v, %v; want ErrUnknownSuite and nothing imported", imported, err)
	}
	if !strings.Contains(err.Error(), "nonsense/v1") {
		t.Errorf("the refusal does not name the suite it cannot open: %v", err)
	}
	if files := keyFiles(t, root); len(files) != 0 {
		t.Errorf("a refused Import left %v behind", files)
	}
	if keys, _ := s.Keys(ctx); len(keys) != 0 {
		t.Errorf("a refused Import registered %+v", keys)
	}

	// The control: the same bundle under its own suite imports, so the
	// refusal above is about the suite and nothing else.
	if _, imported, err := openStore(t, t.TempDir()).Import(ctx, readBundle(t, bundleBytes), fixedNow); err != nil || !imported {
		t.Fatalf("the unedited bundle did not import: %v, %v", imported, err)
	}
}
