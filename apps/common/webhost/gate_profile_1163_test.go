package webhost

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/profile"
)

// Issues #1163 and #1164: the destructive gate opens for a profile that has no
// identity header to spoof, and verify, hold and release-hold stop sitting
// behind it.
//
// The gate is the second route gate.go always allowed: a code-authored gate
// for a profile with no identity header. These tests are the record of what
// "opens" means. Every one of them reads the router's answer, because a gate
// that reports open while a route still refuses (or the reverse) is the
// failure that matters, and nothing a unit test of the gate type alone could
// tell the two apart.

// stubStepUp is a StepUpVerifier that accepts exactly one password for one
// user and counts how often it was asked.
type stubStepUp struct {
	mu       sync.Mutex
	user     string
	password string
	calls    int
}

func (s *stubStepUp) VerifyStepUp(_ context.Context, username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if username == s.user && password == s.password {
		return nil
	}
	return errors.New("step-up refused")
}

func (s *stubStepUp) asked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

const stepUpPassword = "correct horse battery staple"

type rig struct {
	router  http.Handler
	backend *syncFakeBackend
	rec     *recordingBackend
	stepUp  *stubStepUp
}

type rigOptions struct {
	noStepUp bool
	trustFwd bool
}

func newRig(t *testing.T, gate DestructiveGate, opts ...func(*rigOptions)) *rig {
	t.Helper()
	var o rigOptions
	for _, f := range opts {
		f(&o)
	}
	r := &rig{backend: newSyncFakeBackend(), rec: &recordingBackend{}, stepUp: &stubStepUp{user: "alice", password: stepUpPassword}}
	cfg := RouterConfig{
		Platform:              allowingPlatform("alice"),
		Backend:               r.backend,
		Gate:                  gate,
		Recorder:              r.rec,
		TrustForwardedHeaders: o.trustFwd,
		BinaryVersion:         "test",
		Commit:                "test",
	}
	if !o.noStepUp {
		cfg.StepUp = r.stepUp
	}
	r.router = NewRouter(cfg)
	return r
}

func withoutStepUp(o *rigOptions)         { o.noStepUp = true }
func withTrustedForwarding(o *rigOptions) { o.trustFwd = true }

// call sends one authenticated, CSRF-paired POST. The default peer is the
// loopback address, so a test about the transport rule has to move it on
// purpose.
func (r *rig) call(t *testing.T, path, body string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "idem-1163")
	attachValidCSRF(req)
	for _, m := range mods {
		m(req)
	}
	rec := httptest.NewRecorder()
	r.router.ServeHTTP(rec, req)
	return rec
}

func withStepUp(password string) func(*http.Request) {
	return func(req *http.Request) { req.Header.Set(StepUpHeader, password) }
}

func withPeer(addr string) func(*http.Request) {
	return func(req *http.Request) { req.RemoteAddr = addr }
}

func withHeader(k, v string) func(*http.Request) {
	return func(req *http.Request) { req.Header.Set(k, v) }
}

func withTLS(req *http.Request) { req.TLS = &tls.ConnectionState{} }

func acknowledgingTransport(req *http.Request) {
	req.Header.Set(InsecureTransportHeader, InsecureTransportAcknowledged)
}

func codeOf(rec *httptest.ResponseRecorder) string { return responseErrorCode(rec.Body.String()) }

// The five request bodies. Each carries every acknowledgement it needs so a
// test removes exactly one thing at a time.
const (
	bodyRunCycle        = `{"action":"run_cycle","config_revision":"rev-1"}`
	bodyRestoreSnapshot = `{"action":"restore_snapshot","config_revision":"rev-1","snapshot_restore":` +
		`{"backup_set_id":"production/uploads-tree","snapshot_id":"snap_7","target_path":"/restore/here","conflict":"skip","acknowledged":true}}`
	bodyRestoreSnapshotUnacked = `{"action":"restore_snapshot","config_revision":"rev-1","snapshot_restore":` +
		`{"backup_set_id":"production/uploads-tree","snapshot_id":"snap_7","target_path":"/restore/here","conflict":"skip"}}`
	bodyVerify  = `{"action":"verify_snapshot","config_revision":"rev-1","snapshot_verify":{"backup_set_id":"production/uploads-tree","run_id":"run_9","level":"metadata"}}`
	bodyHold    = `{"action":"hold_snapshot","config_revision":"rev-1","snapshot_hold":{"backup_set_id":"production/uploads-tree","run_id":"run_9","reason":"incident 4711"}}`
	bodyRelease = `{"action":"release_snapshot_hold","config_revision":"rev-1","snapshot_hold_release":` +
		`{"backup_set_id":"production/uploads-tree","hold_id":"hold_3","acknowledged":true}}`
	bodyReleaseUnacked = `{"action":"release_snapshot_hold","config_revision":"rev-1","snapshot_hold_release":` +
		`{"backup_set_id":"production/uploads-tree","hold_id":"hold_3"}}`
)

func restorePlacementBody(rev string, acknowledged bool) string {
	ack := "false"
	if acknowledged {
		ack = "true"
	}
	return `{"action":"restore_placement","config_revision":"` + rev + `","restore":{"artifact_id":"production/postgres/dump.zst","medium":"cold-store","window_days":14,"acknowledged":` + ack + `}}`
}

