package webhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/core/service"
)

// The review of PR #1167 asked for the gate's reading of a POST /operations
// body and the handler's to be proven to agree, because a body the two read
// differently is a gate bypass. They now share one decode (parsedOperation),
// and these tests hold the composition to that: whatever body arrives, the
// action the backend is asked to run is one the gate let through, decided on
// the same value.

// actionRecordingBackend records which Submit method the handler reached, and
// with what, and answers each with a canned operation.
type actionRecordingBackend struct {
	*syncFakeBackend
	mu      sync.Mutex
	reached []OperationAction
	// restoreAcked is the acknowledgement each restore reached the backend
	// with, as the backend was handed it.
	restoreAcked []bool
}

func newActionRecordingBackend() *actionRecordingBackend {
	return &actionRecordingBackend{syncFakeBackend: newSyncFakeBackend()}
}

func (b *actionRecordingBackend) note(a OperationAction) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reached = append(b.reached, a)
}

func (b *actionRecordingBackend) got() []OperationAction {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]OperationAction(nil), b.reached...)
}

func cannedOperation(a OperationAction) service.Operation {
	return service.Operation{ID: "op_fuzz", Action: string(a), Status: "queued"}
}

func (b *actionRecordingBackend) SubmitRunCycle(context.Context, service.RunCycleRequest) (service.Operation, error) {
	b.note(ActionRunCycle)
	return cannedOperation(ActionRunCycle), nil
}

func (b *actionRecordingBackend) SubmitRunBackupSet(context.Context, service.RunBackupSetRequest) (service.Operation, error) {
	b.note(ActionRunBackupSet)
	return cannedOperation(ActionRunBackupSet), nil
}

func (b *actionRecordingBackend) SubmitRestorePlacement(_ context.Context, req service.RestorePlacementRequest) (service.RestoreSubmission, error) {
	b.note(ActionRestorePlacement)
	b.mu.Lock()
	b.restoreAcked = append(b.restoreAcked, req.Acknowledged)
	b.mu.Unlock()
	return service.RestoreSubmission{Operation: cannedOperation(ActionRestorePlacement)}, nil
}

func (b *actionRecordingBackend) SubmitSnapshotRestore(context.Context, service.SnapshotRestoreRequest) (service.Operation, error) {
	b.note(ActionRestoreSnapshot)
	return cannedOperation(ActionRestoreSnapshot), nil
}

func (b *actionRecordingBackend) SubmitSnapshotVerify(context.Context, service.SnapshotVerifyRequest) (service.Operation, error) {
	b.note(ActionVerifySnapshot)
	return cannedOperation(ActionVerifySnapshot), nil
}

func (b *actionRecordingBackend) SubmitSnapshotHold(context.Context, service.SnapshotHoldRequest) (service.Operation, error) {
	b.note(ActionHoldSnapshot)
	return cannedOperation(ActionHoldSnapshot), nil
}

func (b *actionRecordingBackend) SubmitSnapshotHoldRelease(context.Context, service.SnapshotHoldReleaseRequest) (service.Operation, error) {
	b.note(ActionReleaseSnapshotHold)
	return cannedOperation(ActionReleaseSnapshotHold), nil
}

// postOperation sends body to a fresh router over b with gate, carrying every
// header a well-behaved client might: the right password and the transport
// acknowledgement, from a LAN peer.
func postOperation(gate DestructiveGate, verifier StepUpVerifier, b *actionRecordingBackend, body string) *httptest.ResponseRecorder {
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: b, Gate: gate, StepUp: verifier,
		Recorder: &recordingBackend{}, BinaryVersion: "test", Commit: "test",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations", strings.NewReader(body))
	req.RemoteAddr = "192.168.0.50:1"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "fuzz-1163")
	req.Header.Set(StepUpHeader, stepUpPassword)
	req.Header.Set(InsecureTransportHeader, InsecureTransportAcknowledged)
	attachValidCSRF(req)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// checkOperationGateAgreement is the property, for one body.
