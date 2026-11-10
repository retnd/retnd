package keycustody_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/keycustody"
)

// The Registry contract, run against the journal-backed registry over
// migration 0015's tables. reopen returns a registry over the same durable
// state through a fresh connection, which is what a restart is.
func TestJournalRegistryHonoursTheRegistryContract(t *testing.T) {
	root := t.TempDir()
	runRegistryContract(t, openJournalRegistry(t, root), func() keycustody.Registry { return openJournalRegistry(t, root) })
}

func runRegistryContract(t *testing.T, reg keycustody.Registry, reopen func() keycustody.Registry) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	a := keycustody.KeyRecord{ID: "key-a", Recipient: "age1aaa", Created: t0, Status: keycustody.StatusActive}
	b := keycustody.KeyRecord{ID: "key-b", Recipient: "age1bbb", Created: t0.Add(time.Hour), Status: keycustody.StatusActive}

	if keys, err := reg.Keys(ctx); err != nil || len(keys) != 0 {
		t.Fatalf("empty Keys = %v, %v", keys, err)
	}
	if err := reg.AddKey(ctx, a, ""); err != nil {
		t.Fatalf("AddKey a: %v", err)
	}
	if err := reg.AddKey(ctx, a, ""); !errors.Is(err, keycustody.ErrDuplicateKey) {
		t.Fatalf("AddKey duplicate err = %v, want ErrDuplicateKey", err)
	}

	// Supersede is part of the same write: after it, a is superseded and b
	// is the only active key, oldest first.
	if err := reg.AddKey(ctx, b, "key-a"); err != nil {
		t.Fatalf("AddKey b: %v", err)
	}
	keys, err := reg.Keys(ctx)
	if err != nil || len(keys) != 2 {
		t.Fatalf("Keys = %+v, %v", keys, err)
	}
	if keys[0].ID != "key-a" || keys[0].Status != keycustody.StatusSuperseded || keys[1].ID != "key-b" || keys[1].Status != keycustody.StatusActive {
		t.Fatalf("Keys = %+v, want key-a superseded then key-b active", keys)
	}
	if keys[1].Recipient != "age1bbb" || !keys[1].Created.Equal(t0.Add(time.Hour)) || keys[1].Retired != nil {
		t.Fatalf("key-b round trip = %+v", keys[1])
	}

	// Exports: recorded per key, read back per key.
	exp := keycustody.ExportRecord{KeyID: "key-b", Digest: "sha256:aa", ExportedAt: t0.Add(2 * time.Hour)}
	if err := reg.RecordExport(ctx, exp); err != nil {
		t.Fatalf("RecordExport: %v", err)
	}
	if got, err := reg.Exports(ctx, "key-b"); err != nil || len(got) != 1 || got[0].Digest != exp.Digest || !got[0].ExportedAt.Equal(exp.ExportedAt) {
		t.Fatalf("Exports(b) = %+v, %v", got, err)
	}
	if got, err := reg.Exports(ctx, "key-a"); err != nil || len(got) != 0 {
		t.Fatalf("Exports(a) = %+v, %v, want none", got, err)
	}

	// A confirmation is a proof of possession: it needs a recorded export
	// with the same digest, and repeating it is harmless.
	bad := keycustody.Confirmation{KeyID: "key-b", Digest: "sha256:zz", ConfirmedAt: t0.Add(3 * time.Hour)}
	if err := reg.Confirm(ctx, bad); !errors.Is(err, keycustody.ErrNoSuchExport) {
		t.Fatalf("Confirm without export err = %v, want ErrNoSuchExport", err)
	}
	good := keycustody.Confirmation{KeyID: "key-b", Digest: exp.Digest, ConfirmedAt: t0.Add(3 * time.Hour)}
	for i := 0; i < 2; i++ {
		if err := reg.Confirm(ctx, good); err != nil {
			t.Fatalf("Confirm #%d: %v", i+1, err)
		}
	}
	confirmed, err := reg.Confirmations(ctx)
	if err != nil || len(confirmed) != 1 || confirmed[0].KeyID != "key-b" || confirmed[0].Digest != exp.Digest {
		t.Fatalf("Confirmations = %+v, %v, want the one confirmation", confirmed, err)
	}

	// Restart: everything above is still there through a fresh connection.
	again := reopen()
	if keys, err := again.Keys(ctx); err != nil || len(keys) != 2 || keys[0].Status != keycustody.StatusSuperseded {
		t.Fatalf("after reopen Keys = %+v, %v", keys, err)
	}
	if got, err := again.Exports(ctx, "key-b"); err != nil || len(got) != 1 {
		t.Fatalf("after reopen Exports = %+v, %v", got, err)
	}
	if got, err := again.Confirmations(ctx); err != nil || len(got) != 1 {
		t.Fatalf("after reopen Confirmations = %+v, %v", got, err)
	}
}
