package app

import (
	"context"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/state"
)

// EPIC T, T2.1 (#1316, FR-87): `catalog rebuild` over a sidecar naming a
// suite. An id the registry does not know is recorded as "unknown suite",
// never as plaintext and never as content, and is named in the report; a
// retired suite's copy is catalogued normally and counted.
//
// The test lives beside RebuildCatalog (package app), where the sidecar's
// encryption block is read; recovery only parses the file.
//
// Written by reading; not run (the suites run on the release PR).

// rebuildSuite runs a real (not dry) rebuild over one sidecar naming
// formatID and returns the report and the journal it wrote to.
func rebuildSuite(t *testing.T, formatID string) (CatalogRebuildReport, *state.Journal) {
	t.Helper()
	localDir := t.TempDir()
	suiteSidecar(t, localDir, "backup.dump", formatID, "k0123456789abcdef")
	bs := testBackupSet(t, localDir)
	bs.RetainedCopyEncryption = managed()
	j := openJournal(t)
	svc := New(testConfig(t, testSource("production", bs)), j, newFakeTransport(), nil)
	svc.Now = fixedNow(epoch)
	svc.Keys = fakeRegistry{}

	report, err := svc.RebuildCatalog(context.Background(), bs.ID, false)
	if err != nil {
		t.Fatalf("RebuildCatalog: %v", err)
	}
	if len(report.Errors) != 0 || len(report.Findings) != 1 {
		t.Fatalf("findings %+v errors %+v, want one finding and no errors", report.Findings, report.Errors)
	}
	return report, j
}

func TestRebuildOverAnUnknownSuiteSaysSoAndNeverSaysPlaintext(t *testing.T) {
	const unknown = "tink-aes256gcm-hkdf-1mb/v1" // reserved, not registered in this build
	if _, ok := copycrypt.Lookup(unknown); ok {
		t.Fatalf("setup: %q is registered, so it cannot stand for an unknown suite", unknown)
	}
	report, j := rebuildSuite(t, unknown)
	f := report.Findings[0]

	if len(f.EncryptedCopies) != 1 {
		t.Fatalf("EncryptedCopies = %+v: a copy under an unknown suite was dropped, which reads it as plaintext", f.EncryptedCopies)
	}
	c := f.EncryptedCopies[0]
	if c.Suite != CatalogUnknownSuite || c.FormatID != unknown {
		t.Errorf("the copy reads %+v, want suite %q and format_id %q", c, CatalogUnknownSuite, unknown)
	}
	if report.SuiteCopies[unknown] != 1 {
		t.Errorf("SuiteCopies = %v, want the copy counted under %q", report.SuiteCopies, unknown)
	}
	for id := range report.SuiteCopies {
		if id != unknown {
			t.Errorf("SuiteCopies counts %q: an unknown suite's copy was counted under another suite", id)
		}
	}
	joined := strings.Join(f.Notes, "\n")
	for _, want := range []string{"unknown suite", unknown, "neither plaintext nor verified"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the notes do not say %q:\n%s", want, joined)
		}
	}

	// Nothing the rebuild wrote claims the medium copy: no placement row on
	// the medium (so nothing reads it as plaintext), and the local row it
	// did write claims no class (so nothing reads it as content).
	art := f.Artifact
	if _, ok, err := j.PlacementEncryption(context.Background(), art, "offsite_s3"); err != nil || ok {
		t.Errorf("a placement_encryption row exists for the medium copy after rebuild (err %v)", err)
	}
	rec, err := j.Get(context.Background(), art)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, p := range rec.Placements {
		if p.Medium == "offsite_s3" {
			t.Errorf("the rebuild adopted the medium copy as a placement %+v", p)
		}
		if p.VerificationClass == state.VerificationContent {
			t.Errorf("a rebuilt placement claims content: %+v", p)
		}
	}
}

func TestRebuildOverARetiredSuiteCataloguesAndCountsTheCopy(t *testing.T) {
	ts := copycrypt.InjectForTest(t, copycrypt.Default(), copycrypt.TestSuitePrefix+"retired/v1", copycrypt.StatusRetired, "0.9.0")
	report, _ := rebuildSuite(t, ts.ID())
	f := report.Findings[0]

	if f.Action != CatalogRebuildReconstructed {
		t.Fatalf("a sidecar with a retired suite's copy was not reconstructed: %+v", f)
	}
	if len(f.EncryptedCopies) != 1 || f.EncryptedCopies[0].Suite != string(copycrypt.StatusRetired) {
		t.Fatalf("EncryptedCopies = %+v, want the copy catalogued as retired", f.EncryptedCopies)
	}
	if report.SuiteCopies[ts.ID()] != 1 {
		t.Errorf("SuiteCopies = %v, want the retired suite's copy counted", report.SuiteCopies)
	}
	joined := strings.Join(f.Notes, "\n")
	if strings.Contains(joined, CatalogUnknownSuite) {
		t.Errorf("a retired suite was reported as unknown:\n%s", joined)
	}
	if !strings.Contains(joined, "retired suite") || !strings.Contains(joined, "still reads") {
		t.Errorf("the notes do not say the copy is under a retired suite that still reads:\n%s", joined)
	}

	// Control: the same sidecar under the default suite is quiet about suites.
	report, _ = rebuildSuite(t, copycrypt.Default().ID())
	if got := report.Findings[0].EncryptedCopies[0].Suite; got != string(copycrypt.Default().Status()) {
		t.Errorf("control: the default suite's copy reads %q", got)
	}
	if notes := strings.Join(report.Findings[0].Notes, "\n"); strings.Contains(notes, CatalogUnknownSuite) || strings.Contains(notes, "retired suite") {
		t.Errorf("control: the default suite's copy was reported as unknown or retired:\n%s", notes)
	}
}
