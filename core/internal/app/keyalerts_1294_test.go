package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/secretref"
)

// #1294: ENCRYPTION_KEY_UNRESOLVABLE is raised from the real alert pass, not
// only from its builder's own tests.
//
// Written by reading; not run.

// keyAlertCustody is a custodian the alert pass can ask. It is not the real
// store: keycustody's own tests cover how a missing or unreadable file becomes
// a condition, so this one says "gone" or "back" on demand and records what
// the wiring handed it. What these tests pin is the wiring: which sets are
// asked about, with which provider, what a failed look does, and what the
// dispatcher does with the answers.
type keyAlertCustody struct {
	t *testing.T

	active    keycustody.KeyRecord
	hasActive bool
	activeErr error

	// gone makes AlertConditions report every managed policy it is handed
	// as unresolvable, the way the real store does for a missing key file.
	gone bool

	// forbid fails the test on any call, for the deployments that must never
	// reach custody at all.
	forbid bool

	asked [][]keycustody.SetPolicy
}

func (c *keyAlertCustody) Keys(context.Context) ([]keycustody.KeyRecord, error) { return nil, nil }

func (c *keyAlertCustody) Active(context.Context) (keycustody.KeyRecord, bool, error) {
	if c.forbid {
		c.t.Error("key custody was asked for the active key by a deployment that needs no key alert")
	}
	return c.active, c.hasActive, c.activeErr
}

func (c *keyAlertCustody) AlertConditions(_ context.Context, policies []keycustody.SetPolicy) ([]alert.Condition, []alert.Subject) {
	if c.forbid {
		c.t.Error("key custody was asked for alert conditions by a deployment that needs no key alert")
	}
	c.asked = append(c.asked, policies)
	var out []alert.Condition
	for _, p := range policies {
		if c.gone && p.Required && p.Provider.Managed != "" {
			out = append(out, keycustody.UnresolvableCondition(p.Set, p.Provider.Describe(), "the key file is missing"))
		}
	}
	return out, nil
}

// keyAlertCustodyWithExternals is the same custodian after #1292: it can
// register an external key, which is how the wiring knows a copy under an
// external reference is not refused outright.
type keyAlertCustodyWithExternals struct{ *keyAlertCustody }

func (keyAlertCustodyWithExternals) RegisterExternal(context.Context, secretref.Ref, time.Time) (keycustody.KeyRecord, error) {
	return keycustody.KeyRecord{}, nil
}

func requiredSetWith(t *testing.T, name string, p config.RetainedCopyKeyProvider) config.BackupSet {
	t.Helper()
	bs := testBackupSet(t, t.TempDir())
	bs.Name = name
	bs.ID = mustSetID(t, "production", name)
	bs.RetainedCopyEncryption = config.RetainedCopyEncryption{Mode: config.RetainedCopyEncryptionRequired, KeyProvider: p}
	return bs
}

func keyAlertService(t *testing.T, keys KeyRegistry, sets ...config.BackupSet) (*Service, *recordingSink) {
	t.Helper()
	svc := New(alertingConfig(t, testSource("production", sets...)), openJournal(t), newFakeTransport(), nil)
	svc.Now = fixedNow(epoch)
	svc.Keys = keys
	sink := &recordingSink{}
	if !svc.EnableAlerts(sink) {
		t.Fatal("EnableAlerts returned false for a config that opted in")
	}
	return svc, sink
}

func TestEvaluateAlertsRaisesEncryptionKeyUnresolvableWhenTheManagedKeyIsGone_1294(t *testing.T) {
	bs := requiredSetWith(t, "postgres-primary", config.RetainedCopyKeyProvider{Managed: true})
	keys := &keyAlertCustody{t: t, active: keycustody.KeyRecord{ID: "k0123456789abcdef", Status: keycustody.StatusActive}, hasActive: true, gone: true}
	svc, sink := keyAlertService(t, keys, bs)
	ctx := context.Background()

	svc.AlertTick(ctx)
	svc.AlertTick(ctx)
	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 1 {
		t.Fatalf("ENCRYPTION_KEY_UNRESOLVABLE delivered %d times across two passes of one missing key, want exactly 1 (all: %v)", got, sink.kinds())
	}
	a := sink.delivered[0]
	if a.Scope != bs.ID.String() {
		t.Errorf("Scope = %q, want %q", a.Scope, bs.ID)
	}
	if !strings.Contains(a.Message, "refused") {
		t.Errorf("Message = %q, want the sentence that says copies are being refused", a.Message)
	}
	if len(keys.asked) == 0 || len(keys.asked[0]) != 1 || keys.asked[0][0].Provider.Managed != "k0123456789abcdef" || !keys.asked[0][0].Required {
		t.Fatalf("the wiring handed custody %+v, want one required policy carrying the active key's id", keys.asked)
	}

	// The key comes back: the next pass omits the condition and the dispatcher
	// resolves it, which is observable only by it alerting again on a
	// recurrence.
	keys.gone = false
	svc.AlertTick(ctx)
	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 1 {
		t.Fatalf("delivered %d after the key came back, want still 1", got)
	}
	keys.gone = true
	svc.AlertTick(ctx)
	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 2 {
		t.Fatalf("delivered %d after a recurrence, want 2: the condition was not resolved while the key was back", got)
	}
}