// ---------------------------------------------------------------------
// #1163: which profiles open the gate
// ---------------------------------------------------------------------

// The pin: every shipped profile's gate state is written down here, and a
// profile this table does not know fails the test. A new profile therefore
// cannot silently open the gate: whoever adds one has to come here, decide,
// and say why.
var shippedProfileGateStates = map[profile.ID]string{
	profile.Generic:        "open",
	profile.TrueNAS:        "open",
	profile.Unraid:         "open",
	profile.OpenMediaVault: "open",
	profile.Proxmox:        "open",
	profile.Synology:       "open",
	// The gateway profile: its identity is a header, and #92 has not
	// verified the trusted-proxy check against real hardware.
	profile.UGOS: "shut",
}

func gateStateOf(g DestructiveGate) string {
	state, _ := gateStatus(g)
	return state
}

func TestProfileGate1163_EveryShippedProfileHasAPinnedGateState(t *testing.T) {
	for _, id := range profile.IDs() {
		want, known := shippedProfileGateStates[id]
		if !known {
			t.Errorf("profile %q is not in shippedProfileGateStates: a new profile has to be classified here before it can open or shut anything", id)
			continue
		}
		if got := gateStateOf(NewProfileGate(id)); got != want {
			t.Errorf("profile %q: gate is %s, want %s", id, got, want)
		}
	}
	for id := range shippedProfileGateStates {
		if _, err := profile.Lookup(string(id)); err != nil {
			t.Errorf("shippedProfileGateStates names %q, which is not a profile: %v", id, err)
		}
	}
}

// The rule in words, checked against the registry rather than a list: it
// opens only when the compiled profile is a local-account profile (no
// Gateway), and a profile with a Gateway never opens it.
func TestProfileGate1163_OpensOnlyWhenTheCompiledProfileHasNoGateway(t *testing.T) {
	for _, id := range profile.IDs() {
		p := id.Profile()
		open := gateStateOf(NewProfileGate(id)) == "open"
		if p.Gateway != nil && open {
			t.Errorf("profile %q has a Gateway and its gate is open", id)
		}
		if p.Capabilities.NativeAuth && open {
			t.Errorf("profile %q reports native auth and its gate is open", id)
		}
		if p.Gateway == nil && !p.Capabilities.NativeAuth && !open {
			t.Errorf("profile %q is a local-account profile and its gate is shut", id)
		}
	}
}

func TestProfileGate1163_AnythingNotAProfileStaysShut(t *testing.T) {
	for _, id := range []profile.ID{"", "nope", "GENERIC", " generic", "generic ", "ugos/../generic"} {
		if got := gateStateOf(NewProfileGate(id)); got != "shut" {
			t.Errorf("NewProfileGate(%q) is %s, want shut", id, got)
		}
	}
	if got := gateStateOf(nil); got != "shut" {
		t.Errorf("a nil gate is %s, want shut", got)
	}
	var nyi NotYetImplementedGate
	if nyi.Passed() {
		t.Error("NotYetImplementedGate passed")
	}
}

// The reason a shut profile gate gives names the profile class rather than
// repeating the generic sentence, and an open one gives none.
func TestProfileGate1163_TheReasonSaysWhyAGatewayProfileIsShut(t *testing.T) {
	state, reason := gateStatus(NewProfileGate(profile.UGOS))
	if state != "shut" || !strings.Contains(reason, "#92") || !strings.Contains(strings.ToLower(reason), "gateway") {
		t.Errorf("ugos gate = %s / %q, want shut with a reason naming the gateway and #92", state, reason)
	}
	state, reason = gateStatus(NewProfileGate(profile.Generic))
	if state != "open" || reason != "" {
		t.Errorf("generic gate = %s / %q, want open with no reason", state, reason)
	}
}

func TestProfileGate1163_TheCapabilityFieldReadsTheRealGatePerProfile(t *testing.T) {
	for _, id := range profile.IDs() {
		want := shippedProfileGateStates[id]
		r := newRig(t, NewProfileGate(id))
		state, _ := destructiveOperationsOf(t, capabilitiesFrom(t, r.router, nil))
		if state != want {
			t.Errorf("profile %q: GET /system/capabilities says %s, want %s (the field must be read from the gate, not assumed)", id, state, want)
		}
	}
}

func TestProfileGate1163_TheCapabilityFieldSaysWhetherRestoresAskForAPassword(t *testing.T) {
	stepUp := func(gate DestructiveGate) bool {
		raw := capabilitiesFrom(t, newRig(t, gate).router, nil)["destructive_operations"].(map[string]any)
		v, present := raw["step_up_required"]
		if !present {
			t.Fatalf("destructive_operations has no step_up_required: %v", raw)
		}
		b, _ := v.(bool)
		return b
	}
	if !stepUp(NewProfileGate(profile.Generic)) {
		t.Error("a local-account profile: step_up_required is false, want true")
	}
	if stepUp(NewProfileGate(profile.UGOS)) {
		t.Error("the gateway profile: step_up_required is true, but a shut gate asks for no password")
	}
	if stepUp(NotYetImplementedGate{}) || stepUp(nil) {
		t.Error("a shut or missing gate reports step_up_required")
	}
	if stepUp(alwaysPassGate{}) {
		t.Error("a gate that declares no conditions reports step_up_required: the field must follow what the router will enforce")
	}
}

