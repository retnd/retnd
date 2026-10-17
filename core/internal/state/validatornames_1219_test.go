package state

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
)

// #1219: the validator scans need artifact names, newest first, and the
// newest recovery test recorded under each validator. They used to get both
// by loading every record with every placement.

func namesFixture(t *testing.T, names ...string) (*Journal, model.BackupSetID) {
	t.Helper()
	j, _ := openJournal(t)
	set := testArtifact(t).Set
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i, name := range names {
		// Discovered out of the order they were created in, so "newest
		// first" cannot be satisfied by the primary key.
		driveOneArtifact(t, j, set, name, base.Add(time.Duration((i*7)%len(names))*time.Hour))
	}
	return j, set
}

func recordTestUnder(t *testing.T, j *Journal, set model.BackupSetID, name, validator, st string, at time.Time) {
	t.Helper()
	a, err := model.NewArtifactID(set, name)
	if err != nil {
		t.Fatal(err)
	}
	rt := passingTest(CopyRef{Artifact: a, Medium: MediumLocal})
	rt.ValidatorID, rt.At, rt.NextDue = validator, at, at.Add(time.Hour)
	if st == RecoveryFailed {
		rt.State, rt.DigestMatched, rt.Termination, rt.Reason = RecoveryFailed, yes(), RecoveryTerminationExited, "exit 1"
	}
	if err := j.RecordRecoveryTest(context.Background(), rt); err != nil {
		t.Fatalf("RecordRecoveryTest(%s): %v", name, err)
	}
}

// oracle is the old derivation: the whole records, stably sorted newest first.
func oracleNames(t *testing.T, j *Journal, set model.BackupSetID) []string {
	t.Helper()
	records, err := j.ListByBackupSet(context.Background(), set)
	if err != nil {
		t.Fatal(err)
	}
	sort.SliceStable(records, func(i, k int) bool { return records[i].DiscoveredAt.After(records[k].DiscoveredAt) })
	var out []string
	for _, r := range records {
		out = append(out, r.Artifact.Name)
	}
	return out
}

func TestNamesAndLastRecoveryTests_NamesAreNewestFirstAndEqualWhatTheRecordsSay(t *testing.T) {
	j, set := namesFixture(t, "a.dump", "b.dump", "c.dump", "d.dump", "e.dump")
	other, _ := model.NewBackupSetID("staging", "elsewhere")
	driveOneArtifact(t, j, other, "z.dump", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))

	got, err := j.NamesAndLastRecoveryTests(context.Background(), set)
	if err != nil {
		t.Fatal(err)
	}
	if want := oracleNames(t, j, set); !reflect.DeepEqual(got.Names, want) {
		t.Errorf("names = %v, want %v", got.Names, want)
	}
	if len(got.Names) != 5 {
		t.Errorf("another set's artifact leaked in: %v", got.Names)
	}
	if len(got.LastTests) != 0 {
		t.Errorf("nothing was tested but LastTests = %+v", got.LastTests)
	}
}

func TestNamesAndLastRecoveryTests_TheNewestTestPerValidatorWins(t *testing.T) {
	j, set := namesFixture(t, "a.dump", "b.dump", "c.dump")
	t0 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	recordTestUnder(t, j, set, "a.dump", "manifest.local.sh", RecoveryPassed, t0)
	recordTestUnder(t, j, set, "b.dump", "manifest.local.sh", RecoveryFailed, t0.Add(time.Hour))
	recordTestUnder(t, j, set, "c.dump", "dumps.local.sh", RecoveryPassed, t0.Add(-time.Hour))

	got, err := j.NamesAndLastRecoveryTests(context.Background(), set)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.LastTests["manifest.local.sh"]
	if !ok || m.State != RecoveryFailed || !m.At.Equal(t0.Add(time.Hour)) || m.Artifact.Name != "b.dump" || m.Artifact.Set != set {
		t.Errorf("manifest = %+v", m)
	}
	d, ok := got.LastTests["dumps.local.sh"]
	if !ok || d.State != RecoveryPassed || d.Artifact.Name != "c.dump" {
		t.Errorf("dumps = %+v", d)
	}
	if len(got.LastTests) != 2 {
		t.Errorf("LastTests = %+v", got.LastTests)
	}
}

func TestNamesAndLastRecoveryTests_AnEmptySetIsEmptyAndNotAnError(t *testing.T) {
	j, _ := openJournal(t)
	set, _ := model.NewBackupSetID("production", "nothing-yet")
	got, err := j.NamesAndLastRecoveryTests(context.Background(), set)
	if err != nil || len(got.Names) != 0 || len(got.LastTests) != 0 {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestNamesAndLastRecoveryTests_ReadsFarLessThanTheRecordsDo(t *testing.T) {
	names := make([]string, 120)
	for i := range names {
		names[i] = fmt.Sprintf("backup-%03d.dump", i)
	}
	j, set := namesFixture(t, names...)
	ctx := context.Background()
	records := testing.AllocsPerRun(5, func() {
		if _, err := j.ListByBackupSet(ctx, set); err != nil {
			t.Fatal(err)
		}
	})
	lean := testing.AllocsPerRun(5, func() {
		if _, err := j.NamesAndLastRecoveryTests(ctx, set); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("allocations for %d artifacts: ListByBackupSet %.0f, NamesAndLastRecoveryTests %.0f", len(names), records, lean)
	if lean*4 > records {
		t.Errorf("the names-only read made %.0f allocations against %.0f for the whole records; it should be a fraction of them", lean, records)
	}
}

func benchmarkNames(b *testing.B, lean bool) {
	b.Helper()
	j, _ := openJournal(b)
	set := testArtifact(b).Set
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 200; i++ {
		driveOneArtifact(b, j, set, fmt.Sprintf("backup-%03d.dump", i), base.Add(time.Duration(i)*time.Minute))
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if lean {
			if _, err := j.NamesAndLastRecoveryTests(ctx, set); err != nil {
				b.Fatal(err)
			}
		} else if _, err := j.ListByBackupSet(ctx, set); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidatorScan_NamesOnly(b *testing.B)    { benchmarkNames(b, true) }
func BenchmarkValidatorScan_WholeRecords(b *testing.B) { benchmarkNames(b, false) }
