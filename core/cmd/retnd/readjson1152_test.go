package main

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// Issue #1152 (EPIC S, S2.2). Most read verbs printed text only, so a script
// and the UI echo had no wire object to parse. These specs drive the real
// CLI over a real configuration and journal and hold what `--json` prints to
// the contract's own types.
//
// Two checks are used throughout, and both are there because a decode that
// merely succeeds proves very little:
//
//   - a strict decode (unknown fields refused), so a key the contract does
//     not name fails here instead of being dropped by a lenient decoder;
//   - a look at the raw keys, because the contract's always-sent fields
//     (a nullable counter, an empty list) are present-and-null or
//     present-and-empty on the wire, and a struct decode cannot tell those
//     apart from absent.

// cliJSON runs the CLI and returns what it wrote to stdout and its exit code.
func cliJSON(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var code int
	out := captureStdout(t, func() { code = run(args) })
	return out, code
}

// strictDecode decodes exactly one JSON document into v and refuses unknown
// fields and trailing text, so a verb that prints a table, or a document and
// then a footer, fails here.
func strictDecode(t *testing.T, out string, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("stdout is not one contract document: %v\n%s", err, out)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("stdout carries more than one JSON document (next token err = %v):\n%s", err, out)
	}
}

// rawKeys decodes out as an object and returns it, so a test can say a key is
// present (even as null) rather than only that a struct field is zero.
func rawKeys(t *testing.T, out string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader([]byte(out))).Decode(&m); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, out)
	}
	return m
}

func requireKeys(t *testing.T, m map[string]json.RawMessage, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("the wire object has no %q key; it has %v", k, keysOfRaw(m))
		}
	}
}

