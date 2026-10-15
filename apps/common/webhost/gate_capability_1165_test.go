package webhost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Issue #1165: the destructive gate's state is a capability, and a refusal by
// the gate is an audit entry.
//
// Before this, the only way a client could learn the gate was shut was to be
// refused by it. GET /system/capabilities now carries destructive_operations,
// {state: open|shut, reason}, read from the gate this router was built with
// rather than assumed, so the UI, the CLI and an API client stop guessing.
//
// And a refusal by the gate already reached the activity feed as a generic
// "post /operations refused", with the route and the code. It did not say what
// the gate's state was, which act was asked for, or which snapshot it named,
// and the one refusal that is a 201 (create with run_immediately) did not
// reach the feed as a refusal at all. The tests below hold the entry to
// carry those, to be written exactly once per refusal, and to be absent when
// nothing was refused.

func capabilitiesOf(t *testing.T, gate DestructiveGate) map[string]any {
	t.Helper()
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: newSyncFakeBackend(), Gate: gate,
		BinaryVersion: "test", Commit: "test",
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/system/capabilities", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func destructiveOperationsOf(t *testing.T, body map[string]any) (state, reason string) {
	t.Helper()
	raw, ok := body["destructive_operations"].(map[string]any)
	if !ok {
		t.Fatalf("GET /system/capabilities has no destructive_operations object: %v", body)
	}
	state, _ = raw["state"].(string)
	reason, _ = raw["reason"].(string)
	return state, reason
}

func TestGateCapability1165_TheFieldReflectsTheGateTheRouterWasBuiltWith(t *testing.T) {
	for _, tc := range []struct {
		name       string
		gate       DestructiveGate
		wantState  string
		wantReason string // substring; "" means the reason must be empty
	}{
		{"the gate every shipped build has", NotYetImplementedGate{}, "shut", "#92"},
		{"no gate wired at all fails closed", nil, "shut", "gate"},
		{"a gate that passed", alwaysPassGate{}, "open", ""},
	} {
		state, reason := destructiveOperationsOf(t, capabilitiesOf(t, tc.gate))
		if state != tc.wantState {
			t.Errorf("%s: state = %q, want %q (a field hard-coded to either value would pass one row and fail another)", tc.name, state, tc.wantState)
		}
		if tc.wantReason == "" {
			if reason != "" {
				t.Errorf("%s: reason = %q, want empty for an open gate", tc.name, reason)
			}
		} else if !strings.Contains(reason, tc.wantReason) {
			t.Errorf("%s: reason = %q, want it to contain %q", tc.name, reason, tc.wantReason)
		}
	}
}

// The field is read per request. A gate is allowed to change its answer (that
// is what #92 will do), and a capability read once at construction would go
// on saying shut after it opened.
type flippableGate struct{ open bool }

func (g *flippableGate) Passed() bool { return g.open }

func TestGateCapability1165_TheFieldFollowsTheGateWhenItChanges(t *testing.T) {
	g := &flippableGate{}
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: newSyncFakeBackend(), Gate: g,
		BinaryVersion: "test", Commit: "test",
	})
	read := func() string {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/system/capabilities", nil))
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		state, _ := destructiveOperationsOf(t, body)
		return state
	}
	if got := read(); got != "shut" {
		t.Fatalf("before the gate passes: state = %q, want shut", got)
	}
	g.open = true
	if got := read(); got != "open" {
		t.Errorf("after the gate passes: state = %q, want open", got)
	}
}

func refusalRouter(gate DestructiveGate, rec *recordingBackend) http.Handler {
	return NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: newBackupSetFakeBackend(), Gate: gate, Recorder: rec,
		BinaryVersion: "test", Commit: "test",
	})
}

func TestGateRefusalAudit1165_AGatedOperationWritesExactlyOneEntryNamingWhatWasAskedFor(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		wantOperation    string
		wantSet          string
		wantRun          string
		wantSnapshot     string
		wantHold         string
	}{
		{
			name: "hold a snapshot", path: "/api/v1/operations", wantOperation: "hold_snapshot", wantSet: "alpha/nightly", wantRun: "run-1",
			body: `{"action":"hold_snapshot","config_revision":"rev-1","snapshot_hold":{"backup_set_id":"alpha/nightly","run_id":"run-1","reason":"legal review"}}`,
		},
		{
			name: "restore a snapshot", path: "/api/v1/operations", wantOperation: "restore_snapshot", wantSet: "alpha/nightly", wantSnapshot: "k1234",
			body: `{"action":"restore_snapshot","config_revision":"rev-1","snapshot_restore":{"backup_set_id":"alpha/nightly","snapshot_id":"k1234","target_path":"/srv/restored"}}`,
		},
		{
			name: "release a hold", path: "/api/v1/operations", wantOperation: "release_snapshot_hold", wantSet: "alpha/nightly", wantHold: "hold_9",
			body: `{"action":"release_snapshot_hold","config_revision":"rev-1","snapshot_hold_release":{"backup_set_id":"alpha/nightly","hold_id":"hold_9"}}`,
		},
		{
			name: "run one backup set", path: "/api/v1/operations", wantOperation: "run_backup_set", wantSet: "alpha/nightly",
			body: `{"action":"run_backup_set","config_revision":"rev-1","backup_set_id":"alpha/nightly"}`,
		},
		{
			name: "run every set", path: "/api/v1/operations", wantOperation: "run_cycle",
			body: `{"action":"run_cycle","config_revision":"rev-1"}`,
		},
		{
			name: "apply retention", path: "/api/v1/backup-sets/alpha/nightly/retention/apply", wantOperation: "retention_apply", wantSet: "alpha/nightly",
			body: `{"plan_id":"p1"}`,
		},
	} {
		recorder := &recordingBackend{}
		rec := postJSON(t, refusalRouter(NotYetImplementedGate{}, recorder), tc.path, tc.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403", tc.name, rec.Code)
		}
		got := recorder.recorded()
		if len(got) != 1 {
			t.Fatalf("%s: %d entries were written for one refusal, want exactly 1: %+v", tc.name, len(got), got)
		}
		a := got[0]
		if a.Gate != "shut" {
			t.Errorf("%s: the entry records gate state %q, want shut: an operator reading it later has to know what the gate said at that moment", tc.name, a.Gate)
		}
		if !strings.Contains(a.GateReason, "#92") {
			t.Errorf("%s: gate reason = %q, want it to name #92", tc.name, a.GateReason)
		}
		if a.Actor != "alice" {
			t.Errorf("%s: actor = %q, want the session identity", tc.name, a.Actor)
		}
		if a.Operation != tc.wantOperation {
			t.Errorf("%s: operation = %q, want %q", tc.name, a.Operation, tc.wantOperation)
		}
		if a.BackupSetID != tc.wantSet {
			t.Errorf("%s: backup set = %q, want %q (a snapshot act names its set in the body, not the route)", tc.name, a.BackupSetID, tc.wantSet)
		}
		if a.RunID != tc.wantRun || a.SnapshotID != tc.wantSnapshot || a.HoldID != tc.wantHold {
			t.Errorf("%s: run/snapshot/hold = %q/%q/%q, want %q/%q/%q", tc.name, a.RunID, a.SnapshotID, a.HoldID, tc.wantRun, tc.wantSnapshot, tc.wantHold)
		}
		if a.ErrorCode != "DESTRUCTIVE_OPERATIONS_DISABLED" || a.Status != http.StatusForbidden {
			t.Errorf("%s: code/status = %q/%d", tc.name, a.ErrorCode, a.Status)
		}
	}
}

