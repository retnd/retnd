package keycustody_test

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/health"
)

// FR-66's durable half: managed encryption enabled with no confirmed
// bundle is a degraded fact that survives a restart, and it is health, not
// an alert, because only a human can clear it.

func TestEnablingManagedEncryptionWithNoConfirmedBundleIsDurablyDegraded(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	s := openStore(t, root)
	rec, err := s.Create(ctx, fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	h, err := s.Health(ctx, true)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !h.Enabled || h.ActiveKeyID != rec.ID || h.BundleConfirmed || !h.Degraded() || h.OK() {
		t.Fatalf("Health with a key and no bundle = %+v, want enabled, the active id, unconfirmed and degraded", h)
	}
	if len(h.UnconfirmedKeyIDs) != 1 || h.UnconfirmedKeyIDs[0] != rec.ID {
		t.Fatalf("UnconfirmedKeyIDs = %v, want exactly the active key", h.UnconfirmedKeyIDs)
	}

	// Still present after a restart.
	restarted := openStore(t, root)
	h2, err := restarted.Health(ctx, true)
	if err != nil {
		t.Fatalf("Health after restart: %v", err)
	}
	if !h2.Degraded() {
		t.Fatalf("the degraded state did not survive a restart: %+v", h2)
	}

	// Confirming the bundle clears it.
	var out bytes.Buffer
	receipt, err := restarted.Export(ctx, rec.ID, &out, fixedNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := restarted.Confirm(ctx, rec.ID, receipt.Digest, fixedNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	h3, err := openStore(t, root).Health(ctx, true)
	if err != nil {
		t.Fatalf("Health after confirm and restart: %v", err)
	}
	if h3.Degraded() || !h3.OK() || !h3.BundleConfirmed || len(h3.UnconfirmedKeyIDs) != 0 {
		t.Fatalf("Health after a confirmed bundle = %+v, want not degraded", h3)
	}
}

// A rotation mints a key nobody has a bundle for, so the deployment is
// degraded again until THAT key is exported and confirmed.
func TestARotatedKeyReopensTheDegradedStateUntilItsOwnBundleIsConfirmed(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, t.TempDir())
	first, err := s.Create(ctx, fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var out bytes.Buffer
	r1, err := s.Export(ctx, first.ID, &out, fixedNow)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := s.Confirm(ctx, first.ID, r1.Digest, fixedNow); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	second, err := s.Rotate(ctx, fixedNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	h, err := s.Health(ctx, true)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !h.Degraded() || h.ActiveKeyID != second.ID {
		t.Fatalf("Health after rotation = %+v, want degraded with the new key active", h)
	}
	if len(h.UnconfirmedKeyIDs) != 1 || h.UnconfirmedKeyIDs[0] != second.ID {
		t.Fatalf("UnconfirmedKeyIDs = %v, want only the rotated-in key", h.UnconfirmedKeyIDs)
	}
}

// The degraded state is ABSENT on a deployment with encryption off, so the
// zero value is not doing the work: a key store that exists with keys in it
// but with no set requiring encryption is not degraded.
func TestTheDegradedStateIsAbsentWhenEncryptionIsOff(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, t.TempDir())
	if _, err := s.Create(ctx, fixedNow); err != nil {
		t.Fatalf("Create: %v", err)
	}
	h, err := s.Health(ctx, false)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Enabled || h.Degraded() || !h.OK() {
		t.Fatalf("Health with encryption off = %+v, want not enabled and not degraded", h)
	}
	if h.ActiveKeyID == "" {
		t.Fatalf("Health with encryption off hides the key that exists: the section reports facts, the verdict is what depends on enablement")
	}
}

// The zero value reads "this deployment encrypts nothing", exactly as
// Report.Workflow's does, which is what lets Report grow the section with
// no existing call site changing.
func TestTheZeroKeyCustodyHealthEncryptsNothing(t *testing.T) {
	var zero health.KeyCustodyHealth
	if zero.Enabled || zero.Degraded() || !zero.OK() || zero.ActiveKeyID != "" || zero.BundleConfirmed {
		t.Fatalf("zero KeyCustodyHealth = %+v, want the honest reading for a deployment that encrypts nothing", zero)
	}
	var report health.Report
	if !reflect.DeepEqual(report.KeyCustody, zero) {
		t.Fatalf("Report.KeyCustody zero value = %+v, want %+v", report.KeyCustody, zero)
	}
	if _, ok := reflect.TypeOf(report).FieldByName("KeyCustody"); !ok {
		t.Fatalf("health.Report has no KeyCustody section")
	}
}

// Enabled with no key at all is degraded too: the operator switched
// encryption on and nothing can encrypt yet, which a surface must say
// rather than reading as fine.
func TestEnabledWithNoKeyIsDegradedAndSaysSo(t *testing.T) {
	s := openStore(t, t.TempDir())
	h, err := s.Health(context.Background(), true)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !h.Enabled || h.ActiveKeyID != "" || !h.Degraded() {
		t.Fatalf("Health enabled with no key = %+v, want degraded with no active key", h)
	}
}
