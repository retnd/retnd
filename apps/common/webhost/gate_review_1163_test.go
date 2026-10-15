package webhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/apps/common/platform/profile"
)

// The review of PR #1167 (issues #1163 and #1164): what the gate refuses with,
// what the audit records when it does, and which hop decides whether the
// browser's connection is encrypted.

// A run asked for on create and refused for transport is a 201, exactly like
// one refused by a shut gate, so it needs the same note or the feed records a
// plain success. The note says why, and the why is not "the gate is shut".
func TestTransportAudit1163_ARunRefusedForTransportIsAnEntryWithItsOwnReason(t *testing.T) {
	rec := &recordingBackend{}
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: newBackupSetFakeBackend(), Gate: NewProfileGate(profile.Generic),
		StepUp: &stubStepUp{user: "alice", password: stepUpPassword}, Recorder: rec, BinaryVersion: "test", Commit: "test",
	})
	body := strings.TrimSuffix(strings.TrimSpace(validCreateBody), "}") + `,"run_immediately":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup-sets", strings.NewReader(body))
	req.RemoteAddr = "192.168.0.50:1"
	req.Header.Set("Content-Type", "application/json")
	attachValidCSRF(req)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d: %s", w.Code, w.Body.String())
	}
	got := rec.recorded()
	if len(got) != 1 {
		t.Fatalf("%d entries for one create, want exactly 1: %+v", len(got), got)
	}
	a := got[0]
	if a.Operation != "run_immediately" || a.ErrorCode != "INSECURE_TRANSPORT_ACK_REQUIRED" {
		t.Errorf("the entry does not say the run was refused for transport: operation=%q code=%q", a.Operation, a.ErrorCode)
	}
	if a.Gate != "open" {
		t.Errorf("gate = %q, want open: the gate passed, the transport rule refused", a.Gate)
	}
	if !strings.Contains(a.GateReason, "not encrypted") {
		t.Errorf("gate reason = %q, want the transport reason, distinct from a shut gate's", a.GateReason)
	}
	if !strings.HasSuffix(a.BackupSetID, "/postgres-primary") {
		t.Errorf("backup set = %q, want the set that was created", a.BackupSetID)
	}
}

// The same refusal on the two gated routes is recorded the same way, so the
// three places the transport rule applies cannot drift apart in the feed.
func TestTransportAudit1163_TheGatedRoutesRecordTheTransportRefusalToo(t *testing.T) {
	for _, tc := range []struct{ name, path, body, op string }{
		{"run_cycle", "/api/v1/operations", bodyRunCycle, "run_cycle"},
		{"retention apply", "/api/v1/backup-sets/production/uploads-tree/retention/apply", `{"plan_id":"p"}`, "retention_apply"},
	} {
		r := newRig(t, NewProfileGate(profile.Generic))
		w := r.call(t, tc.path, tc.body, withPeer("192.168.0.50:1"))
		if codeOf(w) != "INSECURE_TRANSPORT_ACK_REQUIRED" {
			t.Fatalf("%s: %d %s, want INSECURE_TRANSPORT_ACK_REQUIRED", tc.name, w.Code, codeOf(w))
		}
		a := r.rec.last(t)
		if a.Gate != "open" || a.Operation != tc.op || !strings.Contains(a.GateReason, "not encrypted") {
			t.Errorf("%s: entry gate=%q operation=%q reason=%q, want open/%s/the transport reason", tc.name, a.Gate, a.Operation, a.GateReason, tc.op)
		}
	}
}

// Every other consent refusal in the contract is a 409 named for what was not
// acknowledged. These follow that, and each one says what its own act does.
func TestAcknowledgements1163_EachRefusalIsA409NamedForItsAct(t *testing.T) {
	rev := newSyncFakeBackend().ConfigRevision()
	for _, tc := range []struct {
		name, body, code string
		mustSay          []string
		mustNotSay       []string
	}{
		{"restore_snapshot", bodyRestoreSnapshotUnacked, "RESTORE_TARGET_NOT_ACKNOWLEDGED", []string{"/restore/here"}, nil},
		// A provider thaw writes into no directory. Its acknowledgement is the
		// cost one it always carried.
		{"restore_placement", restorePlacementBody(rev, false), "RECOVERY_COST_NOT_ACKNOWLEDGED", []string{"cost"}, []string{"directory"}},
	} {
		r := newRig(t, NewProfileGate(profile.Generic))
		w := r.call(t, "/api/v1/operations", tc.body, withStepUp(stepUpPassword))
		if w.Code != http.StatusConflict || codeOf(w) != tc.code {
			t.Errorf("%s unacknowledged: %d %s, want 409 %s", tc.name, w.Code, codeOf(w), tc.code)
		}
		msg := messageOf(w)
		for _, s := range tc.mustSay {
			if !strings.Contains(msg, s) {
				t.Errorf("%s: message %q does not mention %q", tc.name, msg, s)
			}
		}
		for _, s := range tc.mustNotSay {
			if strings.Contains(msg, s) {
				t.Errorf("%s: message %q mentions %q, which is another act's consequence", tc.name, msg, s)
			}
		}
		if r.stepUp.asked() != 0 {
			t.Errorf("%s: the password was checked for a request refused for its acknowledgement", tc.name)
		}
	}

	for _, id := range []profile.ID{profile.Generic, profile.UGOS} {
		r := newRig(t, NewProfileGate(id))
		w := r.call(t, "/api/v1/operations", bodyReleaseUnacked)
		if w.Code != http.StatusConflict || codeOf(w) != "SNAPSHOT_HOLD_RELEASE_NOT_ACKNOWLEDGED" {
			t.Errorf("%s release unacknowledged: %d %s, want 409 SNAPSHOT_HOLD_RELEASE_NOT_ACKNOWLEDGED", id, w.Code, codeOf(w))
		}
		if !strings.Contains(messageOf(w), "prun") {
			t.Errorf("%s release: message %q does not say the snapshot becomes prunable", id, messageOf(w))
		}
	}
}

// rateLimitedStepUp refuses every check as the local service does once its
// limiter has tripped.
type rateLimitedStepUp struct{ wait time.Duration }

type stepUpLimitErr struct{ wait time.Duration }

func (e stepUpLimitErr) Error() string             { return "too many step-up attempts" }
func (e stepUpLimitErr) RetryAfter() time.Duration { return e.wait }

func (s rateLimitedStepUp) VerifyStepUp(context.Context, string, string) error {
	return stepUpLimitErr{s.wait}
}

// A tripped limiter is not a wrong password. Telling the operator to "check it
// and try again" keeps the limiter tripped.
func TestStepUp1163_ATrippedLimiterIsA429WithRetryAfter(t *testing.T) {
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: newSyncFakeBackend(), Gate: NewProfileGate(profile.Generic),
		StepUp: rateLimitedStepUp{wait: 36500 * time.Millisecond}, BinaryVersion: "test", Commit: "test",
	})
	r := &rig{router: router}
	w := r.call(t, "/api/v1/operations", bodyRestoreSnapshot, withStepUp(stepUpPassword))
	if w.Code != http.StatusTooManyRequests || codeOf(w) != "RATE_LIMITED" {
		t.Fatalf("tripped limiter: %d %s, want 429 RATE_LIMITED", w.Code, codeOf(w))
	}
	if got := w.Header().Get("Retry-After"); got != "37" {
		t.Errorf("Retry-After = %q, want 37 (whole seconds, rounded up)", got)
	}
	if strings.Contains(strings.ToLower(messageOf(w)), "not accepted") {
		t.Errorf("message %q says the password was wrong; it was never looked at", messageOf(w))
	}

	// The control: a plain refusal is still a wrong password.
	ok := newRig(t, NewProfileGate(profile.Generic))
	if w := ok.call(t, "/api/v1/operations", bodyRestoreSnapshot, withStepUp("nope")); w.Code != http.StatusForbidden || codeOf(w) != "STEP_UP_FAILED" {
		t.Errorf("wrong password: %d %s, want 403 STEP_UP_FAILED", w.Code, codeOf(w))
	}
}

// A deployment that cannot re-check a password is missing a capability, the
// way RECOVERY_UNAVAILABLE is, not refusing this caller.
func TestStepUp1163_NoVerifierIsUnavailableNotForbidden(t *testing.T) {
	r := newRig(t, NewProfileGate(profile.Generic), withoutStepUp)
	w := r.call(t, "/api/v1/operations", bodyRestoreSnapshot, withStepUp(stepUpPassword))
	if w.Code != http.StatusServiceUnavailable || codeOf(w) != "STEP_UP_UNAVAILABLE" {
		t.Errorf("no verifier: %d %s, want 503 STEP_UP_UNAVAILABLE", w.Code, codeOf(w))
	}
}

// browserTransportHeader is what serve-ui stamps on every request it proxies.
const browserTransportHeader = "X-Retnd-Browser-Transport"

// The engine behind serve-ui sees serve-ui as its peer, which is loopback on
// the Synology package and a container address everywhere else. Neither says
// anything about the browser, so the hop that faces the browser decides.
func TestTransport1163_TheBrowserFacingHopDecides(t *testing.T) {
	for _, tc := range []struct {
		name     string
		peer     string
		trustFwd bool
		verdict  string
		wantAck  bool
	}{
		{"loopback peer, browser on the LAN over http (the .spk)", "127.0.0.1:1", false, "http", true},
		{"container peer, browser on the NAS (Unraid)", "172.18.0.3:1", false, "loopback", false},
		{"container peer, browser over TLS (Unraid)", "172.18.0.3:1", false, "https", false},
		{"trusted forwarding, browser on the LAN over http", "172.18.0.3:1", true, "http", true},
		{"a verdict this build does not know is not encrypted", "127.0.0.1:1", false, "quantum", true},
	} {
		r := newRig(t, NewProfileGate(profile.Generic), func(o *rigOptions) { o.trustFwd = tc.trustFwd })
		w := r.call(t, "/api/v1/operations", bodyRunCycle, withPeer(tc.peer), withHeader(browserTransportHeader, tc.verdict))
		gotAck := codeOf(w) == "INSECURE_TRANSPORT_ACK_REQUIRED"
		if gotAck != tc.wantAck {
			t.Errorf("%s: %d %s, want ack required = %v", tc.name, w.Code, codeOf(w), tc.wantAck)
		}
		caps := capabilitiesFrom(t, r.router, func(req *http.Request) {
			req.RemoteAddr = tc.peer
			req.Header.Set(browserTransportHeader, tc.verdict)
		})
		d := caps["destructive_operations"].(map[string]any)
		if d["insecure_transport_ack_required"] != tc.wantAck {
			t.Errorf("%s: capabilities say %v, want %v", tc.name, d["insecure_transport_ack_required"], tc.wantAck)
		}
	}
}

// The field depends on who asked, so no cache may hand one caller's answer to
// another.
func TestCapabilities1163_TheReadIsNotCacheable(t *testing.T) {
	r := newRig(t, NewProfileGate(profile.Generic))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/capabilities", nil)
	req.RemoteAddr = "127.0.0.1:1"
	w := httptest.NewRecorder()
	r.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func messageOf(w *httptest.ResponseRecorder) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return env.Error.Message
}
