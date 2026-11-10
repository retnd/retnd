package placement_test

import (
	"bytes"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
)

// #1296: encryptedSet reads config.BackupSet.EncryptsRetainedCopies, so a
// kopia set that resolves to required is not an encrypted set to the copy
// path. The cycle never sends such a set here today (it returns into the
// incremental pipeline first), and this says so on purpose rather than by the
// order of app/cycle.go: if a kopia set did reach this engine, it would not
// get an age pass, a placement_encryption row or a managed key.
//
// Written by reading; not run.

func TestAKopiaSetUnderRequiredIsNotAnEncryptedSetToTheCopyPath_1296(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	f.engine.Sets = fixedSets{set: config.BackupSet{
		Name: testSet, ID: f.artifact.Set, LocalPath: f.localDir, Engine: model.EngineKopia,
		RetainedCopyEncryption: config.RetainedCopyEncryption{
			Mode:        config.RetainedCopyEncryptionRequired,
			KeyProvider: config.RetainedCopyKeyProvider{Managed: true},
		},
	}}

	f.runCycle()

	if _, ok := f.encryption(); ok {
		t.Error("a placement_encryption row was written for a kopia set; its copies are satisfied by the repository and carry no per-copy record")
	}
	if obj := f.medium.bytesAt(f.objectKey()); bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) {
		t.Error("a kopia set's copy went through the age pass")
	}
}