func capabilitiesFrom(t *testing.T, router http.Handler, mod func(*http.Request)) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/capabilities", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	if mod != nil {
		mod(req)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// ---------------------------------------------------------------------
// #1163: what an open gate lets through, and what it still asks for
// ---------------------------------------------------------------------

func TestProfileGate1163_AnOpenGateRunsRetentionAndRunCycle(t *testing.T) {
	r := newRig(t, NewProfileGate(profile.Generic))

	if rec := r.call(t, "/api/v1/operations", bodyRunCycle); rec.Code != http.StatusAccepted {
		t.Fatalf("run_cycle on a local-account profile: status = %d, want 202: %s", rec.Code, rec.Body.String())
	}

	preview := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/backup-sets/production/postgres-primary/retention/preview", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	r.router.ServeHTTP(preview, req)
	var plan struct {
		PlanID string `json:"plan_id"`
	}
	_ = json.Unmarshal(preview.Body.Bytes(), &plan)
	if plan.PlanID == "" {
		t.Fatalf("no plan: %s", preview.Body.String())
	}
	apply := r.call(t, "/api/v1/backup-sets/production/postgres-primary/retention/apply", `{"plan_id":"`+plan.PlanID+`"}`)
	if apply.Code != http.StatusOK {
		t.Fatalf("retention apply with its plan id: status = %d, want 200: %s", apply.Code, apply.Body.String())
	}
}

// Retention apply keeps what it always carried: the plan id the operator
// reviewed. Without one, or with one nobody issued, nothing is deleted.
func TestProfileGate1163_RetentionApplyStillNeedsItsPlanID(t *testing.T) {
	r := newRig(t, NewProfileGate(profile.Generic))
	for _, body := range []string{`{}`, `{"plan_id":""}`, `{"plan_id":"retplan_never-issued"}`} {
		rec := r.call(t, "/api/v1/backup-sets/production/postgres-primary/retention/apply", body)
		if rec.Code < 400 {
			t.Errorf("apply with body %s: status = %d, want a refusal", body, rec.Code)
		}
	}
	if got := r.backend.appliedPlans(); got != 0 {
		t.Errorf("%d plans were applied without a reviewed plan id", got)
	}
}

func TestProfileGate1163_AShutProfileRefusesEveryGatedRoute(t *testing.T) {
	r := newRig(t, NewProfileGate(profile.UGOS))
	for _, tc := range []struct{ name, path, body string }{
		{"run_cycle", "/api/v1/operations", bodyRunCycle},
		{"restore_snapshot", "/api/v1/operations", bodyRestoreSnapshot},
		{"restore_placement", "/api/v1/operations", restorePlacementBody("rev-1", true)},
		{"retention apply", "/api/v1/backup-sets/production/postgres-primary/retention/apply", `{"plan_id":"p"}`},
	} {
		rec := r.call(t, tc.path, tc.body, withStepUp(stepUpPassword), acknowledgingTransport)
		if rec.Code != http.StatusForbidden || codeOf(rec) != "DESTRUCTIVE_OPERATIONS_DISABLED" {
			t.Errorf("%s on the gateway profile: %d %s, want 403 DESTRUCTIVE_OPERATIONS_DISABLED", tc.name, rec.Code, codeOf(rec))
		}
	}
}