func keysOfRaw(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- snapshot list | show | holds | retention ---------------------------

// seedSucceededSnapshot puts one finished, measured run in the journal and
// returns its run id, so the read verbs have a real restore point to report.
func seedSucceededSnapshot(t *testing.T, configPath, runID, snapshotID string, startedAt time.Time) {
	t.Helper()
	j, err := state.Open(t.Context(), filepath.Join(filepath.Dir(configPath), "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer func() { _ = j.Close() }()

	setID, err := model.NewBackupSetID("production", "uploads-tree")
	if err != nil {
		t.Fatalf("NewBackupSetID: %v", err)
	}
	if _, err := j.BeginSnapshotRun(t.Context(), state.SnapshotRunRequest{
		RunID: runID, IdempotencyKey: "seed:" + runID, Set: setID, SetUUID: incrementalSetUUID,
		Engine: string(model.EngineKopia), Domain: "production",
		SourceIdentity:  "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		ConsistencyMode: "external_snapshot", VerificationLevel: "content_sample", StartedAt: startedAt,
	}); err != nil {
		t.Fatalf("BeginSnapshotRun: %v", err)
	}
	i64 := func(n int64) *int64 { return &n }
	str := func(s string) *string { return &s }
	yes := true
	step := func(p state.SnapshotPhase, upd state.SnapshotRunUpdate, at time.Duration) {
		upd.At = startedAt.Add(at)
		if err := j.AdvanceSnapshotRun(t.Context(), runID, p, upd); err != nil {
			t.Fatalf("AdvanceSnapshotRun(%s): %v", p, err)
		}
	}
	step(state.PhaseSourceScan, state.SnapshotRunUpdate{}, time.Minute)
	step(state.PhaseSnapshotWrite, state.SnapshotRunUpdate{}, 2*time.Minute)
	step(state.PhaseManifestCommitted, state.SnapshotRunUpdate{
		SnapshotID: str(snapshotID), Files: i64(1204), Directories: i64(58), LogicalBytes: i64(9_000_000),
		SourceBytesRead: i64(8_100_000), RepositoryBytesWritten: i64(2_300_000), ContentReusedBytes: i64(5_800_000),
		EntriesScanned: i64(1300), SourceComplete: &yes, VerificationStatus: str("pending"),
	}, 3*time.Minute)
	step(state.PhaseVerification, state.SnapshotRunUpdate{}, 4*time.Minute)
	step(state.PhaseCatalogCommit, state.SnapshotRunUpdate{
		VerificationStatus: str("passed"), VerificationLevelAchieved: str("content_sample"),
	}, 5*time.Minute)
	step(state.PhaseSuccess, state.SnapshotRunUpdate{}, 6*time.Minute)
}

func placeSnapshotHold(t *testing.T, configPath, holdID, runID, reason string) {
	t.Helper()
	j, err := state.Open(t.Context(), filepath.Join(filepath.Dir(configPath), "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer func() { _ = j.Close() }()
	if _, err := j.PlaceSnapshotHold(t.Context(), state.SnapshotHoldRequest{
		HoldID: holdID, RunID: runID, Reason: reason, PlacedBy: "ticket-4471", At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("PlaceSnapshotHold: %v", err)
	}
}

const incSet = "production/uploads-tree"

func TestSnapshotListJSON_IsTheListSnapshotsResponse_1152(t *testing.T) {
	configPath := aDeploymentWithSnapshots(t)
	seedSucceededSnapshot(t, configPath, "run_a", "k1a2b3c4", time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC))
	seedSnapshotRun(t, configPath, "run_b") // measured nothing

	out, code := cliJSON(t, "snapshot", "list", "--json", "--config", configPath, incSet)
	if code != exitOK {
		t.Fatalf("snapshot list --json = %d, want 0:\n%s", code, out)
	}

	var got apicontract.ListSnapshotsResponse
	strictDecode(t, out, &got)
	if len(got.Snapshots) != 2 {
		t.Fatalf("got %d snapshots, want 2:\n%s", len(got.Snapshots), out)
	}
	byRun := map[string]apicontract.Snapshot{}
	for _, s := range got.Snapshots {
		byRun[s.RunID] = s
	}
	a := byRun["run_a"]
	if a.BackupSetID != incSet || a.Engine != "kopia" || a.RepositoryDomain != "production" || a.SnapshotID != "k1a2b3c4" {
		t.Errorf("run_a identity wrong: %+v", a)
	}
	if a.Phase != "SUCCESS" || !a.LastKnownGood {
		t.Errorf("run_a phase=%q last_known_good=%v, want SUCCESS and true", a.Phase, a.LastKnownGood)
	}
	if a.Files == nil || *a.Files != 1204 || a.LogicalBytes == nil || *a.LogicalBytes != 9_000_000 {
		t.Errorf("run_a counters not carried: files=%v logical=%v", a.Files, a.LogicalBytes)
	}
	if a.VerificationStatus != "passed" || a.VerificationLevelAchieved != "content_sample" {
		t.Errorf("run_a verification = %q/%q", a.VerificationStatus, a.VerificationLevelAchieved)
	}
	if a.CompletedAt == "" || a.DurationSeconds == nil || *a.DurationSeconds != 360 {
		t.Errorf("run_a completed_at=%q duration=%v, want a time and 360s", a.CompletedAt, a.DurationSeconds)
	}

	// The unmeasured run: the counters are present and null on the wire, not
	// absent and not zero. "Not measured" and "measured zero" are different
	// claims and the contract keeps them apart.
	var raw apiRawSnapshots
	strictDecode(t, out, &raw)
	for _, s := range raw.Snapshots {
		if s["run_id"] == "run_b" {
			for _, k := range []string{"entries_scanned", "files", "directories", "logical_bytes", "source_bytes_read", "repository_bytes_written", "content_reused_bytes", "source_complete", "duration_seconds"} {
				v, ok := s[k]
				if !ok {
					t.Errorf("run_b has no %q key; an unmeasured counter is present and null", k)
				} else if v != nil {
					t.Errorf("run_b %s = %v, want null (never measured)", k, v)
				}
			}
		}
	}
}

type apiRawSnapshots struct {
	Snapshots []map[string]any `json:"snapshots"`
}

func TestSnapshotListJSON_AnEmptySetIsAnEmptyArray_1152(t *testing.T) {
	configPath := aDeploymentWithSnapshots(t)
	out, code := cliJSON(t, "snapshot", "list", "--json", "--config", configPath, incSet)
	if code != exitOK {
		t.Fatalf("code = %d", code)
	}
	if strings.Contains(out, "has taken no snapshots") {
		t.Errorf("--json printed the sentence instead of the object:\n%s", out)
	}
	if got := strings.Join(strings.Fields(out), ""); got != `{"snapshots":[]}` {
		t.Errorf("an empty listing is %s, want {\"snapshots\":[]} (an array, never null)", got)
	}
}

func TestSnapshotShowJSON_IsTheSnapshotResponseWithItsTransitionLog_1152(t *testing.T) {
	configPath := aDeploymentWithSnapshots(t)
	seedSucceededSnapshot(t, configPath, "run_a", "k1a2b3c4", time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC))
	placeSnapshotHold(t, configPath, "hold_1", "run_a", "legal request 4471")

	out, code := cliJSON(t, "snapshot", "show", "--json", "--config", configPath, incSet, "run_a")
	if code != exitOK {
		t.Fatalf("snapshot show --json = %d:\n%s", code, out)
	}
	var got apicontract.SnapshotResponse
	strictDecode(t, out, &got)
	if got.Snapshot.RunID != "run_a" || got.Snapshot.ConsistencyMode != "external_snapshot" {
		t.Errorf("snapshot = %+v", got.Snapshot)
	}
	if len(got.Snapshot.Holds) != 1 || got.Snapshot.Holds[0].HoldID != "hold_1" || !got.Snapshot.Holds[0].Active {
		t.Errorf("the hold is not on the snapshot: %+v", got.Snapshot.Holds)
	}
	// PENDING first, SUCCESS last, every edge in between, oldest first.
	if n := len(got.Transitions); n < 7 {
		t.Fatalf("transition log has %d entries, want at least 7:\n%s", n, out)
	}
	last := got.Transitions[len(got.Transitions)-1]
	if last.To != "SUCCESS" || last.At == "" {
		t.Errorf("last transition = %+v, want one into SUCCESS with a time", last)
	}
	requireKeys(t, rawKeys(t, out), "snapshot", "transitions")
}

func TestSnapshotShowJSON_AnUnknownRunPrintsNoDocumentAndFails_1152(t *testing.T) {
	configPath := aDeploymentWithSnapshots(t)
	out, code := cliJSON(t, "snapshot", "show", "--json", "--config", configPath, incSet, "run_nope")
	if code != exitFailure {
		t.Errorf("an unknown run exited %d, want %d (an ordinary failure, not a usage error)", code, exitFailure)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a failed --json read wrote to stdout, which a script would parse as a result:\n%s", out)
	}
}

func TestSnapshotHoldsJSON_IsTheListSnapshotHoldsResponse_1152(t *testing.T) {
	configPath := aDeploymentWithSnapshots(t)
	seedSucceededSnapshot(t, configPath, "run_a", "k1a2b3c4", time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC))

	// None yet: an empty array.
	out, code := cliJSON(t, "snapshot", "holds", "--json", "--config", configPath, incSet)
	if code != exitOK {
		t.Fatalf("code = %d", code)
	}
	if got := strings.Join(strings.Fields(out), ""); got != `{"holds":[]}` {
		t.Errorf("no holds is %s, want {\"holds\":[]}", got)
	}

	placeSnapshotHold(t, configPath, "hold_1", "run_a", "legal request 4471")
	out, code = cliJSON(t, "snapshot", "holds", "--json", "--config", configPath, incSet)
	if code != exitOK {
		t.Fatalf("code = %d", code)
	}
	var got apicontract.ListSnapshotHoldsResponse
	strictDecode(t, out, &got)
	if len(got.Holds) != 1 {
		t.Fatalf("holds = %+v", got.Holds)
	}
	h := got.Holds[0]
	if h.HoldID != "hold_1" || h.RunID != "run_a" || h.BackupSetID != incSet || h.Reason != "legal request 4471" || h.PlacedBy != "ticket-4471" || !h.Active || h.PlacedAt == "" {
		t.Errorf("hold = %+v", h)
	}
	if h.ReleasedAt != "" || h.ReleasedBy != "" {
		t.Errorf("an active hold carries a release: %+v", h)
	}
}

func TestSnapshotRetentionJSON_IsTheSnapshotRetentionResponse_1152(t *testing.T) {
	configPath := aDeploymentWithSnapshots(t)
	seedSucceededSnapshot(t, configPath, "run_a", "k1a2b3c4", time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC))
	placeSnapshotHold(t, configPath, "hold_1", "run_a", "legal request 4471")

	out, code := cliJSON(t, "snapshot", "retention", "--json", "--config", configPath, incSet)
	if code != exitOK {
		t.Fatalf("code = %d:\n%s", code, out)
	}
	var got apicontract.SnapshotRetentionResponse
	strictDecode(t, out, &got)
	if got.GeneratedAt == "" {
		t.Errorf("generated_at is empty")
	}
	if len(got.Verdicts) != 1 {
		t.Fatalf("verdicts = %+v", got.Verdicts)
	}
	v := got.Verdicts[0]
	if v.RunID != "run_a" || v.SnapshotID != "k1a2b3c4" || v.Action == "" || v.Reason == "" || v.StartedAt == "" {
		t.Errorf("verdict = %+v", v)
	}
	if len(v.Holds) != 1 || v.Holds[0].HoldID != "hold_1" {
		t.Errorf("the verdict does not name the hold that protects the snapshot: %+v", v.Holds)
	}
}

func TestSnapshotReadVerbs_TextOutputIsUnchangedWithoutJSON_1152(t *testing.T) {
	configPath := aDeploymentWithSnapshots(t)
	out, code := cliJSON(t, "snapshot", "list", "--config", configPath, incSet)
	if code != exitOK || !strings.Contains(out, "has taken no snapshots") {
		t.Errorf("the text form changed: code=%d out=%q", code, out)
	}
}
