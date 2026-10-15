package webhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/core/service"
)

// Issue #1191: the "Source host (SFTP)" recovery destination writes onto a
// production machine, so over HTTP it goes through #1163's destructive
// gate, the acknowledgement and the step-up re-authentication, and every
// other recovery target stays exactly where it was (CSRF, no gate).

// sourceRecoveryBackend is the recovery fake plus the source preflight.
type sourceRecoveryBackend struct {
	*fakeRecoveryBackend
	preflights []string
}

func (b *sourceRecoveryBackend) PreflightRecoverySource(_ context.Context, setID string) (service.RecoverySourceDestination, error) {
	b.preflights = append(b.preflights, setID)
	return service.RecoverySourceDestination{
		SetID: setID, Available: true, Host: "db1.example.internal", Port: 22, User: "retnd-pull",
		RemotePath: "/var/backups/postgres", Folder: "retnd-recovered-20261015-101500",
		Writable: "no", Detail: "the account retnd pulls with is read-only on db1.example.internal",
	}, nil
}

type sourceRig struct {
	router  http.Handler
	backend *sourceRecoveryBackend
	stepUp  *stubStepUp
}

func newSourceRig(t *testing.T, gate DestructiveGate) *sourceRig {
	t.Helper()
	be := &sourceRecoveryBackend{fakeRecoveryBackend: &fakeRecoveryBackend{syncFakeBackend: newSyncFakeBackend(), content: []byte("x")}}
	r := &sourceRig{backend: be, stepUp: &stubStepUp{user: "alice", password: stepUpPassword}}
	r.router = NewRouter(RouterConfig{
		Platform:      allowingPlatform("alice"),
		Backend:       be,
		Gate:          gate,
		StepUp:        r.stepUp,
		BinaryVersion: "test",
		Commit:        "test",
	})
	return r
}

func (r *sourceRig) post(t *testing.T, path, body string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Content-Type", "application/json")
	attachValidCSRF(req)
	for _, m := range mods {
		m(req)
	}
	rec := httptest.NewRecorder()
	r.router.ServeHTTP(rec, req)
	return rec
}

const (
	sourceMintAcked   = `{"target":{"kind":"source","acknowledged":true}}`
	sourceMintUnacked = `{"target":{"kind":"source"}}`
	sourceManyAcked   = `{"set":"production/postgres","target":{"kind":"source","acknowledged":true}}`
	preflightPath     = "/api/v1/backup-sets/production/postgres/recovery-source/preflight"
)

func TestRecoverySource1191_AShutGateRefusesTheSourceTargetAndOnlyIt(t *testing.T) {
	r := newSourceRig(t, NotYetImplementedGate{})
	for _, path := range []string{mintPath, "/api/v1/recoveries"} {
		body := sourceMintAcked
		if path == "/api/v1/recoveries" {
			body = sourceManyAcked
		}
		rec := r.post(t, path, body, withStepUp(stepUpPassword))
		if rec.Code != http.StatusForbidden || codeOf(rec) != "DESTRUCTIVE_OPERATIONS_DISABLED" {
			t.Errorf("%s with a source target on a shut gate = %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if n := len(r.backend.minted); n != 0 {
		t.Errorf("a refused source recovery reached the service %d times", n)
	}
	// The other targets are untouched by the gate.
	if rec := r.post(t, mintPath, `{"target":{"kind":"staging","path":"x"}}`); rec.Code != http.StatusCreated {
		t.Errorf("a staging recovery on a shut gate = %d %s; it must stay off the gate", rec.Code, rec.Body.String())
	}
}

func TestRecoverySource1191_AnOpenLocalAccountGateAsksForTheAcknowledgementAndThePassword(t *testing.T) {
	r := newSourceRig(t, NewProfileGate(profile.Generic))

	if rec := r.post(t, mintPath, sourceMintUnacked, withStepUp(stepUpPassword)); rec.Code != http.StatusBadRequest || codeOf(rec) != "ACKNOWLEDGEMENT_REQUIRED" {
		t.Errorf("unacknowledged = %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.post(t, mintPath, sourceMintAcked); rec.Code != http.StatusForbidden || codeOf(rec) != "STEP_UP_REQUIRED" {
		t.Errorf("no password = %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.post(t, mintPath, sourceMintAcked, withStepUp("wrong")); rec.Code != http.StatusForbidden || codeOf(rec) != "STEP_UP_FAILED" {
		t.Errorf("wrong password = %d %s", rec.Code, rec.Body.String())
	}
	if n := len(r.backend.minted); n != 0 {
		t.Fatalf("%d refused requests reached the service", n)
	}

	rec := r.post(t, mintPath, sourceMintAcked, withStepUp(stepUpPassword))
	if rec.Code != http.StatusCreated {
		t.Fatalf("acknowledged and re-authenticated = %d %s", rec.Code, rec.Body.String())
	}
	if len(r.backend.minted) != 1 {
		t.Fatalf("minted = %d", len(r.backend.minted))
	}
	got := r.backend.minted[0].Target
	if got.Kind != "source" || !got.Acknowledged {
		t.Errorf("the service was handed %+v; the acknowledgement has to reach it", got)
	}

	// The set route asks the same.
	if rec := r.post(t, "/api/v1/recoveries", sourceManyAcked); rec.Code != http.StatusForbidden || codeOf(rec) != "STEP_UP_REQUIRED" {
		t.Errorf("set recovery to the source with no password = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecoverySource1191_PlainHTTPFromAnotherMachineNeedsTheTransportAcknowledgement(t *testing.T) {
	r := newSourceRig(t, NewProfileGate(profile.Generic))
	rec := r.post(t, mintPath, sourceMintAcked, withStepUp(stepUpPassword), withPeer("192.168.0.20:51000"))
	if rec.Code != http.StatusForbidden || codeOf(rec) != "INSECURE_TRANSPORT_ACK_REQUIRED" {
		t.Errorf("source recovery over plain HTTP from the LAN = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecoverySource1191_AGatewayProfileStaysShutForTheSource(t *testing.T) {
	r := newSourceRig(t, NewProfileGate(profile.UGOS))
	rec := r.post(t, mintPath, sourceMintAcked, withStepUp(stepUpPassword))
	if rec.Code != http.StatusForbidden || codeOf(rec) != "DESTRUCTIVE_OPERATIONS_DISABLED" {
		t.Errorf("source recovery on a gateway profile = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecoverySource1191_ThePreflightRouteAnswersBeforeTheRunAndIsNotGated(t *testing.T) {
	r := newSourceRig(t, NotYetImplementedGate{})
	rec := r.post(t, preflightPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight = %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"available": true, "host": "db1.example.internal", "user": "retnd-pull", "remote_path": "/var/backups/postgres",
		"folder": "retnd-recovered-20261015-101500", "writable": "no",
	} {
		if body[k] != want {
			t.Errorf("preflight %s = %v, want %v (body %s)", k, body[k], want, rec.Body.String())
		}
	}
	if len(r.backend.preflights) != 1 || r.backend.preflights[0] != "production/postgres" {
		t.Errorf("preflights = %v", r.backend.preflights)
	}
	// CSRF, like every POST.
	req := httptest.NewRequest(http.MethodPost, preflightPath, nil)
	req.RemoteAddr = "127.0.0.1:40000"
	out := httptest.NewRecorder()
	r.router.ServeHTTP(out, req)
	if out.Code != http.StatusForbidden {
		t.Errorf("preflight without CSRF = %d", out.Code)
	}
}