// Restores need a re-authentication and an explicit acknowledgement on top of
// an open gate. Each is removed on its own and the restore must not reach the
// service.
func TestProfileGate1163_RestoresNeedStepUpAndAnAcknowledgement(t *testing.T) {
	rev := newSyncFakeBackend().ConfigRevision()
	for _, tc := range []struct {
		name         string
		ackedBody    string
		unackedBody  string
		reachedCount func(*rig) int
	}{
		{"restore_snapshot", bodyRestoreSnapshot, bodyRestoreSnapshotUnacked, func(r *rig) int { return r.backend.snapshotRestores() }},
		{"restore_placement", restorePlacementBody(rev, true), restorePlacementBody(rev, false), func(r *rig) int { return r.backend.placementRestores() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The control: everything present, it runs.
			ok := newRig(t, NewProfileGate(profile.Generic))
			if rec := ok.call(t, "/api/v1/operations", tc.ackedBody, withStepUp(stepUpPassword)); rec.Code != http.StatusAccepted {
				t.Fatalf("control (step-up and acknowledgement present): status = %d, want 202: %s", rec.Code, rec.Body.String())
			}
			if tc.reachedCount(ok) != 1 {
				t.Fatalf("control did not reach the service")
			}

			// No acknowledgement.
			r := newRig(t, NewProfileGate(profile.Generic))
			rec := r.call(t, "/api/v1/operations", tc.unackedBody, withStepUp(stepUpPassword))
			if rec.Code < 400 || tc.reachedCount(r) != 0 {
				t.Errorf("without the acknowledgement: status = %d, reached service %d times, want a refusal and zero", rec.Code, tc.reachedCount(r))
			}
			if tc.name == "restore_snapshot" && codeOf(rec) != "RESTORE_TARGET_NOT_ACKNOWLEDGED" {
				t.Errorf("without the acknowledgement: code = %q, want RESTORE_TARGET_NOT_ACKNOWLEDGED", codeOf(rec))
			}

			// No step-up.
			r = newRig(t, NewProfileGate(profile.Generic))
			rec = r.call(t, "/api/v1/operations", tc.ackedBody)
			if rec.Code != http.StatusForbidden || codeOf(rec) != "STEP_UP_REQUIRED" || tc.reachedCount(r) != 0 {
				t.Errorf("without step-up: %d %s (reached %d), want 403 STEP_UP_REQUIRED and zero", rec.Code, codeOf(rec), tc.reachedCount(r))
			}

			// Wrong password.
			r = newRig(t, NewProfileGate(profile.Generic))
			rec = r.call(t, "/api/v1/operations", tc.ackedBody, withStepUp("not the password"))
			if rec.Code != http.StatusForbidden || codeOf(rec) != "STEP_UP_FAILED" || tc.reachedCount(r) != 0 {
				t.Errorf("wrong password: %d %s (reached %d), want 403 STEP_UP_FAILED and zero", rec.Code, codeOf(rec), tc.reachedCount(r))
			}
			if r.stepUp.asked() != 1 {
				t.Errorf("the verifier was asked %d times, want 1", r.stepUp.asked())
			}

			// No verifier wired at all fails closed, even with a password.
			r = newRig(t, NewProfileGate(profile.Generic), withoutStepUp)
			rec = r.call(t, "/api/v1/operations", tc.ackedBody, withStepUp(stepUpPassword))
			if rec.Code != http.StatusServiceUnavailable || codeOf(rec) != "STEP_UP_UNAVAILABLE" || tc.reachedCount(r) != 0 {
				t.Errorf("no verifier: %d %s (reached %d), want 503 STEP_UP_UNAVAILABLE and zero", rec.Code, codeOf(rec), tc.reachedCount(r))
			}
		})
	}
}

// Step-up is asked for restores only. A run cycle or a retention apply is not
// a restore, and asking for a password on every press is how a prompt gets
// clicked through.
func TestProfileGate1163_OnlyRestoresAskForStepUp(t *testing.T) {
	r := newRig(t, NewProfileGate(profile.Generic))
	if rec := r.call(t, "/api/v1/operations", bodyRunCycle); rec.Code != http.StatusAccepted {
		t.Fatalf("run_cycle: status = %d: %s", rec.Code, rec.Body.String())
	}
	if r.stepUp.asked() != 0 {
		t.Errorf("run_cycle asked the verifier %d times", r.stepUp.asked())
	}
}

// The step-up password never reaches the activity feed, the audit entry or
// the response.
func TestProfileGate1163_TheStepUpPasswordIsNeverRecorded(t *testing.T) {
	r := newRig(t, NewProfileGate(profile.Generic))
	rec := r.call(t, "/api/v1/operations", bodyRestoreSnapshot, withStepUp(stepUpPassword))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	raw, _ := json.Marshal(r.rec.recorded())
	if strings.Contains(string(raw)+rec.Body.String(), stepUpPassword) {
		t.Errorf("the password reached the recorded actions or the response: %s", raw)
	}
	if len(r.rec.recorded()) == 0 {
		t.Error("nothing was recorded, so the check above proved nothing")
	}
}

// ---------------------------------------------------------------------
// #1163: the session cookie cannot be Secure and the peer is not loopback
// ---------------------------------------------------------------------

