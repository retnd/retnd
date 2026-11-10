package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/recovery"
)

// EPIC N, #992 (N1.14): `catalog rebuild` over a deployment with no state
// identifies encrypted copies from the sidecars alone and says what is
// missing, and it does that without reading key material: the key provider is
// named, never resolved. Whether a key resolves is a content revalidation's
// question and that path says "could not check" when it does not.
//
// Written by reading; not run.

type fakeRegistry struct{ keys []keycustody.KeyRecord }

func (r fakeRegistry) Keys(context.Context) ([]keycustody.KeyRecord, error) { return r.keys, nil }

// encryptedSidecar writes the sidecar of an artifact that has an encrypted
// copy on offsite_s3 and no local copy, the shape a state-less deployment
// finds on disk.
func encryptedSidecar(t *testing.T, localDir string) {
	t.Helper()
	at := time.Date(2026, 8, 20, 3, 5, 0, 0, time.UTC)
	verified := at.Add(time.Hour)
	m := recovery.Manifest{
		FormatVersion: recovery.CurrentFormatVersion, Source: "production", BackupSet: "postgres-primary",
		ArtifactName: "backup.dump", RemotePath: "/backups/backup.dump",
		ReceivedTimestamp: at, RetentionTimestamp: at, SizeBytes: 1024, Checksum: "deadbeef", ChecksumAlgorithm: "sha256",
		Placements: []recovery.ManifestPlacement{{
			Medium: "offsite_s3", Location: "retnd/production/postgres-primary/backup.dump", Status: "ACTIVE", VerificationClass: "content",
			Checksum: "deadbeef", ChecksumAlgorithm: "sha256",
			Encryption: &recovery.ManifestEncryption{
				FormatID: "age/v1", FormatVersion: 1, KeyID: "k0123456789abcdef",
				PlaintextDigest: "deadbeef", PlaintextDigestAlgorithm: "sha256", PlaintextSize: 1024,
				StoredDigest: "cafef00d", StoredDigestAlgorithm: "sha256", StoredSize: 1330, LastDecryptVerified: &verified,
			},
		}},
	}
	if err := recovery.WriteManifest(localDir, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
}

func rebuildWith(t *testing.T, policy config.RetainedCopyEncryption, registry KeyRegistry) CatalogRebuildFinding {
	t.Helper()
	localDir := t.TempDir()
	encryptedSidecar(t, localDir)
	bs := testBackupSet(t, localDir)
	bs.RetainedCopyEncryption = policy
	svc := New(testConfig(t, testSource("production", bs)), openJournal(t), newFakeTransport(), nil)
	svc.Now = fixedNow(epoch)
	svc.Keys = registry

	report, err := svc.RebuildCatalog(context.Background(), bs.ID, true)
	if err != nil {
		t.Fatalf("RebuildCatalog: %v", err)
	}
	if len(report.Errors) != 0 || len(report.Findings) != 1 {
		t.Fatalf("findings %+v errors %+v, want one finding and no errors", report.Findings, report.Errors)
	}
	return report.Findings[0]
}

func managed() config.RetainedCopyEncryption {
	return config.RetainedCopyEncryption{
		Mode: config.RetainedCopyEncryptionRequired, KeyProvider: config.RetainedCopyKeyProvider{Managed: true},
	}
}

func TestRebuildIdentifiesAnEncryptedCopyAndNamesTheMissingManagedKey_992(t *testing.T) {
	f := rebuildWith(t, managed(), fakeRegistry{}) // the registry is empty: the state is gone

	if len(f.EncryptedCopies) != 1 {
		t.Fatalf("EncryptedCopies = %+v, want the one copy on offsite_s3", f.EncryptedCopies)
	}
	c := f.EncryptedCopies[0]
	if c.Medium != "offsite_s3" || c.KeyID != "k0123456789abcdef" || c.FormatID != "age/v1" || c.KeyKnown {
		t.Errorf("encrypted copy = %+v", c)
	}
	joined := strings.Join(f.Notes, "\n")
	for _, want := range []string{"encrypted", "age/v1", "k0123456789abcdef", "key registry", "recovery bundle"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the notes do not say %q:\n%s", want, joined)
		}
	}
}