// The create that asks for a run is the refusal that is not a 403. The set is
// saved and the answer is a 201, so nothing in the status marks it, and it
// used to reach the feed as a plain success.
func TestGateRefusalAudit1165_ACreateWhoseRunIsRefusedIsAnEntryEvenThoughItIsA201(t *testing.T) {
	recorder := &recordingBackend{}
	body := strings.TrimSuffix(strings.TrimSpace(validCreateBody), "}") + `,"run_immediately":true}`
	rec := postJSON(t, refusalRouter(NotYetImplementedGate{}, recorder), "/api/v1/backup-sets", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	got := recorder.recorded()
	if len(got) != 1 {
		t.Fatalf("%d entries for one create, want exactly 1: %+v", len(got), got)
	}
	a := got[0]
	if a.Gate != "shut" || a.Operation != "run_immediately" || a.ErrorCode != "DESTRUCTIVE_OPERATIONS_DISABLED" {
		t.Errorf("the entry does not say the run was refused by the gate: gate=%q operation=%q code=%q", a.Gate, a.Operation, a.ErrorCode)
	}
	if !strings.HasSuffix(a.BackupSetID, "/postgres-primary") {
		t.Errorf("backup set = %q, want the set that was created", a.BackupSetID)
	}
	if a.Status != http.StatusCreated {
		t.Errorf("status = %d: the entry records what the client was told", a.Status)
	}
}

// Nothing was refused, so nothing is a gate entry. The ordinary record of the
// call still exists (every non-GET is recorded), but it carries no gate state.
func TestGateRefusalAudit1165_AnExemptCallAndAnOpenGateWriteNoGateEntry(t *testing.T) {
	// A create without a run is exempt from the gate even while it is shut.
	recorder := &recordingBackend{}
	rec := postJSON(t, refusalRouter(NotYetImplementedGate{}, recorder), "/api/v1/backup-sets", validCreateBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("exempt create: status = %d: %s", rec.Code, rec.Body.String())
	}
	for _, a := range recorder.recorded() {
		if a.Gate != "" || a.Operation != "" || a.GateReason != "" {
			t.Errorf("an exempt call that succeeded wrote a gate entry: %+v", a)
		}
	}

	// And a gated call the gate lets through is not a refusal either.
	recorder = &recordingBackend{}
	rec = postJSON(t, refusalRouter(alwaysPassGate{}, recorder), "/api/v1/operations", `{"action":"run_cycle","config_revision":"rev-1"}`)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("an open gate refused: %s", rec.Body.String())
	}
	for _, a := range recorder.recorded() {
		if a.Gate != "" {
			t.Errorf("a call the open gate allowed wrote a gate entry: %+v", a)
		}
	}
}

// The entry names who, what and which, and nothing that authenticates them.
func TestGateRefusalAudit1165_TheEntryCarriesNoCredential(t *testing.T) {
	recorder := &recordingBackend{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations",
		strings.NewReader(`{"action":"verify_snapshot","config_revision":"rev-1","snapshot_verify":{"backup_set_id":"alpha/nightly","run_id":"run-1"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer s3cr3t-token-1165")
	req.AddCookie(&http.Cookie{Name: "retnd_session", Value: "s3cr3t-session-1165"})
	attachValidCSRF(req)
	rec := httptest.NewRecorder()
	refusalRouter(NotYetImplementedGate{}, recorder).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
	got := recorder.recorded()
	if len(got) != 1 || got[0].Gate != "shut" {
		t.Fatalf("expected one gate entry, got %+v", got)
	}
	raw, _ := json.Marshal(got[0])
	for _, secret := range []string{"s3cr3t-token-1165", "s3cr3t-session-1165", req.Header.Get("X-CSRF-Token")} {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Errorf("the entry carries %q: %s", secret, raw)
		}
	}
}
