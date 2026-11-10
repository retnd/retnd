package app

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/recovery"
)

// EPIC T, #1319 (T2.4), Suite C: catalog loss and rebuild over a backup root
// whose sidecars record copies under both shipped suites and one suite this
// build does not know. The two known ones are identified with their own
// suite id and key; the unknown one is identified as an encrypted copy under
// an unknown suite (CatalogUnknownSuite), named in the report, counted under
// its own id, and never adopted as a placement, so never plaintext and never
// verified content. A rebuild reads no key material either way (#992).
//
// Written by reading; not run.

// unknownSuite is an id no build registers. It is not a reserved Later id
// (tink-aes256gcm-hkdf-1mb/v1), so this test does not start passing by
// accident the day that suite ships.
const unknownSuite = "never-registered/v9"

func suiteSidecar(t *testing.T, localDir, name, suite, keyID string) {
	t.Helper()
	at := time.Date(2026, 11, 10, 3, 5, 0, 0, time.UTC)
	m := recovery.Manifest{
		FormatVersion: recovery.CurrentFormatVersion, Source: "production", BackupSet: "postgres-primary",
		ArtifactName: name, RemotePath: "/backups/" + name,
		ReceivedTimestamp: at, RetentionTimestamp: at, SizeBytes: 1024, Checksum: "deadbeef", ChecksumAlgorithm: "sha256",
		Placements: []recovery.ManifestPlacement{{
			Medium: "offsite_s3", Location: "retnd/production/postgres-primary/" + name, Status: "ACTIVE", VerificationClass: "content",
			Checksum: "deadbeef", ChecksumAlgorithm: "sha256",
			Encryption: &recovery.ManifestEncryption{
				FormatID: suite, FormatVersion: 1, KeyID: keyID,
				PlaintextDigest: "deadbeef", PlaintextDigestAlgorithm: "sha256", PlaintextSize: 1024,
				StoredDigest: "cafef00d", StoredDigestAlgorithm: "sha256", StoredSize: 2600,
			},
		}},
	}
	if err := recovery.WriteManifest(localDir, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
}

func TestRebuildOverTwoSuitesAndAnUnknownOneNamesEachAndNeverSaysPlaintext_1319(t *testing.T) {
	localDir := t.TempDir()
	copies := []struct{ artifact, suite, key string }{
		{"a-age.dump", "age/v1", "k00000000000000a1"},
		{"b-agepq.dump", "age-pq/v1", "k00000000000000b2"},
		{"c-unknown.dump", unknownSuite, "k00000000000000c3"},
	}
	for _, c := range copies {
		suiteSidecar(t, localDir, c.artifact, c.suite, c.key)
	}
	bs := testBackupSet(t, localDir)
	bs.RetainedCopyEncryption = managed()
	journal := openJournal(t)
	svc := New(testConfig(t, testSource("production", bs)), journal, newFakeTransport(), nil)
	svc.Now = fixedNow(epoch)
	svc.Keys = fakeRegistry{keys: []keycustody.KeyRecord{
		{ID: "k00000000000000a1", Status: keycustody.StatusSuperseded, SuiteID: "age/v1"},
		{ID: "k00000000000000b2", Status: keycustody.StatusActive, SuiteID: "age-pq/v1"},
	}}

	for _, dry := range []bool{true, false} {
		report, err := svc.RebuildCatalog(context.Background(), bs.ID, dry)
		if err != nil {
			t.Fatalf("RebuildCatalog(dry=%v): %v", dry, err)
		}
		if len(report.Errors) != 0 || len(report.Findings) != len(copies) {
			t.Fatalf("dry=%v: findings %+v errors %+v, want %d findings and no errors", dry, report.Findings, report.Errors, len(copies))
		}
		if want := map[string]int{"age/v1": 1, "age-pq/v1": 1, unknownSuite: 1}; !reflect.DeepEqual(report.SuiteCopies, want) {
			t.Errorf("dry=%v: the report counts %v per suite, want %v", dry, report.SuiteCopies, want)
		}
		byName := map[string]CatalogRebuildFinding{}
		for _, f := range report.Findings {
			byName[f.Artifact.Name] = f
		}
		for _, c := range copies {
			f, ok := byName[c.artifact]
			if !ok {
				t.Fatalf("dry=%v: no finding for %s", dry, c.artifact)
			}
			if len(f.EncryptedCopies) != 1 || f.EncryptedCopies[0].FormatID != c.suite || f.EncryptedCopies[0].KeyID != c.key {
				t.Errorf("dry=%v %s: encrypted copies %+v, want one under %s and %s", dry, c.artifact, f.EncryptedCopies, c.suite, c.key)
			}
			// Identified as an encrypted copy at all is the "never
			// plaintext" half; the Suite verdict is the registry's word or
			// CatalogUnknownSuite.
			wantVerdict := "supported"
			if c.suite == unknownSuite {
				wantVerdict = CatalogUnknownSuite
			}
			if len(f.EncryptedCopies) == 1 && f.EncryptedCopies[0].Suite != wantVerdict {
				t.Errorf("dry=%v %s: the copy's suite verdict is %q, want %q", dry, c.artifact, f.EncryptedCopies[0].Suite, wantVerdict)
			}
			notes := strings.Join(f.Notes, "\n")
			unknown := strings.Contains(notes, "unknown suite")
			if unknown != (c.suite == unknownSuite) {
				t.Errorf("dry=%v %s: notes say unknown suite = %v:\n%s", dry, c.artifact, unknown, notes)
			}
			if c.suite == unknownSuite && !strings.Contains(notes, unknownSuite) {
				t.Errorf("dry=%v: the note for the unknown suite does not name it:\n%s", dry, notes)
			}
			if c.suite != unknownSuite && (len(f.EncryptedCopies) != 1 || !f.EncryptedCopies[0].KeyKnown) {
				t.Errorf("dry=%v %s: the registered key is not recognised: %+v", dry, c.artifact, f.EncryptedCopies)
			}
		}
	}

	// After the real run, no medium copy was adopted as a journal placement,
	// so none of them, the unknown one least of all, can start life as
	// verified content.
	for _, c := range copies {
		artifact, err := model.NewArtifactID(bs.ID, c.artifact)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := journal.Get(context.Background(), artifact)
		if err != nil {
			t.Fatalf("reading %s back: %v", c.artifact, err)
		}
		for _, p := range rec.Placements {
			if p.Medium == "offsite_s3" {
				t.Errorf("%s: the rebuild adopted the off-site copy as a placement (%+v); a sidecar is a proposal", c.artifact, p)
			}
		}
	}
}
