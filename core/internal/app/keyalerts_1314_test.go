package app

import (
	"context"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/secretref"
)

// EPIC T, T1.4 (#1314): a pinned suite the external key is not a key of is
// refused by the engine, and the alert pass says why. Written by reading;
// not run.

// keyAlertCustodyWithSuites can register an external key and say which suite
// a reference's key belongs to, as service.KeyCustody does.
type keyAlertCustodyWithSuites struct {
	keyAlertCustodyWithExternals
	suite string
}

func (c keyAlertCustodyWithSuites) ExternalKeySuite(context.Context, secretref.Ref) (string, error) {
	return c.suite, nil
}

func pinnedExternalSet(t *testing.T, suite string) config.BackupSet {
	t.Helper()
	bs := requiredSetWith(t, "postgres-primary", config.RetainedCopyKeyProvider{External: &config.SecretSource{File: "/run/secrets/retained-copy-key"}})
	bs.RetainedCopyEncryption.Suite = suite
	return bs
}

func TestAnExternalKeyOfAnotherSuiteRaisesTheReason_1314(t *testing.T) {
	bs := pinnedExternalSet(t, "age-pq/v1")
	keys := keyAlertCustodyWithSuites{keyAlertCustodyWithExternals{&keyAlertCustody{t: t}}, "age/v1"}
	svc, sink := keyAlertService(t, keys, bs)

	svc.AlertTick(context.Background())

	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 1 {
		t.Fatalf("delivered %d ENCRYPTION_KEY_UNRESOLVABLE for an external key of the wrong suite, want 1 (all: %v)", got, sink.kinds())
	}
	if msg := sink.delivered[0].Message; !strings.Contains(msg, "pins suite age-pq/v1") || !strings.Contains(msg, "age/v1 key") {
		t.Errorf("the alert does not say which suites disagree: %q", msg)
	}
}

func TestAnExternalKeyOfThePinnedSuiteIsHandedToCustodyAsBefore_1314(t *testing.T) {
	bs := pinnedExternalSet(t, "age-pq/v1")
	inner := &keyAlertCustody{t: t}
	keys := keyAlertCustodyWithSuites{keyAlertCustodyWithExternals{inner}, "age-pq/v1"}
	svc, sink := keyAlertService(t, keys, bs)

	svc.AlertTick(context.Background())

	if got := sink.countOf(alert.EncryptionKeyUnresolvable); got != 0 {
		t.Fatalf("a matching external key raised %d alert(s)", got)
	}
	if len(inner.asked) == 0 || len(inner.asked[0]) != 1 || inner.asked[0][0].Provider.External.IsZero() {
		t.Fatalf("custody was handed %+v, want the external policy to probe", inner.asked)
	}
}