func TestProfileGate1163_AnInsecureNonLoopbackPeerNeedsAnExtraAcknowledgement(t *testing.T) {
	gated := []struct{ name, path, body string }{
		{"run_cycle", "/api/v1/operations", bodyRunCycle},
		{"retention apply", "/api/v1/backup-sets/production/postgres-primary/retention/apply", `{"plan_id":"p"}`},
		{"restore_snapshot", "/api/v1/operations", bodyRestoreSnapshot},
	}
	lan := withPeer("192.168.0.50:51000")
	for _, tc := range gated {
		r := newRig(t, NewProfileGate(profile.Generic))
		rec := r.call(t, tc.path, tc.body, lan, withStepUp(stepUpPassword))
		if rec.Code != http.StatusForbidden || codeOf(rec) != "INSECURE_TRANSPORT_ACK_REQUIRED" {
			t.Errorf("%s over plain HTTP from the LAN: %d %s, want 403 INSECURE_TRANSPORT_ACK_REQUIRED", tc.name, rec.Code, codeOf(rec))
		}
		if r.stepUp.asked() != 0 {
			t.Errorf("%s: the password was checked before the transport was settled", tc.name)
		}
	}

	// Acknowledged: it goes through.
	r := newRig(t, NewProfileGate(profile.Generic))
	if rec := r.call(t, "/api/v1/operations", bodyRunCycle, lan, acknowledgingTransport); rec.Code != http.StatusAccepted {
		t.Errorf("acknowledged insecure transport: status = %d: %s", rec.Code, rec.Body.String())
	}
	// A different value is not an acknowledgement.
	r = newRig(t, NewProfileGate(profile.Generic))
	rec := r.call(t, "/api/v1/operations", bodyRunCycle, lan, withHeader(InsecureTransportHeader, "yes"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("a header with the wrong value was accepted: %d", rec.Code)
	}
}

func TestProfileGate1163_TheTransportRuleHasTheRightExceptions(t *testing.T) {
	lan := withPeer("192.168.0.50:51000")
	for _, tc := range []struct {
		name string
		opts []func(*rigOptions)
		mods []func(*http.Request)
		want int
	}{
		{"loopback peer, plain HTTP", nil, []func(*http.Request){withPeer("127.0.0.1:1")}, http.StatusAccepted},
		{"loopback IPv6 peer, plain HTTP", nil, []func(*http.Request){withPeer("[::1]:1")}, http.StatusAccepted},
		{"TLS terminated here", nil, []func(*http.Request){lan, withTLS}, http.StatusAccepted},
		{"trusted proxy says https", []func(*rigOptions){withTrustedForwarding}, []func(*http.Request){lan, withHeader("X-Forwarded-Proto", "https")}, http.StatusAccepted},
		// A forged X-Forwarded-Proto from a deployment that does not trust the
		// edge proxy changes nothing: the cookie would not be Secure either.
		{"untrusted X-Forwarded-Proto: https", nil, []func(*http.Request){lan, withHeader("X-Forwarded-Proto", "https")}, http.StatusForbidden},
		{"untrusted Forwarded: proto=https", nil, []func(*http.Request){lan, withHeader("Forwarded", "proto=https;for=127.0.0.1")}, http.StatusForbidden},
		// Behind the trusted proxy the peer is the proxy, and the browser's own
		// address is the first X-Forwarded-For entry. Loopback there is local.
		{"trusted proxy, client is loopback", []func(*rigOptions){withTrustedForwarding}, []func(*http.Request){lan, withHeader("X-Forwarded-For", "127.0.0.1")}, http.StatusAccepted},
		{"trusted proxy, client is on the LAN, plain HTTP", []func(*rigOptions){withTrustedForwarding}, []func(*http.Request){lan, withHeader("X-Forwarded-For", "192.168.0.77")}, http.StatusForbidden},
		// X-Forwarded-For is only believed from a trusted proxy.
		{"untrusted X-Forwarded-For: 127.0.0.1", nil, []func(*http.Request){lan, withHeader("X-Forwarded-For", "127.0.0.1")}, http.StatusForbidden},
		// A loopback first entry smuggled behind a real client address is not
		// the client.
		{"trusted proxy, spoofed second entry", []func(*rigOptions){withTrustedForwarding}, []func(*http.Request){lan, withHeader("X-Forwarded-For", "192.168.0.77, 127.0.0.1")}, http.StatusForbidden},
	} {
		r := newRig(t, NewProfileGate(profile.Generic), tc.opts...)
		rec := r.call(t, "/api/v1/operations", bodyRunCycle, tc.mods...)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d (%s), want %d", tc.name, rec.Code, codeOf(rec), tc.want)
		}
	}
}

func TestProfileGate1163_TheCapabilityFieldSaysWhenTheExtraAcknowledgementIsNeeded(t *testing.T) {
	r := newRig(t, NewProfileGate(profile.Generic))
	need := func(mod func(*http.Request)) bool {
		body := capabilitiesFrom(t, r.router, mod)
		raw, ok := body["destructive_operations"].(map[string]any)
		if !ok {
			t.Fatalf("no destructive_operations: %v", body)
		}
		v, present := raw["insecure_transport_ack_required"]
		if !present {
			t.Fatalf("destructive_operations has no insecure_transport_ack_required: %v", raw)
		}
		b, _ := v.(bool)
		return b
	}
	if need(withPeer("127.0.0.1:1")) {
		t.Error("loopback peer: ack required, want not")
	}
	if !need(withPeer("192.168.0.50:51000")) {
		t.Error("LAN peer over plain HTTP: ack not required, want required")
	}
	if need(func(req *http.Request) { req.RemoteAddr = "192.168.0.50:1"; req.TLS = &tls.ConnectionState{} }) {
		t.Error("TLS: ack required, want not")
	}
}

