package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/keycustody"
)

// The two ends of FR-24's freshness verdict, and the distinction between
// them.
//
// A backup set that has never produced anything is DEGRADED and not STALE,
// and those are different claims: stale says a backup that used to arrive has
// stopped, which is a statement about history, and a set with no history
// cannot support it. Getting that wrong sends an operator looking for a
// regression in a set that has simply never run.
//
// The healthy case runs a real cycle first rather than writing a COMPLETE row,
// so what is asserted is that the pipeline's own output satisfies
// internal/health, not that a fixture does.

// TestBuildHealthReport_NoHistoryIsDegradedNotStale proves BuildHealthReport
// reaches into internal/health correctly for a backup set with no journal
// history at all: FR-24's decideState treats that as DEGRADED (nothing has
// "stopped" if nothing ever started), never HEALTHY (silence is never
// healthy) and never STALE (that would require history to go stale from).
func TestBuildHealthReport_NoHistoryIsDegradedNotStale(t *testing.T) {
	bs := testBackupSet(t, t.TempDir())

	journal := openJournal(t)
	svc := New(testConfig(t, testSource("production", bs)), journal, newFakeTransport(), nil)
	svc.Now = fixedNow(epoch)

	report, err := svc.BuildHealthReport(context.Background(), VersionInfo{BinaryVersion: "1.2.3", RcloneVersion: "v1.75.0"})
	if err != nil {
		t.Fatalf("BuildHealthReport: %v", err)
	}
	if report.Process.BinaryVersion != "1.2.3" || report.Process.RcloneVersion != "v1.75.0" {
		t.Errorf("Process = %+v, want the supplied VersionInfo reflected", report.Process)
	}
	if len(report.BackupSets) != 1 {
		t.Fatalf("BackupSets = %+v, want exactly one", report.BackupSets)
	}
	if got := report.BackupSets[0].State; got != health.Degraded {
		t.Errorf("State = %s, want %s", got, health.Degraded)
	}
}

// TestBuildHealthReport_HealthyAfterACompleteCycle proves a backup set
// that just produced a fresh, complete backup reports HEALTHY.
func TestBuildHealthReport_HealthyAfterACompleteCycle(t *testing.T) {
	localDir := t.TempDir()
	bs := testBackupSet(t, localDir)
	bs.StaleAfter = mustParseDuration(t, "24h")

	tr := newFakeTransport()
	tr.put("backup.dump", "health payload", epoch.Unix())

	journal := openJournal(t)
	svc := New(testConfig(t, testSource("production", bs)), journal, tr, nil)
	svc.Now = fixedNow(epoch)

	svc.RunCycle(context.Background())

	report, err := svc.BuildHealthReport(context.Background(), VersionInfo{})
	if err != nil {
		t.Fatalf("BuildHealthReport: %v", err)
	}
	if len(report.BackupSets) != 1 {
		t.Fatalf("BackupSets = %+v, want exactly one", report.BackupSets)
	}
	if got := report.BackupSets[0].State; got != health.Healthy {
		t.Errorf("State = %s, want %s (reason: %s)", got, health.Healthy, report.BackupSets[0].Reason)
	}
	if report.BackupSets[0].LastSuccessfulPollAt == nil {
		t.Error("LastSuccessfulPollAt = nil, want set: RunCycle just ran against this backup set")
	}
}

func mustParseDuration(t *testing.T, s string) config.Duration {
	t.Helper()
	d, err := time.ParseDuration(s)
	if err != nil {
		t.Fatalf("ParseDuration(%q): %v", s, err)
	}
	return config.Duration(d)
}

// brokenKeys is a key custodian whose registry cannot be read.
type brokenKeys struct{}

func (brokenKeys) Keys(context.Context) ([]keycustody.KeyRecord, error) {
	return nil, errors.New("the key registry is unreadable")
}

func (brokenKeys) Health(context.Context, bool) (health.KeyCustodyHealth, error) {
	return health.KeyCustodyHealth{}, errors.New("the key registry is unreadable")
}

// #993: a broken key store degrades the key-custody section and nothing
// else. The report is still built, so the alert pass behind it (which gives
// up on a report that cannot be built) still runs for every other condition.
func TestBuildHealthReport_AKeyStoreErrorDegradesOnlyTheKeyCustodySection_993(t *testing.T) {
	bs := testBackupSet(t, t.TempDir())
	svc := New(testConfig(t, testSource("production", bs)), openJournal(t), newFakeTransport(), nil)
	svc.Now = fixedNow(epoch)
	svc.Keys = brokenKeys{}

	report, err := svc.BuildHealthReport(context.Background(), VersionInfo{})
	if err != nil {
		t.Fatalf("a key store error failed the whole report: %v", err)
	}
	if len(report.BackupSets) != 1 || report.BackupSets[0].State != health.Degraded {
		t.Errorf("the backup sets were not evaluated: %+v", report.BackupSets)
	}
	kc := report.KeyCustody
	if kc.Unevaluated == "" || !strings.Contains(kc.Unevaluated, "unreadable") {
		t.Errorf("the section does not say it could not be evaluated: %+v", kc)
	}
	if strings.Contains(kc.Unevaluated, "\n") {
		t.Errorf("the reason is not one line: %q", kc.Unevaluated)
	}
}

func TestAStaleBackupStillAlertsWhenTheKeyStoreIsBroken_993(t *testing.T) {
	bs := testBackupSet(t, t.TempDir())
	bs.StaleAfter = mustParseDuration(t, "1h")
	tr := newFakeTransport()
	tr.put("backup.dump", "payload", epoch.Unix())
	svc := New(alertingConfig(t, testSource("production", bs)), openJournal(t), tr, nil)
	svc.Now = fixedNow(epoch)
	svc.Keys = brokenKeys{}
	sink := &recordingSink{}
	if !svc.EnableAlerts(sink) {
		t.Fatal("EnableAlerts returned false for a config that opted in")
	}
	svc.RunCycle(context.Background())
	svc.Now = fixedNow(epoch.Add(48 * time.Hour))
	svc.RunCycle(context.Background())
	if got := sink.countOf(alert.StaleBackup); got != 1 {
		t.Errorf("stale alerts = %d with a broken key store, want 1: the alert pass must not give up on it (all: %v)", got, sink.kinds())
	}
}
