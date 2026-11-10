package state

import (
	"context"
	"testing"
)

// EPIC T, T2.1 (#1316): the per-suite count of ACTIVE copies that the key
// custody health section and `retnd encryption suites` report. It counts
// only ACTIVE copies, and it counts an id as recorded, so a copy under a
// suite this build does not know is named rather than dropped.
//
// Written by reading; not run (the suites run on the release PR).
func TestActiveCopiesBySuite_CountsOnlyActiveCopiesUnderTheirRecordedId(t *testing.T) {
	ctx := context.Background()
	j, a := scrubFixture(t)
	if err := j.RegisterEncryptionKey(ctx, EncryptionKey{ID: "k1", Provider: KeyProviderExternal, CreatedAt: scrubAt}); err != nil {
		t.Fatal(err)
	}
	if got, err := j.ActiveCopiesBySuite(ctx); err != nil || len(got) != 0 {
		t.Fatalf("a journal with no encrypted copy counts %v, %v", got, err)
	}
	if err := j.RecordPlacementEncryption(ctx, a, MediumLocal, PlacementEncryption{FormatID: "age/v1", FormatVersion: 1, KeyID: "k1", StoredHash: "c", StoredHashAlg: "sha256", StoredSize: 5000}); err != nil {
		t.Fatal(err)
	}
	if got, err := j.ActiveCopiesBySuite(ctx); err != nil || len(got) != 1 || got["age/v1"] != 1 {
		t.Fatalf("ActiveCopiesBySuite = %v, %v; want age/v1: 1", got, err)
	}

	// An id the registry does not know (planted past the write path's
	// refusal) is counted under itself, not dropped and not folded into
	// another suite.
	if _, err := j.db.ExecContext(ctx, `UPDATE placement_encryption SET format_id = ?`, "tink-aes256gcm-hkdf-1mb/v1"); err != nil {
		t.Fatal(err)
	}
	if got, err := j.ActiveCopiesBySuite(ctx); err != nil || len(got) != 1 || got["tink-aes256gcm-hkdf-1mb/v1"] != 1 {
		t.Fatalf("ActiveCopiesBySuite = %v, %v; want the unknown id counted under itself", got, err)
	}

	if _, err := j.db.ExecContext(ctx, `UPDATE placements SET status = ?`, PlacementGone); err != nil {
		t.Fatal(err)
	}
	if got, err := j.ActiveCopiesBySuite(ctx); err != nil || len(got) != 0 {
		t.Errorf("a copy that is no longer ACTIVE still counts: %v, %v", got, err)
	}
}