func checkOperationGateAgreement(t *testing.T, body string) {
	t.Helper()
	decoded, decodeErr := decodeSubmitOperation([]byte(body))
	tooBig := len(body) > maxSubmitOperationBodyBytes

	// 1. A shut gate lets nothing but an exempt action reach the backend, and
	//    what reaches it is what the handler's decode says the body asked.
	shut := newActionRecordingBackend()
	postOperation(NewProfileGate(profile.UGOS), &stubStepUp{user: "alice", password: stepUpPassword}, shut, body)
	for _, a := range shut.got() {
		if !a.gateExempt() {
			t.Fatalf("a shut gate let %q reach the backend for body %q", a, body)
		}
		if decodeErr != nil || tooBig || decoded.Action != a {
			t.Fatalf("the backend ran %q but the body decodes to %q (err %v) for body %q", a, decoded.Action, decodeErr, body)
		}
	}

	// 2. An open gate whose password check refuses everyone lets no restore
	//    through.
	refusing := newActionRecordingBackend()
	postOperation(NewProfileGate(profile.Generic), &stubStepUp{user: "nobody", password: "never"}, refusing, body)
	for _, a := range refusing.got() {
		if a.isRestore() {
			t.Fatalf("a restore (%q) got past a password check that refuses everyone, for body %q", a, body)
		}
	}

	// 3. With the right password, a restore that reaches the backend is one
	//    the handler's decode says was acknowledged.
	open := newActionRecordingBackend()
	postOperation(NewProfileGate(profile.Generic), &stubStepUp{user: "alice", password: stepUpPassword}, open, body)
	for _, a := range open.got() {
		if decodeErr != nil || decoded.Action != a {
			t.Fatalf("the backend ran %q but the body decodes to %q (err %v) for body %q", a, decoded.Action, decodeErr, body)
		}
		if a.isRestore() && !restoreAcknowledged(&decoded) {
			t.Fatalf("an unacknowledged %q reached the backend for body %q", a, body)
		}
	}

	// 4. The audit entry names the act the handler decoded.
	if decodeErr == nil {
		if op, _, _, _, _ := gateTarget("/operations", nil, []byte(body)); op != string(decoded.Action) {
			t.Fatalf("the audit entry names %q and the handler decodes %q for body %q", op, decoded.Action, body)
		}
	}
}

