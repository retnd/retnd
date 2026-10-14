package webhost

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/retnd/retnd/core/service"
)

// #1152: `retnd ... --json` prints what core/service's *Wire functions build,
// and this file is where each of them is held to what the API serves.
//
// core may not import this package, so the diff lives here. For each read the
// REAL handler is driven over the fake backend and its body is compared, as
// decoded JSON, with the CLI's mapping of the same read model. Comparing
// decoded values rather than bytes is deliberate: key order and indentation
// are not part of the contract, while which keys are present, which are null
// and which are absent are, and a decoded map keeps exactly those.
//
// Every case runs twice, over a read model with every optional field set and
// over one with every optional field absent, because the drift this guards
// against is an `omitempty` on one side only.

func decodedMap(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	return v
}

// wireBody is the CLI mapping, marshalled and decoded the way the handler's
// body is.
func wireBody(t *testing.T, wire any) any {
	t.Helper()
	b, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return decodedMap(t, b)
}

// sameAsTheAPI fails when the CLI's body is not the handler's body, and when
// the comparison would pass only because both were nearly empty.
func sameAsTheAPI(t *testing.T, what string, api []byte, cli any, minKeys int) {
	t.Helper()
	got, want := wireBody(t, cli), decodedMap(t, api)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: the CLI's --json differs from the API body\napi: %s\ncli: %v", what, api, got)
	}
	if m, ok := want.(map[string]any); ok && len(m) < minKeys {
		t.Errorf("%s: the API body has only %d keys; a comparison of two near-empty objects proves nothing:\n%s", what, len(m), api)
	}
}

func readWireAt() time.Time { return time.Date(2026, 10, 1, 12, 30, 0, 123456789, time.UTC) }

func fullSnapshot() service.Snapshot {
	at := readWireAt()
	done := at.Add(6 * time.Minute)
	del := at.Add(time.Hour)
	dur := 6*time.Minute + 700*time.Millisecond
	n := func(v int64) *int64 { return &v }
	yes := true
	return service.Snapshot{
		RunID: "run_a", SnapshotID: "k1a2b3", BackupSetID: "src/set-1", OperationID: "op_1",
		Engine: "kopia", RepositoryDomain: "production", Phase: "SUCCESS", Succeeded: true, LastKnownGood: true,
		SourceConsistency: "external_snapshot", VerificationStatus: "passed", VerificationLevel: "content_sample",
		VerificationAchieved: "content_sample",
		EntriesScanned:       n(1300), Files: n(1204), Directories: n(58), LogicalBytes: n(9_000_000),
		SourceBytesRead: n(8_100_000), RepositoryBytesWritten: n(2_300_000), ContentReusedBytes: n(5_800_000),
		SourceComplete: &yes, Reason: "because", StartedAt: at, CompletedAt: &done, Duration: &dur, DeleteRequestedAt: &del,
		Holds: []service.SnapshotHold{fullHold()},
	}
}

func sparseSnapshot() service.Snapshot {
	return service.Snapshot{
		RunID: "run_b", BackupSetID: "src/set-1", Engine: "kopia", RepositoryDomain: "production",
		Phase: "PENDING", SourceConsistency: "external_snapshot", VerificationLevel: "content_sample",
		StartedAt: readWireAt(),
	}
}

func fullHold() service.SnapshotHold {
	at := readWireAt()
	rel := at.Add(time.Hour)
	return service.SnapshotHold{
		HoldID: "hold_1", RunID: "run_a", BackupSetID: "src/set-1", Reason: "legal", PlacedBy: "ticket-1",
		PlacedAt: at, ReleasedAt: &rel, ReleasedBy: "ticket-2", Active: false,
	}
}

func activeHold() service.SnapshotHold {
	return service.SnapshotHold{
		HoldID: "hold_2", RunID: "run_b", BackupSetID: "src/set-1", Reason: "investigation", PlacedBy: "ticket-3",
		PlacedAt: readWireAt(), Active: true,
	}
}

func TestIssue1152_SnapshotListWireIsTheHandlersListFieldForField(t *testing.T) {
	for name, snaps := range map[string][]service.Snapshot{
		"every optional field set":    {fullSnapshot()},
		"every optional field absent": {sparseSnapshot()},
		"both":                        {fullSnapshot(), sparseSnapshot()},
		"none":                        nil,
	} {
		t.Run(name, func(t *testing.T) {
			rs := newReadSurfaceRouter(t)
			snapshotsOf(rs.backend).snapshots = snaps
			rec := rs.get(t, "/api/v1/backup-sets/src/set-1/snapshots")
			mustStatus(t, rec, 200)
			sameAsTheAPI(t, "snapshot list", rec.Body.Bytes(), service.SnapshotsWire(snaps), 1)
		})
	}
}

func TestIssue1152_SnapshotDetailWireIsTheHandlersDetailFieldForField(t *testing.T) {
	at := readWireAt()
	for name, d := range map[string]service.SnapshotDetail{
		"every optional field set": {Snapshot: fullSnapshot(), Transitions: []service.SnapshotTransition{
			{From: "", To: "PENDING", At: at, Detail: "started"},
			{From: "PENDING", To: "SOURCE_SCAN", At: at.Add(time.Minute)},
		}},
		"every optional field absent": {Snapshot: sparseSnapshot()},
	} {
		t.Run(name, func(t *testing.T) {
			rs := newReadSurfaceRouter(t)
			snapshotsOf(rs.backend).detail = d
			rec := rs.get(t, "/api/v1/backup-sets/src/set-1/snapshots/"+d.Snapshot.RunID)
			mustStatus(t, rec, 200)
			sameAsTheAPI(t, "snapshot detail", rec.Body.Bytes(), service.SnapshotDetailWire(d), 2)
		})
	}
}

