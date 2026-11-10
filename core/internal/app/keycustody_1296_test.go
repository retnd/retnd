package app

import (
	"context"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/metrics"
	"github.com/retnd/retnd/core/internal/model"
)

// #1296: ADR 0028 decision 4 says required on a kopia-engine set is satisfied
// by the repository's own encryption. Health and the metrics gauge have to
// say the same thing the web UI does, and the engine path has to be shown
// never to ask key custody for such a set.
//
// Written by reading; not run.

// noKeyCustody is the custodian of a deployment that turned encryption on and
// has not created a key: no keys, no confirmations. Health is the real rule
// (keycustody.Store.Health) reduced to what this file needs: the verdict
// depends on the enabled flag the caller passes, and the facts are empty.
//
// Active and WithKey fail the test, so it also proves nothing asked it to
// encrypt anything.
type noKeyCustody struct{ t *testing.T }

func (noKeyCustody) Keys(context.Context) ([]keycustody.KeyRecord, error) { return nil, nil }

func (noKeyCustody) Health(_ context.Context, enabled bool) (health.KeyCustodyHealth, error) {
	return health.KeyCustodyHealth{Enabled: enabled}, nil
}

func (c noKeyCustody) Active(context.Context) (keycustody.KeyRecord, bool, error) {
	c.t.Error("key custody was asked for the active key, for a set whose copies are satisfied by the repository")
	return keycustody.KeyRecord{}, false, nil
}

func (c noKeyCustody) WithKey(context.Context, string, func([]byte) error) error {
	c.t.Error("key custody was asked to open a key, for a set whose copies are satisfied by the repository")
	return nil
}

// kopiaSetUnderRequired is an incremental set whose resolved policy is
// required with the managed provider, which is what a deployment-wide
// required resolves a kopia set to.
func kopiaSetUnderRequired(t *testing.T) config.BackupSet {
	t.Helper()
	bs := testBackupSet(t, t.TempDir())
	bs.Name = "uploads-tree"
	bs.ID = mustSetID(t, "production", "uploads-tree")
	bs.Engine = model.EngineKopia
	bs.Repository = model.RepositoryRef{Domain: "production", Set: bs.ID}
	bs.SourceIdentity = "1e3f0c2b4a5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f7"
	bs.LocalPath = ""
	bs.Include = nil
	bs.Completion = config.Completion{}
	bs.RetainedCopyEncryption = managed()
	return bs
}

func requiredCopySet(t *testing.T) config.BackupSet {
	t.Helper()
	bs := testBackupSet(t, t.TempDir())
	bs.RetainedCopyEncryption = managed()
	return bs
}

func keyCustodyOf(t *testing.T, svc *Service) health.KeyCustodyHealth {
	t.Helper()
	report, err := svc.BuildHealthReport(context.Background(), VersionInfo{})
	if err != nil {
		t.Fatalf("BuildHealthReport: %v", err)
	}
	return report.KeyCustody
}

func TestAKopiaOnlyDeploymentUnderRequiredIsNotDegradedKeyCustody_1296(t *testing.T) {
	svc := New(testConfig(t, testSource("production", kopiaSetUnderRequired(t))), openJournal(t), newFakeTransport(), nil)
	svc.Now = fixedNow(epoch)
	svc.Keys = noKeyCustody{t: t}

	kc := keyCustodyOf(t, svc)
	if kc.Enabled {
		t.Error("KeyCustody.Enabled is true for a deployment whose only set is a kopia set, a set no managed key will ever encrypt anything for")
	}
	if kc.Degraded() {
		t.Error("KeyCustody.Degraded() is true (\"no key can encrypt yet\") for a deployment in which no managed key will ever encrypt anything")
	}
}

func TestAddingACopySetUnderRequiredMakesKeyCustodyEnabledAndDegraded_1296(t *testing.T) {
	svc := New(testConfig(t, testSource("production", kopiaSetUnderRequired(t), requiredCopySet(t))), openJournal(t), newFakeTransport(), nil)
	svc.Now = fixedNow(epoch)
	svc.Keys = noKeyCustody{t: t}

	kc := keyCustodyOf(t, svc)
	if !kc.Enabled {
		t.Error("KeyCustody.Enabled is false with a copy-engine set under required; the kopia exclusion must not hide a set a managed key does guard")
	}
	if !kc.Degraded() {
		t.Error("KeyCustody.Degraded() is false with a required copy-engine set and no key, which is the control for the kopia-only case")
	}
}

func TestTheEncryptionGaugesReadZeroForAKopiaOnlyDeploymentUnderRequired_1296(t *testing.T) {
	scrape := func(sets ...config.BackupSet) string {
		svc := New(testConfig(t, testSource("production", sets...)), openJournal(t), newFakeTransport(), nil)
		svc.Now = fixedNow(epoch)
		svc.Keys = noKeyCustody{t: t}
		report, err := svc.BuildHealthReport(context.Background(), VersionInfo{})
		if err != nil {
			t.Fatalf("BuildHealthReport: %v", err)
		}
		return metrics.Render(report)
	}

	kopiaOnly := scrape(kopiaSetUnderRequired(t))
	if strings.Contains(kopiaOnly, "retnd_retained_copy_encryption_enabled 1") || strings.Contains(kopiaOnly, "retnd_retained_copy_encryption_degraded 1") {
		t.Errorf("the scrape says encryption is on or degraded for a kopia-only deployment:\n%s", kopiaOnly)
	}

	// The control: the same gauges do read 1 when a copy set is required.
	both := scrape(kopiaSetUnderRequired(t), requiredCopySet(t))
	for _, want := range []string{"retnd_retained_copy_encryption_enabled 1", "retnd_retained_copy_encryption_degraded 1"} {
		if !strings.Contains(both, want+"\n") {
			t.Errorf("with a required copy set the scrape lacks %q:\n%s", want, both)
		}
	}
}

func TestARequiredKopiaSetNeverAsksKeyCustody_1296(t *testing.T) {
	kopia := kopiaSetUnderRequired(t)
	cfg := testConfig(t, testSource("production", kopia))
	cfg.IncrementalEngine.Enabled = false

	svc := New(cfg, openJournal(t), newFakeTransport(), nil)
	svc.Now = fixedNow(epoch)
	svc.Repositories = poisonEngine{t: t}
	svc.Keys = noKeyCustody{t: t}

	report := svc.RunCycle(context.Background())

	if len(report.Sets) != 1 || report.Sets[0].Err == nil {
		t.Fatalf("the cycle did not take the incremental path and refuse at the gate (sets: %+v); without that this test says nothing about the engine path", report.Sets)
	}
	// noKeyCustody fails the test on Active or WithKey, which is the whole
	// assertion: a kopia set returns into processIncrementalSet before the
	// per-artifact walk, so the copy path's activeKey is never reached.
}
