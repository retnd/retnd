package config

import "testing"

// #1296: ADR 0028 decision 4 says required on a kopia-engine set is satisfied
// by kopia's own repository encryption. These pin the one method every
// consumer reads for that, and that the config still accepts the spelling.
//
// Written by reading; not run.

func TestRequiredOnARepositoryEncryptedSetIsAcceptedAndDoesNotEncryptRetainedCopies_1296(t *testing.T) {
	c := incrementalConfig()
	c.RetainedCopyEncryptionBlock = &RetainedCopyEncryptionBlock{
		Mode: RetainedCopyEncryptionRequired, KeyProvider: managed(),
	}
	mustValidate(t, &c)

	copySet, kopiaSet := theSet(t, &c, 0), theSet(t, &c, 1)
	if kopiaSet.Engine == copySet.Engine {
		t.Fatalf("the fixture's two sets run the same engine (%q), so the comparison below proves nothing", kopiaSet.Engine)
	}

	if !kopiaSet.RetainedCopyEncryption.Enabled() {
		t.Errorf("the kopia set's resolved mode = %q; required is still accepted and still resolves, the resolution is not what changes", kopiaSet.RetainedCopyEncryption.Mode)
	}
	if kopiaSet.EncryptsRetainedCopies() {
		t.Error("EncryptsRetainedCopies is true for a kopia set, so the age pass, the health section and the alert would all treat a repository-encrypted set as one a managed key guards")
	}

	if !copySet.RetainedCopyEncryption.Enabled() || !copySet.EncryptsRetainedCopies() {
		t.Errorf("the copy-engine set beside it: Enabled=%v EncryptsRetainedCopies=%v, want both true",
			copySet.RetainedCopyEncryption.Enabled(), copySet.EncryptsRetainedCopies())
	}
}

func TestRequiredAsAPerSetOverrideOnARepositoryEncryptedSetIsAcceptedToo_1296(t *testing.T) {
	c := incrementalConfig()
	c.Sources[0].BackupSets[1].RetainedCopyEncryptionConfig = &RetainedCopyEncryptionOverride{
		Mode: RetainedCopyEncryptionRequired, KeyProvider: managed(),
	}
	mustValidate(t, &c)

	kopiaSet := theSet(t, &c, 1)
	if !kopiaSet.RetainedCopyEncryption.Enabled() || kopiaSet.EncryptsRetainedCopies() {
		t.Errorf("an explicit required on a kopia set: Enabled=%v EncryptsRetainedCopies=%v, want true and false",
			kopiaSet.RetainedCopyEncryption.Enabled(), kopiaSet.EncryptsRetainedCopies())
	}
}

func TestAnOffSetNeverEncryptsRetainedCopiesWhateverItsEngine_1296(t *testing.T) {
	c := incrementalConfig()
	mustValidate(t, &c)
	for i := range c.Sources[0].BackupSets {
		if theSet(t, &c, i).EncryptsRetainedCopies() {
			t.Errorf("set %d resolves to off and still reports that it encrypts retained copies", i)
		}
	}
}