func TestIssue1152_SnapshotHoldsWireIsTheHandlersListFieldForField(t *testing.T) {
	for name, holds := range map[string][]service.SnapshotHold{
		"released and active": {fullHold(), activeHold()},
		"none":                nil,
	} {
		t.Run(name, func(t *testing.T) {
			rs := newReadSurfaceRouter(t)
			snapshotsOf(rs.backend).holds = holds
			rec := rs.get(t, "/api/v1/backup-sets/src/set-1/holds")
			mustStatus(t, rec, 200)
			sameAsTheAPI(t, "snapshot holds", rec.Body.Bytes(), service.SnapshotHoldsWire(holds), 1)
		})
	}
}

func TestIssue1152_SnapshotRetentionWireIsTheHandlersPreviewFieldForField(t *testing.T) {
	at := readWireAt()
	for name, p := range map[string]service.SnapshotRetentionPreview{
		"every optional field set": {GeneratedAt: at, Verdicts: []service.SnapshotRetentionVerdict{{
			RunID: "run_a", SnapshotID: "k1", Action: "KEEP", StartedAt: at, Reason: "kept",
			Tiers: []service.SnapshotRetentionTier{{Tier: "daily", SelectedBy: "NEWEST"}, {Tier: "weekly"}},
			Holds: []service.SnapshotHold{activeHold()}, HoldReason: "set-wide",
		}}},
		"every optional field absent": {GeneratedAt: at, Verdicts: []service.SnapshotRetentionVerdict{{
			RunID: "run_b", Action: "DELETE", StartedAt: at, Reason: "nothing keeps it",
		}}},
		"none": {GeneratedAt: at},
	} {
		t.Run(name, func(t *testing.T) {
			rs := newReadSurfaceRouter(t)
			snapshotsOf(rs.backend).retention = p
			rec := rs.get(t, "/api/v1/backup-sets/src/set-1/snapshot-retention")
			mustStatus(t, rec, 200)
			sameAsTheAPI(t, "snapshot retention", rec.Body.Bytes(), service.SnapshotRetentionWire(p), 2)
		})
	}
}

func fullRepository() service.RepositoryHealth {
	at := readWireAt()
	skew := -90*time.Second - 400*time.Millisecond
	return service.RepositoryHealth{
		Domain: "production", MayShare: true, BackupSets: []string{"src/set-1", "src/set-2"}, State: "DEGRADED",
		Reachable: true, Readable: true, Writable: false, CredentialsValid: true, ClockSane: false, ClockSkew: &skew,
		LastMaintenanceAt: at, LastMaintenanceResult: "ok", MaintenanceOverdue: true,
		LastSnapshotAt: at.Add(time.Hour), LastSnapshotStatus: "SUCCESS",
		LastVerificationAt: at.Add(2 * time.Hour), LastVerificationStatus: "passed", Detail: "writes are refused",
	}
}

func sparseRepository() service.RepositoryHealth {
	return service.RepositoryHealth{Domain: "offsite", State: "FAILING"}
}

func TestIssue1152_RepositoriesWireIsTheHandlersListFieldForField(t *testing.T) {
	for name, report := range map[string]service.RepositoryHealthReport{
		"every optional field set":    {GeneratedAt: readWireAt(), Repositories: []service.RepositoryHealth{fullRepository()}},
		"every optional field absent": {GeneratedAt: readWireAt(), Repositories: []service.RepositoryHealth{sparseRepository()}},
		"none":                        {GeneratedAt: readWireAt()},
	} {
		t.Run(name, func(t *testing.T) {
			rs := newReadSurfaceRouter(t)
			snapshotsOf(rs.backend).repos = report
			rec := rs.get(t, "/api/v1/repositories")
			mustStatus(t, rec, 200)
			sameAsTheAPI(t, "repositories", rec.Body.Bytes(), service.RepositoriesWire(report), 2)
		})
	}
}

func TestIssue1152_RepositoryMaintenanceWireIsTheHandlersObjectFieldForField(t *testing.T) {
	at := readWireAt()
	for name, m := range map[string]service.RepositoryMaintenance{
		"every optional field set": {
			Domain: "production", Owner: "deployment-a", LastQuickAt: at, LastFullAt: at.Add(time.Hour),
			NextEligibleAt: at.Add(2 * time.Hour), Due: true, DueMode: "full", DueReason: "never ran",
			Overdue: true, Runs: 7, Failures: 2, ReclaimedBytes: 1 << 20, Failing: true,
		},
		"every optional field absent": {Domain: "offsite"},
	} {
		t.Run(name, func(t *testing.T) {
			rs := newReadSurfaceRouter(t)
			snapshotsOf(rs.backend).maintenance = m
			rec := rs.get(t, "/api/v1/repositories/"+m.Domain+"/maintenance")
			mustStatus(t, rec, 200)
			sameAsTheAPI(t, "repository maintenance", rec.Body.Bytes(), service.RepositoryMaintenanceWire(m), 8)
		})
	}
}
