package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/model"
)

// #1304: a dropped external reference reaches the alert pass and the health
// report. Written by reading; not run.

// riskCustody is a custodian that also knows which external keys have lost
// every reference. It embeds the #1294 double, so the required-set path is
// unchanged.
type riskCustody struct {
	*keyAlertCustody
	risks []health.ExternalKeyAtRisk
	err   error
}

func (c *riskCustody) LastExternalKeysAtRisk() []health.ExternalKeyAtRisk { return c.risks }

func (c *riskCustody) ExternalKeysAtRisk(context.Context) ([]health.ExternalKeyAtRisk, error) {
	return c.risks, c.err
}

// healthCustody answers the health section's two questions.
type healthCustody struct {
	t     *testing.T
	risks []health.ExternalKeyAtRisk
}

func (c *healthCustody) Keys(context.Context) ([]keycustody.KeyRecord, error) { return nil, nil }

func (c *healthCustody) Health(context.Context, bool) (health.KeyCustodyHealth, error) {
	return health.KeyCustodyHealth{}, nil
}

// ExternalKeysAtRisk resolves references, which a health build must never do
// (it runs on every GET /health).
func (c *healthCustody) ExternalKeysAtRisk(context.Context) ([]health.ExternalKeyAtRisk, error) {
	c.t.Error("a health build resolved external key references")
	return nil, nil
}

// LastExternalKeysAtRisk is the last probe's result, read without resolving.
func (c *healthCustody) LastExternalKeysAtRisk() []health.ExternalKeyAtRisk { return c.risks }

func atRisk(set string, n int) health.ExternalKeyAtRisk {
	r := health.ExternalKeyAtRisk{KeyID: "x0123456789abcdef", Reason: "no external reference is declared"}
	for i := 0; i < n; i++ {
		r.Copies = append(r.Copies, model.CopyRef{Artifact: model.ArtifactID{Set: mustSetID2(set), Name: "a" + string(rune('0'+i)) + ".dump"}, Medium: "warm"})
	}
	return r
}

func mustSetID2(set string) model.BackupSetID {
	return model.BackupSetID{Source: "production", Set: set}
}

func TestADroppedExternalReferenceRaisesTheKeyAlertEvenWhenNoSetRequiresEncryption_1304(t *testing.T) {
	off := testBackupSet(t, t.TempDir())
	off.Name = "postgres-primary"
	off.ID = mustSetID(t, "production", "postgres-primary")
	keys := &riskCustody{keyAlertCustody: &keyAlertCustody{t: t, forbid: true}, risks: []health.ExternalKeyAtRisk{atRisk("postgres-primary", 2)}}
	svc, sink := keyAlertService(t, keys, off)
	ctx := context.Background()

	svc.AlertTick(ctx)
	svc.AlertTick(ctx)

	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 1 {
		t.Fatalf("delivered %d, want exactly 1 across two passes of one standing fact (all: %v)", got, sink.kinds())
	}
	a := sink.delivered[0]
	if a.Scope != off.ID.String() {
		t.Errorf("Scope = %q, want the set the copies belong to, %q", a.Scope, off.ID)
	}
	for _, want := range []string{"x0123456789abcdef", "a0.dump", "a1.dump", "warm", "cannot be read"} {
		if !strings.Contains(a.Message, want) {
			t.Errorf("Message = %q lacks %q", a.Message, want)
		}
	}

	// The reference comes back: resolved. A recurrence alerts again.
	keys.risks = nil
	svc.AlertTick(ctx)
	keys.risks = []health.ExternalKeyAtRisk{atRisk("postgres-primary", 2)}
	svc.AlertTick(ctx)
	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 2 {
		t.Fatalf("delivered %d after a recurrence, want 2", got)
	}
}

func TestAnExternalKeyRiskLookThatFailedLeavesTheConditionStanding_1304(t *testing.T) {
	bs := testBackupSet(t, t.TempDir())
	bs.Name = "postgres-primary"
	bs.ID = mustSetID(t, "production", "postgres-primary")
	keys := &riskCustody{keyAlertCustody: &keyAlertCustody{t: t}, risks: []health.ExternalKeyAtRisk{atRisk("postgres-primary", 1)}}
	svc, sink := keyAlertService(t, keys, bs)
	ctx := context.Background()

	svc.AlertTick(ctx)
	keys.err = errors.New("database is locked")
	svc.AlertTick(ctx)
	keys.err = nil
	svc.AlertTick(ctx)

	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 1 {
		t.Fatalf("delivered %d, want still 1: a pass that could not look resolved a standing condition", got)
	}
}

func TestACopyOfADisabledSetIsNotAlertedAboutAndASetAlreadyAlertedIsNotAlertedTwice_1304(t *testing.T) {
	managed := config.RetainedCopyKeyProvider{Managed: true}
	disabled := requiredSetWith(t, "disabled-set", managed)
	disabled.Disabled = true
	req := requiredSetWith(t, "postgres-primary", managed)
	keys := &riskCustody{
		keyAlertCustody: &keyAlertCustody{t: t, active: keycustody.KeyRecord{ID: "k0123456789abcdef", Status: keycustody.StatusActive}, hasActive: true, gone: true},
		risks:           []health.ExternalKeyAtRisk{atRisk("disabled-set", 1), atRisk("postgres-primary", 1)},
	}
	svc, sink := keyAlertService(t, keys, disabled, req)

	svc.AlertTick(context.Background())

	// One alert: the required set's own (its key is gone), which already says
	// copies are refused. The disabled set is never polled.
	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 1 {
		t.Fatalf("delivered %d, want 1 (all: %v)", got, sink.kinds())
	}
	if sink.delivered[0].Scope != req.ID.String() {
		t.Errorf("Scope = %q, want %q", sink.delivered[0].Scope, req.ID)
	}
}

func TestKeyCustodyHealthCarriesTheExternalKeysNoReferenceYields_1304(t *testing.T) {
	bs := testBackupSet(t, t.TempDir())
	keys := &healthCustody{t: t, risks: []health.ExternalKeyAtRisk{atRisk("postgres-primary", 1)}}
	svc := New(alertingConfig(t, testSource("production", bs)), openJournal(t), newFakeTransport(), nil)
	svc.Keys = keys

	got, err := svc.keyCustodyHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ExternalKeysAtRisk) != 1 || got.ExternalKeysAtRisk[0].KeyID != "x0123456789abcdef" || !got.Degraded() {
		t.Errorf("health = %+v, want the key listed and the section degraded", got)
	}
}
