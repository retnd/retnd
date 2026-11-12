package webhost

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/core/apicontract"
)

// #1385: an approved confirmation is replayed on the approver's connection,
// so the transport rule judges the person who clicked approve, exactly as it
// does for their own call. Written red first.

const approveCnf1 = "/api/v1/agent/confirmations/cnf_1/approve"

// approveRunCycle plants a pending run_cycle on the generic profile's open
// gate, approves it with the mods applied to the approve request, and returns
// how the replay ended.
func approveRunCycle(t *testing.T, trustFwd bool, mods ...func(*http.Request)) (*confirmationsRig, apicontract.AgentConfirmation) {
	t.Helper()
	b := &confirmationsBackend{
		backupSetFakeBackend: newBackupSetFakeBackend(),
		fakeAgentAccess:      newFakeAgentAccess(),
		fakeConfirmations:    newFakeConfirmations(pendingConfirmation("cnf_1", "POST", "/operations", `{"action":"run_cycle"}`)),
	}
	rec := &recordingBackend{}
	steps := &stubStepUp{user: "alice", password: stepUpPassword}
	rig := &confirmationsRig{backend: b, rec: rec, steps: steps, router: NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: b, Gate: NewProfileGate(profile.Generic), StepUp: steps, Recorder: rec,
		AgentTools: testTools, TrustForwardedHeaders: trustFwd, BinaryVersion: "test", Commit: "test",
	})}
	req := session(http.MethodPost, approveCnf1, "", stepUpPassword)
	for _, m := range mods {
		m(req)
	}
	res := serve(rig.router, req)
	if res.Code != http.StatusOK {
		t.Fatalf("approve = %d %s; the approval itself stands whatever the replay does", res.Code, res.Body.String())
	}
	var got apicontract.AgentConfirmation
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Execution == nil {
		t.Fatalf("approved confirmation has no execution: %+v", got)
	}
	return rig, got
}

func wantTransportRefusal(t *testing.T, rig *confirmationsRig, got apicontract.AgentConfirmation) {
	t.Helper()
	if got.Execution.Code != "INSECURE_TRANSPORT_ACK_REQUIRED" || got.Execution.Status != 403 || got.Execution.ExitCode != 52 {
		t.Errorf("execution = %+v, want INSECURE_TRANSPORT_ACK_REQUIRED, 403, exit 52", got.Execution)
	}
	if len(rig.backend.ops) != 0 {
		t.Errorf("a refused replay started %d operations", len(rig.backend.ops))
	}
}

// Falsify: replay on a request with no connection (the bug).
func TestApprovingRunCycleOverLoopbackQueuesTheCycle(t *testing.T) {
	rig, got := approveRunCycle(t, false)
	if got.Execution.Status/100 != 2 || got.Execution.Code != "" || got.Execution.ExitCode != 0 {
		t.Errorf("execution = %+v, want a 2xx with exit code 0 as for retnd operation run", got.Execution)
	}
	if len(rig.backend.ops) != 1 {
		t.Errorf("operations queued = %d, want 1", len(rig.backend.ops))
	}
}

// Falsify: stamp the replay loopback whatever the approver's connection was.
func TestApprovingFromAnotherMachineOverPlainHTTPNeedsTheAcknowledgement(t *testing.T) {
	rig, got := approveRunCycle(t, false, func(r *http.Request) { r.RemoteAddr = "192.0.2.9:4000" })
	wantTransportRefusal(t, rig, got)
}

func TestApprovingFromAnotherMachineWithTheAcknowledgementRunsTheCycle(t *testing.T) {
	rig, got := approveRunCycle(t, false, func(r *http.Request) {
		r.RemoteAddr = "192.0.2.9:4000"
		r.Header.Set(InsecureTransportHeader, InsecureTransportAcknowledged)
	})
	if got.Execution.Status/100 != 2 || got.Execution.ExitCode != 0 || len(rig.backend.ops) != 1 {
		t.Errorf("execution = %+v, operations %d, want the acknowledged replay to run", got.Execution, len(rig.backend.ops))
	}
}

func TestApprovingOverTLSFromAnotherMachineRunsTheCycle(t *testing.T) {
	rig, got := approveRunCycle(t, false, func(r *http.Request) {
		r.RemoteAddr = "192.0.2.9:4000"
		r.TLS = &tls.ConnectionState{}
	})
	if got.Execution.Status/100 != 2 || got.Execution.ExitCode != 0 || len(rig.backend.ops) != 1 {
		t.Errorf("execution = %+v, operations %d, want a TLS approver to replay as https", got.Execution, len(rig.backend.ops))
	}
}

// The browser-facing hop's verdict is the approver's, as it is for a call.
func TestApprovingBehindServeUIReplaysTheHopsVerdict(t *testing.T) {
	rig, got := approveRunCycle(t, false, func(r *http.Request) {
		r.RemoteAddr = "172.18.0.2:4000"
		r.Header.Set(BrowserTransportHeader, BrowserTransportHTTPS)
	})
	if got.Execution.Status/100 != 2 || len(rig.backend.ops) != 1 {
		t.Errorf("execution = %+v, operations %d, want the hop's https verdict honoured", got.Execution, len(rig.backend.ops))
	}
	rig, got = approveRunCycle(t, false, func(r *http.Request) {
		r.RemoteAddr = "127.0.0.1:4000"
		r.Header.Set(BrowserTransportHeader, BrowserTransportHTTP)
	})
	wantTransportRefusal(t, rig, got)
}

// Forwarded headers are believed on the replay exactly when they are on the
// approve request: when the deployment trusts them, and no further.
func TestForwardedHeadersOnAnApprovalAreTrustedNoFurtherThanTheRuleTrustsThem(t *testing.T) {
	spoof := func(r *http.Request) {
		r.RemoteAddr = "192.0.2.9:4000"
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		r.Header.Set("X-Forwarded-Proto", "https")
	}
	rig, got := approveRunCycle(t, false, spoof)
	wantTransportRefusal(t, rig, got)

	rig, got = approveRunCycle(t, true, spoof)
	if got.Execution.Status/100 != 2 || len(rig.backend.ops) != 1 {
		t.Errorf("trusted forwarding: execution = %+v, operations %d, want the edge proxy's verdict honoured", got.Execution, len(rig.backend.ops))
	}

	// Only the first entry counts: a loopback entry further along proves nothing.
	rig, got = approveRunCycle(t, true, func(r *http.Request) {
		r.RemoteAddr = "192.0.2.9:4000"
		r.Header.Set("X-Forwarded-For", "198.51.100.7, 127.0.0.1")
	})
	wantTransportRefusal(t, rig, got)
}