func TestRebuildSaysSoWhenTheSetHasNoKeyProviderAtAll_992(t *testing.T) {
	f := rebuildWith(t, config.RetainedCopyEncryption{Mode: config.RetainedCopyEncryptionOff}, fakeRegistry{})
	joined := strings.Join(f.Notes, "\n")
	if len(f.EncryptedCopies) != 1 || !strings.Contains(joined, "no key provider") {
		t.Errorf("copies %+v notes:\n%s\nwant the copy identified and the missing provider named", f.EncryptedCopies, joined)
	}
}

func TestRebuildRecognisesAKnownManagedKeyWithoutReadingIt_992(t *testing.T) {
	f := rebuildWith(t, managed(), fakeRegistry{keys: []keycustody.KeyRecord{{ID: "k0123456789abcdef", Status: keycustody.StatusActive}}})
	if len(f.EncryptedCopies) != 1 || !f.EncryptedCopies[0].KeyKnown {
		t.Fatalf("EncryptedCopies = %+v, want the key known", f.EncryptedCopies)
	}
	if strings.Contains(strings.Join(f.Notes, "\n"), "not in this deployment") {
		t.Error("a registered key is reported as missing")
	}
}

// An external provider is named by its reference description, and the
// reference is never followed: a command reference that would leave a marker
// if it ran does not run, a file reference is not opened.
func TestRebuildNamesAnExternalProviderWithoutResolvingIt_992(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "key.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\necho AGE-SECRET-KEY-1CANARY\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := config.RetainedCopyEncryption{
		Mode:        config.RetainedCopyEncryptionRequired,
		KeyProvider: config.RetainedCopyKeyProvider{External: &config.SecretSource{Command: []string{script}}},
	}
	f := rebuildWith(t, policy, fakeRegistry{})

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("catalog rebuild executed the external key command: it must name the provider and never resolve it")
	}
	joined := strings.Join(f.Notes, "\n")
	if !strings.Contains(joined, "command "+script) {
		t.Errorf("the notes do not name the external provider:\n%s", joined)
	}
	if strings.Contains(joined, "CANARY") {
		t.Error("key material reached the report")
	}
}

// A rebuild with no key custody wired at all still identifies the copy, and
// says it cannot tell whether the key is known rather than guessing.
func TestRebuildWithNoRegistryStillIdentifiesTheCopy_992(t *testing.T) {
	f := rebuildWith(t, managed(), nil)
	if len(f.EncryptedCopies) != 1 {
		t.Fatalf("EncryptedCopies = %+v", f.EncryptedCopies)
	}
}

// A plaintext medium copy keeps today's note and gains nothing.
func TestAPlaintextMediumCopyIsNotReportedAsEncrypted_992(t *testing.T) {
	localDir := t.TempDir()
	at := time.Date(2026, 8, 20, 3, 5, 0, 0, time.UTC)
	m := recovery.Manifest{
		FormatVersion: recovery.CurrentFormatVersion, Source: "production", BackupSet: "postgres-primary",
		ArtifactName: "backup.dump", RemotePath: "/backups/backup.dump", ReceivedTimestamp: at, RetentionTimestamp: at, SizeBytes: 1,
		Placements: []recovery.ManifestPlacement{{Medium: "offsite_s3", Location: "k", Status: "ACTIVE"}},
	}
	if err := recovery.WriteManifest(localDir, m); err != nil {
		t.Fatal(err)
	}
	bs := testBackupSet(t, localDir)
	svc := New(testConfig(t, testSource("production", bs)), openJournal(t), newFakeTransport(), nil)
	report, err := svc.RebuildCatalog(context.Background(), bs.ID, true)
	if err != nil || len(report.Findings) != 1 {
		t.Fatalf("err %v findings %+v", err, report.Findings)
	}
	if len(report.Findings[0].EncryptedCopies) != 0 || len(report.Findings[0].Notes) != 1 {
		t.Errorf("finding = %+v, want the one existing note and no encrypted copy", report.Findings[0])
	}
}