// run_immediately is the gated branch of a create: over plain HTTP from the
// LAN it is refused in run_error and the set is still saved.
func TestProfileGate1163_RunImmediatelyFollowsTheSameRules(t *testing.T) {
	post := func(mods ...func(*http.Request)) map[string]any {
		r := newRig(t, NewProfileGate(profile.Generic))
		r.backend = nil
		router := NewRouter(RouterConfig{
			Platform: allowingPlatform("alice"), Backend: newBackupSetFakeBackend(), Gate: NewProfileGate(profile.Generic),
			StepUp: r.stepUp, BinaryVersion: "test", Commit: "test",
		})
		body := strings.TrimSuffix(strings.TrimSpace(validCreateBody), "}") + `,"run_immediately":true}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/backup-sets", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1"
		req.Header.Set("Content-Type", "application/json")
		attachValidCSRF(req)
		for _, m := range mods {
			m(req)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: status = %d: %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if got := post(); got["run_error"] != nil && got["run_error"] != "" {
		t.Errorf("loopback create with run_immediately on a local-account profile carried run_error %v", got["run_error"])
	}
	if got := post(withPeer("192.168.0.50:1")); got["run_error"] == nil || got["run_error"] == "" {
		t.Error("a run requested over plain HTTP from the LAN, unacknowledged, was not refused")
	}
	if got := post(withPeer("192.168.0.50:1"), acknowledgingTransport); got["run_error"] != nil && got["run_error"] != "" {
		t.Errorf("an acknowledged run was refused: %v", got["run_error"])
	}
}

// ---------------------------------------------------------------------
// #1163: forged headers
// ---------------------------------------------------------------------

// A forged identity or forwarding header changes neither who the caller is
// nor what the gate says. The router's authenticator is the arbiter of the
// first, and the gate is static, so the second cannot move: the test plants
// the headers on every request kind against both profile classes.
func TestProfileGate1163_ForgedIdentityAndForwardingHeadersChangeNothing(t *testing.T) {
	forged := []func(*http.Request){
		withHeader("X-Ugos-User", "mallory"),
		withHeader("X-Forwarded-User", "mallory"),
		withHeader("X-Forwarded-For", "127.0.0.1"),
		withHeader("X-Forwarded-Proto", "https"),
		withHeader("X-Forwarded-Host", "localhost"),
		withHeader("Forwarded", "for=127.0.0.1;proto=https;by=127.0.0.1"),
		withHeader("X-Real-Ip", "127.0.0.1"),
	}

	t.Run("the gateway profile stays shut", func(t *testing.T) {
		r := newRig(t, NewProfileGate(profile.UGOS), withTrustedForwarding)
		for _, path := range []string{"/api/v1/operations", "/api/v1/backup-sets/production/postgres-primary/retention/apply"} {
			body := bodyRunCycle
			if strings.HasSuffix(path, "apply") {
				body = `{"plan_id":"p"}`
			}
			rec := r.call(t, path, body, append(forged, withStepUp(stepUpPassword), acknowledgingTransport)...)
			if rec.Code != http.StatusForbidden || codeOf(rec) != "DESTRUCTIVE_OPERATIONS_DISABLED" {
				t.Errorf("%s with forged headers: %d %s, want 403 DESTRUCTIVE_OPERATIONS_DISABLED", path, rec.Code, codeOf(rec))
			}
		}
		state, _ := destructiveOperationsOf(t, capabilitiesFrom(t, r.router, func(req *http.Request) {
			for _, f := range forged {
				f(req)
			}
		}))
		if state != "shut" {
			t.Errorf("capabilities with forged headers: %s, want shut", state)
		}
	})

	t.Run("a local-account profile does not take its identity from them", func(t *testing.T) {
		r := newRig(t, NewProfileGate(profile.Generic), withTrustedForwarding)
		rec := r.call(t, "/api/v1/operations", bodyRunCycle, forged...)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		var op struct {
			Actor string `json:"actor"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &op)
		if op.Actor != "alice" {
			t.Errorf("actor = %q, want alice: a header became the identity", op.Actor)
		}
		for _, a := range r.rec.recorded() {
			if a.Actor != "alice" {
				t.Errorf("recorded actor = %q, want alice", a.Actor)
			}
		}
	})

	t.Run("a forged X-Forwarded-Proto never makes an untrusted peer secure", func(t *testing.T) {
		r := newRig(t, NewProfileGate(profile.Generic)) // forwarding NOT trusted
		rec := r.call(t, "/api/v1/operations", bodyRunCycle, withPeer("192.168.0.50:1"), withHeader("X-Forwarded-Proto", "https"))
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403: a forged header lifted the transport rule", rec.Code)
		}
	})
}

// ---------------------------------------------------------------------
// #1164: verify, hold and release-hold are off the gate
// ---------------------------------------------------------------------

func TestExemptActions1164_ThreeAreExemptAndRestoreSnapshotIsNot(t *testing.T) {
	want := []OperationAction{ActionVerifySnapshot, ActionHoldSnapshot, ActionReleaseSnapshotHold}
	if len(destructiveGateExemptActions) != len(want) {
		t.Errorf("exempt actions = %v, want exactly %v", destructiveGateExemptActions, want)
	}
	for _, a := range want {
		if !destructiveGateExemptActions[a] {
			t.Errorf("%s is not on destructiveGateExemptActions", a)
		}
	}
	for _, a := range []OperationAction{ActionRestoreSnapshot, ActionRestorePlacement, ActionRunCycle, ActionRunBackupSet, ""} {
		if destructiveGateExemptActions[a] {
			t.Errorf("%s is on destructiveGateExemptActions: it must stay gated", a)
		}
	}
}