// operationGateSeeds are the shapes a parser differential hides in.
func operationGateSeeds() []string {
	verify := `{"backup_set_id":"production/uploads-tree","run_id":"run_9","level":"metadata"}`
	restore := `{"backup_set_id":"production/uploads-tree","snapshot_id":"snap_7","target_path":"/t","acknowledged":true}`
	seeds := []string{
		bodyRunCycle, bodyRestoreSnapshot, bodyRestoreSnapshotUnacked, bodyVerify, bodyHold, bodyRelease, bodyReleaseUnacked,
		restorePlacementBody("rev-1", true), restorePlacementBody("rev-1", false),
		// Case-folded and escaped keys.
		`{"ACTION":"verify_snapshot","config_revision":"rev-1","snapshot_verify":` + verify + `}`,
		`{"Action":"restore_snapshot","config_revision":"rev-1","SNAPSHOT_RESTORE":` + restore + `}`,
		`{"action":"verify_snapshot","config_revision":"rev-1","snapshot_verify":` + verify + `}`,
		`{"action":"verify_snapshot","config_revision":"rev-1","snapshot_verify":` + verify + `}`,
		`{"aCtIoN":"restore_snapshot","config_revision":"rev-1","snapshot_restore":` + restore + `}`,
		// Duplicate keys, both orders, and case-folded duplicates.
		`{"action":"verify_snapshot","action":"restore_snapshot","config_revision":"rev-1","snapshot_restore":` + restore + `}`,
		`{"action":"restore_snapshot","action":"verify_snapshot","config_revision":"rev-1","snapshot_verify":` + verify + `}`,
		`{"action":"verify_snapshot","ACTION":"run_cycle","config_revision":"rev-1"}`,
		`{"ACTION":"run_cycle","action":"verify_snapshot","config_revision":"rev-1","snapshot_verify":` + verify + `}`,
		// Duplicate acknowledgements and parameter objects.
		`{"action":"restore_snapshot","config_revision":"rev-1","snapshot_restore":` + restore + `,"snapshot_restore":{"backup_set_id":"a/b","target_path":"/x","acknowledged":false}}`,
		`{"action":"restore_snapshot","config_revision":"rev-1","snapshot_restore":{"backup_set_id":"a/b","target_path":"/x","acknowledged":true,"acknowledged":false}}`,
		`{"action":"restore_snapshot","config_revision":"rev-1","snapshot_restore":{"backup_set_id":"a/b","target_path":"/x","acknowledged":false,"ACKNOWLEDGED":true}}`,
		// Trailing data, a second object, and junk after the first value.
		bodyVerify + `{"action":"restore_snapshot"}`,
		bodyVerify + ` garbage`,
		bodyRestoreSnapshot + `,`,
		// Wrong types and nulls.
		`{"action":["verify_snapshot"]}`, `{"action":null,"config_revision":"rev-1"}`, `{"action":7}`,
		`{"action":"verify_snapshot","snapshot_verify":null,"config_revision":"rev-1"}`,
		`{"action":"restore_snapshot","snapshot_restore":{"acknowledged":"true","target_path":"/t","backup_set_id":"a/b"},"config_revision":"rev-1"}`,
		// Not an object at all.
		``, `{`, `[]`, `"verify_snapshot"`, `null`,
	}
	// Bodies straddling the recorder's bound and the route's own.
	for _, n := range []int{maxRecordedBodyBytes - 64, maxRecordedBodyBytes, maxRecordedBodyBytes + 64} {
		pad := `,"pad":"` + strings.Repeat("x", n) + `"}`
		seeds = append(seeds,
			strings.TrimSuffix(bodyVerify, "}")+pad,
			strings.TrimSuffix(bodyRestoreSnapshot, "}")+pad,
			`{"pad":"`+strings.Repeat("x", n)+`",`+strings.TrimPrefix(bodyRestoreSnapshot, "{"),
		)
	}
	return seeds
}

func FuzzOperationGate1163_TheGateAndTheHandlerReadTheSameBody(f *testing.F) {
	for _, s := range operationGateSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		checkOperationGateAgreement(t, body)
	})
}

// Every action the handler dispatches is decided by the gate on the value the
// handler then runs: each reaches its own backend method on an open gate with
// every consent given, and only the three exempt ones reach it on a shut one.
func TestOperationGate1163_EveryActionIsDecidedOnTheValueTheHandlerRuns(t *testing.T) {
	bodies := map[OperationAction]string{
		ActionRunCycle:            bodyRunCycle,
		ActionRunBackupSet:        `{"action":"run_backup_set","config_revision":"rev-1","backup_set_id":"production/uploads-tree"}`,
		ActionRestorePlacement:    restorePlacementBody("rev-1", true),
		ActionRestoreSnapshot:     bodyRestoreSnapshot,
		ActionVerifySnapshot:      bodyVerify,
		ActionHoldSnapshot:        bodyHold,
		ActionReleaseSnapshotHold: bodyRelease,
	}
	for action, body := range bodies {
		open := newActionRecordingBackend()
		w := postOperation(NewProfileGate(profile.Generic), &stubStepUp{user: "alice", password: stepUpPassword}, open, body)
		if got := open.got(); len(got) != 1 || got[0] != action {
			t.Errorf("%s on an open gate: reached %v (status %d %s), want exactly [%s]", action, got, w.Code, codeOf(w), action)
		}
		shut := newActionRecordingBackend()
		w = postOperation(NewProfileGate(profile.UGOS), nil, shut, body)
		reached := len(shut.got()) == 1
		if reached != action.gateExempt() {
			t.Errorf("%s on a shut gate: reached = %v (status %d %s), want %v", action, reached, w.Code, codeOf(w), action.gateExempt())
		}
	}
}