func TestAManagedSetWithNoActiveKeyIsHandedToCustodyAsSuch_1294(t *testing.T) {
	bs := requiredSetWith(t, "postgres-primary", config.RetainedCopyKeyProvider{Managed: true})
	keys := &keyAlertCustody{t: t}
	svc, _ := keyAlertService(t, keys, bs)

	svc.AlertTick(context.Background())

	if len(keys.asked) == 0 || len(keys.asked[0]) != 1 {
		t.Fatalf("custody was handed %+v, want one policy", keys.asked)
	}
	p := keys.asked[0][0]
	if !p.Required || !p.Managed || p.Provider.Managed != "" {
		t.Errorf("policy = %+v, want Required and Managed with no key id, which is what makes the sentence \"no managed key is active\"", p)
	}
}

func TestEvaluateAlertsNeverOpensKeyCustodyWhenNoSetRequiresEncryption_1294(t *testing.T) {
	off := testBackupSet(t, t.TempDir())
	keys := &keyAlertCustody{t: t, forbid: true}
	svc, sink := keyAlertService(t, keys, off)

	svc.AlertTick(context.Background())

	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 0 {
		t.Errorf("a deployment that encrypts nothing delivered %d key alerts", got)
	}
}

func TestAnUnreadableKeyRegistryLeavesTheKeyConditionUnevaluated_1294(t *testing.T) {
	bs := requiredSetWith(t, "postgres-primary", config.RetainedCopyKeyProvider{Managed: true})
	keys := &keyAlertCustody{t: t, active: keycustody.KeyRecord{ID: "k1", Status: keycustody.StatusActive}, hasActive: true, gone: true}
	svc, sink := keyAlertService(t, keys, bs)
	ctx := context.Background()

	svc.AlertTick(ctx)
	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 1 {
		t.Fatalf("setup: delivered %d, want 1", got)
	}

	// The registry cannot be read this pass. Omitting the condition would
	// resolve it and the next good pass would alert a second time about a
	// fact that never changed.
	keys.activeErr = errors.New("database is locked")
	svc.AlertTick(ctx)
	keys.activeErr = nil
	svc.AlertTick(ctx)

	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 1 {
		t.Fatalf("delivered %d after a pass that could not look, want still 1 (a duplicate means the failed look resolved the condition)", got)
	}
}

func TestARequiredSetOnAnExternalReferenceIsAlertedAsRefusedNotSilent_1294(t *testing.T) {
	bs := requiredSetWith(t, "postgres-primary", config.RetainedCopyKeyProvider{External: &config.SecretSource{Env: "RETND_COPY_KEY"}})
	keys := &keyAlertCustody{t: t}
	svc, sink := keyAlertService(t, keys, bs)

	svc.AlertTick(context.Background())

	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 1 {
		t.Fatalf("delivered %d for a required set whose copies are refused for want of external-key support, want 1: silent while refusing is the one combination that is not acceptable (all: %v)", got, sink.kinds())
	}
	m := sink.delivered[0].Message
	for _, want := range []string{"external", "RETND_COPY_KEY", "refused"} {
		if !strings.Contains(m, want) {
			t.Errorf("Message = %q lacks %q", m, want)
		}
	}
}

// The same deployment after #1292: the custodian can register an external key,
// so the wiring says nothing itself and hands the reference to custody, whose
// probe decides. Nothing in alerts.go changes when that lands.
func TestARequiredSetOnAnExternalReferenceIsHandedToTheProbeOnceCustodyCanWriteUnderIt_1294(t *testing.T) {
	bs := requiredSetWith(t, "postgres-primary", config.RetainedCopyKeyProvider{External: &config.SecretSource{Env: "RETND_COPY_KEY"}})
	inner := &keyAlertCustody{t: t}
	svc, sink := keyAlertService(t, keyAlertCustodyWithExternals{inner}, bs)

	svc.AlertTick(context.Background())

	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 0 {
		t.Errorf("delivered %d: with custody able to write under an external key the wiring must not refuse on its own", got)
	}
	if len(inner.asked) == 0 || len(inner.asked[0]) != 1 || inner.asked[0][0].Provider.External.IsZero() {
		t.Fatalf("custody was handed %+v, want one policy carrying the external reference", inner.asked)
	}
}

func TestADisabledOrKopiaSetRaisesNoKeyAlert_1294(t *testing.T) {
	managed := config.RetainedCopyKeyProvider{Managed: true}

	disabled := requiredSetWith(t, "disabled-set", managed)
	disabled.Disabled = true

	kopia := requiredSetWith(t, "uploads-tree", managed)
	kopia.Engine = model.EngineKopia

	keys := &keyAlertCustody{t: t, forbid: true}
	svc, sink := keyAlertService(t, keys, disabled, kopia)

	svc.AlertTick(context.Background())

	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 0 {
		t.Errorf("a disabled set and a kopia set delivered %d key alerts, want 0: the first is never polled and the second's copies are satisfied by the repository", got)
	}
}

func TestARequiredSetWithNoCustodianIsAlertedAsRefused_1294(t *testing.T) {
	bs := requiredSetWith(t, "postgres-primary", config.RetainedCopyKeyProvider{Managed: true})
	svc, sink := keyAlertService(t, nil, bs)

	svc.AlertTick(context.Background())

	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 1 {
		t.Fatalf("delivered %d for a required set with no key custody, want 1: the engine refuses it, so the alert must say so", got)
	}
	if m := sink.delivered[0].Message; !strings.Contains(m, "no key custody") {
		t.Errorf("Message = %q does not say no key custody is configured", m)
	}
}