// problemsWithExemptBehaviour is the behavioural check the planted-fault test
// below turns on: it drives the router and reports what is wrong, so a
// planted fault is judged by the same code that judges the real list.
func problemsWithExemptBehaviour(t *testing.T) []string {
	t.Helper()
	var problems []string
	for _, gate := range []struct {
		name string
		g    DestructiveGate
	}{{"gateway profile", NewProfileGate(profile.UGOS)}, {"no gate", nil}, {"the shipped shut gate", NotYetImplementedGate{}}} {
		r := newRig(t, gate.g)
		for _, a := range []struct{ name, body string }{{"verify_snapshot", bodyVerify}, {"hold_snapshot", bodyHold}, {"release_snapshot_hold", bodyRelease}} {
			if rec := r.call(t, "/api/v1/operations", a.body); rec.Code != http.StatusAccepted {
				problems = append(problems, gate.name+": "+a.name+" is refused ("+codeOf(rec)+"), want 202")
			}
		}
		for _, a := range []struct{ name, body string }{
			{"restore_snapshot", bodyRestoreSnapshot}, {"run_cycle", bodyRunCycle}, {"restore_placement", restorePlacementBody("rev-1", true)},
		} {
			rec := r.call(t, "/api/v1/operations", a.body, withStepUp(stepUpPassword), acknowledgingTransport)
			if rec.Code != http.StatusForbidden || codeOf(rec) != "DESTRUCTIVE_OPERATIONS_DISABLED" {
				problems = append(problems, gate.name+": "+a.name+" got "+codeOf(rec)+", want DESTRUCTIVE_OPERATIONS_DISABLED")
			}
		}
		if got := r.backend.snapshotRestores(); got != 0 {
			problems = append(problems, gate.name+": restore_snapshot reached the service")
		}
	}
	return problems
}

func TestExemptActions1164_VerifyHoldAndReleasePassAShutGateAndRestoreDoesNot(t *testing.T) {
	if problems := problemsWithExemptBehaviour(t); len(problems) != 0 {
		t.Errorf("%s", strings.Join(problems, "\n"))
	}
}

// The planted fault: restore_snapshot added to the exempt list. The check
// above has to notice, or it proves nothing.
func TestExemptActions1164_APlantedFaultAddingRestoreSnapshotIsCaught(t *testing.T) {
	saved := destructiveGateExemptActions
	t.Cleanup(func() { destructiveGateExemptActions = saved })
	planted := map[OperationAction]bool{}
	for k, v := range saved {
		planted[k] = v
	}
	planted["restore_snapshot"] = true
	destructiveGateExemptActions = planted

	problems := problemsWithExemptBehaviour(t)
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "restore_snapshot") {
		t.Errorf("restore_snapshot on the exempt list went unnoticed; problems were: %q", joined)
	}
}

