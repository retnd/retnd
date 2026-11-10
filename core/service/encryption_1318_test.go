package service

import (
	"context"
	"testing"
)

// The Backups list and Backup detail draw the suite id on the Encrypted chip
// (#1318), so each encrypted placement carries the suite its copy record
// names. The record's format_id IS the suite id (ADR 0031, D1), so the field
// is that column read back, never the policy's current suite: a copy written
// under age/v1 says age/v1 after the policy moved to age-pq/v1.
func TestArtifactPlacementsCarryTheSuiteTheChipShows_1318(t *testing.T) {
	f, a, _ := encryptedFixture(t)
	ctx := context.Background()

	got, err := f.b.GetArtifact(ctx, a.String())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range got.Placements {
		if p.Medium != "warm" {
			continue
		}
		found = true
		// encryptedFixture records format id "age" for the copy on warm.
		if p.EncryptionSuite != "age" {
			t.Errorf("the encrypted copy reads suite %q, want the recorded format id %q", p.EncryptionSuite, "age")
		}
		if w := placementWire(p); w.EncryptionSuite != p.EncryptionSuite {
			t.Errorf("the wire drops the suite: %q, want %q", w.EncryptionSuite, p.EncryptionSuite)
		}
	}
	if !found {
		t.Fatal("the fixture's copy on warm is missing")
	}

	list, err := f.b.ListArtifacts(ctx, ArtifactFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var inList bool
	for _, art := range list {
		for _, p := range art.Placements {
			if art.ID == a.String() && p.Medium == "warm" && p.EncryptionSuite == "age" {
				inList = true
			}
		}
	}
	if !inList {
		t.Error("the list response does not carry the suite, so the chip would need a request per row")
	}

	plain := seedComplete(t, f, "plain.dump", "warm")
	pg, err := f.b.GetArtifact(ctx, plain.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pg.Placements {
		if p.EncryptionSuite != "" {
			t.Errorf("a plaintext copy names a suite: %+v", p)
		}
	}
}