// A body the gate cannot read is a gated body: an exempt action is exempt
// only when it says so plainly.
func TestExemptActions1164_AnUnreadableOrUnknownActionStaysGated(t *testing.T) {
	r := newRig(t, NewProfileGate(profile.UGOS))
	for _, body := range []string{``, `{`, `[]`, `{"action":"VERIFY_SNAPSHOT"}`, `{"action":" verify_snapshot"}`, `{"action":["verify_snapshot"]}`, `{"action":"nope"}`, `{"action":"run_cycle","action2":"verify_snapshot"}`} {
		rec := r.call(t, "/api/v1/operations", body)
		if rec.Code != http.StatusForbidden || codeOf(rec) != "DESTRUCTIVE_OPERATIONS_DISABLED" {
			t.Errorf("body %q: %d %s, want 403 DESTRUCTIVE_OPERATIONS_DISABLED", body, rec.Code, codeOf(rec))
		}
	}
	// The handler decodes into a struct, which matches keys case-insensitively
	// and lets a later one win. A gate that read keys exactly would see the
	// exempt action here while the handler ran the gated one.
	for _, body := range []string{
		`{"action":"verify_snapshot","ACTION":"run_cycle","config_revision":"rev-1"}`,
		`{"ACTION":"run_cycle","action":"verify_snapshot","config_revision":"rev-1"}`,
		`{"action":"verify_snapshot","Action":"run_cycle","config_revision":"rev-1"}`,
		`{"\u0061ction":"verify_snapshot","action":"run_cycle","config_revision":"rev-1"}`,
	} {
		rec := r.call(t, "/api/v1/operations", body)
		if rec.Code != http.StatusForbidden || codeOf(rec) != "DESTRUCTIVE_OPERATIONS_DISABLED" {
			t.Errorf("body %q: %d %s, want 403 DESTRUCTIVE_OPERATIONS_DISABLED", body, rec.Code, codeOf(rec))
		}
	}
	// Two "action" keys: whichever a lenient parser takes, the gated one must win.
	rec := r.call(t, "/api/v1/operations", `{"action":"verify_snapshot","action":"run_cycle","config_revision":"rev-1"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("duplicate action key: %d, want 403", rec.Code)
	}
	rec = r.call(t, "/api/v1/operations", `{"action":"run_cycle","action":"verify_snapshot","config_revision":"rev-1"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("duplicate action key, other order: %d, want 403", rec.Code)
	}
}

// Authentication still comes first: an unauthenticated caller learns nothing
// about the gate from an exempt action either.
func TestExemptActions1164_AnExemptActionStillNeedsASession(t *testing.T) {
	router := NewRouter(RouterConfig{Platform: noAuthWiredAdapter{}, Backend: newSyncFakeBackend(), Gate: NewProfileGate(profile.UGOS), BinaryVersion: "test", Commit: "test"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations", strings.NewReader(bodyVerify))
	req.Header.Set("Idempotency-Key", "k")
	attachValidCSRF(req)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// The three acts are not exempt from the rest of the route: the revision
// check, the idempotency key and the parameter-object rule still apply.
func TestExemptActions1164_TheOtherRulesOfTheRouteStillApply(t *testing.T) {
	r := newRig(t, NewProfileGate(profile.UGOS))
	rec := r.call(t, "/api/v1/operations", bodyVerify, withHeader("Idempotency-Key", ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no idempotency key: %d, want 400", rec.Code)
	}
	rec = r.call(t, "/api/v1/operations", `{"action":"verify_snapshot","config_revision":"rev-1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no parameter object: %d, want 400", rec.Code)
	}
	rec = r.call(t, "/api/v1/operations", `{"action":"verify_snapshot","config_revision":"rev-1","snapshot_hold":{"backup_set_id":"a/b","reason":"x"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("foreign parameter object: %d, want 400", rec.Code)
	}
}

// The three exempt acts are not subject to the step-up or the transport rule
// either: those belong to what the gate guards.
func TestExemptActions1164_NoStepUpAndNoTransportRuleOnThem(t *testing.T) {
	r := newRig(t, NewProfileGate(profile.Generic))
	for _, body := range []string{bodyVerify, bodyHold, bodyRelease} {
		rec := r.call(t, "/api/v1/operations", body, withPeer("192.168.0.50:1"))
		if rec.Code != http.StatusAccepted {
			t.Errorf("%s: %d %s, want 202 with no password and no transport acknowledgement", body[:40], rec.Code, codeOf(rec))
		}
	}
	if r.stepUp.asked() != 0 {
		t.Error("an exempt act asked for a password")
	}
}

// Release-hold lifts protection, so it asks for an explicit acknowledgement on
// every profile, and the entry in the activity feed says who and which.
func TestReleaseHold1164_NeedsAnExplicitAcknowledgement(t *testing.T) {
	for _, id := range []profile.ID{profile.Generic, profile.UGOS} {
		r := newRig(t, NewProfileGate(id))
		rec := r.call(t, "/api/v1/operations", bodyReleaseUnacked)
		if rec.Code != http.StatusConflict || codeOf(rec) != "SNAPSHOT_HOLD_RELEASE_NOT_ACKNOWLEDGED" {
			t.Errorf("%s, no acknowledgement: %d %s, want 409 SNAPSHOT_HOLD_RELEASE_NOT_ACKNOWLEDGED", id, rec.Code, codeOf(rec))
		}
		if snapshotsOf(r.backend).lastHoldRelease.HoldID != "" {
			t.Errorf("%s: an unacknowledged release reached the service", id)
		}
		rec = r.call(t, "/api/v1/operations", strings.Replace(bodyRelease, `"acknowledged":true`, `"acknowledged":false`, 1))
		if rec.Code != http.StatusConflict {
			t.Errorf("%s, acknowledged:false: %d, want 409", id, rec.Code)
		}
		rec = r.call(t, "/api/v1/operations", bodyRelease)
		if rec.Code != http.StatusAccepted {
			t.Errorf("%s, acknowledged: %d %s, want 202", id, rec.Code, rec.Body.String())
		}
	}
}

func TestReleaseHold1164_TheActivityFeedNamesWhoReleasedWhichHold(t *testing.T) {
	r := newRig(t, NewProfileGate(profile.UGOS))
	if rec := r.call(t, "/api/v1/operations", bodyRelease); rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got := r.rec.last(t)
	if got.Actor != "alice" {
		t.Errorf("actor = %q, want alice", got.Actor)
	}
	if got.Operation != "release_snapshot_hold" || got.HoldID != "hold_3" {
		t.Errorf("operation/hold = %q/%q, want release_snapshot_hold/hold_3: the entry must say which hold was released", got.Operation, got.HoldID)
	}
	if got.BackupSetID != "production/uploads-tree" {
		t.Errorf("backup set = %q, want the set the hold was on", got.BackupSetID)
	}
	if got.Gate != "" {
		t.Errorf("the entry says the gate refused it (%q): it was not refused", got.Gate)
	}
	// A refused release (no acknowledgement) is an entry too, with the same
	// naming, and does not claim the gate refused it.
	r = newRig(t, NewProfileGate(profile.UGOS))
	r.call(t, "/api/v1/operations", bodyReleaseUnacked)
	got = r.rec.last(t)
	if got.Operation != "release_snapshot_hold" || got.HoldID != "hold_3" || got.Gate != "" {
		t.Errorf("refused release entry = %+v", got)
	}
}

// The fake keeps the last request it was handed, which is enough to say
// whether a refused act reached the service at all.
func (f *syncFakeBackend) snapshotRestores() int {
	if snapshotsOf(f).lastRestore.BackupSetID != "" {
		return 1
	}
	return 0
}

func (f *syncFakeBackend) placementRestores() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastRestore.ArtifactID != "" {
		return 1
	}
	return 0
}

// appliedPlans is the number of plans issued and since consumed.
func (f *syncFakeBackend) appliedPlans() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.planNextID - len(f.plans)
}
