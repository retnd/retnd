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
	"github.com/retnd/retnd/core/service"
)

// Issues #1191 and #1193: the "Source host (SFTP)" recovery destination writes onto a
// production machine, so over HTTP it goes through #1163's destructive
// gate, the acknowledgement and the step-up re-authentication, and every
// other recovery target stays exactly where it was (CSRF, no gate).

// sourceRecoveryBackend is the recovery fake plus the source-host routes.
type sourceRecoveryBackend struct {
	*fakeRecoveryBackend
	describes  []string
	checks     []string
	checkErr   error
	sourceHost string // the host the configuration resolves to
	grants     map[string]bool
}

func (b *sourceRecoveryBackend) description(setID, folder string) service.SourceHostDescription {
	if folder == "" {
		folder = sourceFolder
	}
	host := b.sourceHost
	if host == "" {
		host = "db1.example.internal"
	}
	return service.SourceHostDescription{
		SetID: setID, Available: true, Host: host, Port: 22, User: "retnd-pull",
		RemotePath: "/var/backups/postgres", Folder: folder, WriteAccess: service.SourceWriteNotChecked,
	}
}

func (b *sourceRecoveryBackend) DescribeSourceHost(setID, folder string) (service.SourceHostDescription, error) {
	b.describes = append(b.describes, setID)
	if setID != "production/postgres" {
		return service.SourceHostDescription{}, service.ErrBackupSetNotFound
	}
	return b.description(setID, folder), nil
}

func (b *sourceRecoveryBackend) CheckSourceHostWrite(_ context.Context, actor, setID, folder string) (service.SourceHostDescription, error) {
	b.checks = append(b.checks, actor+" "+setID)
	if b.checkErr != nil {
		return service.SourceHostDescription{}, b.checkErr
	}
	d := b.description(setID, folder)
	d.WriteAccess, d.Code = service.SourceWriteReadOnly, service.RecoverySourceHostReadOnly
	d.Detail = "the account retnd pulls with is read-only on db1.example.internal"
	d.CheckedAt = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	return d, nil
}

func (b *sourceRecoveryBackend) RecoveryGrantWritesToSourceHost(token string) bool {
	return b.grants[token]
}

// sourceAck is the destination id a client would have been shown.
func sourceAck() string {
	return (&sourceRecoveryBackend{}).description("production/postgres", sourceFolder).DestinationID()
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

func (r *sourceRig) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:40000"
	rec := httptest.NewRecorder()
	r.router.ServeHTTP(rec, req)
	return rec
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
	sourceFolder      = "retnd-recovered-20261015-101500-a1b2"
	sourceMintUnacked = `{"target":{"kind":"source","path":"` + sourceFolder + `"}}`
	describePath      = "/api/v1/backup-sets/production/postgres/source-host"
	writeCheckPath    = "/api/v1/backup-sets/production/postgres/source-host/write-check"
)

var (
	sourceMintAcked = `{"target":{"kind":"source","path":"` + sourceFolder + `","acknowledged_destination":"` + sourceAck() + `"}}`
	sourceManyAcked = `{"set":"production/postgres","target":{"kind":"source","path":"` + sourceFolder + `","acknowledged_destination":"` + sourceAck() + `"}}`
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

	// A consent refusal, in the conflict class with the other
	// *_NOT_ACKNOWLEDGED codes, and the password is not looked at for it.
	if rec := r.post(t, mintPath, sourceMintUnacked, withStepUp(stepUpPassword)); rec.Code != http.StatusConflict || codeOf(rec) != "RECOVERY_SOURCE_HOST_NOT_ACKNOWLEDGED" {
		t.Errorf("unacknowledged = %d %s", rec.Code, rec.Body.String())
	}
	if n := r.stepUp.asked(); n != 0 {
		t.Errorf("the password was checked %d times for a request refused for its acknowledgement", n)
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
	if got.Kind != "source" || got.AcknowledgedDestination != sourceAck() {
		t.Errorf("the service was handed %+v; the acknowledged destination has to reach it", got)
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

// #1193: describing reads configuration and is a GET a dialog may make on
// open; an unknown set is a 404, not a 200 that says so.
func TestSourceHost1193_DescribeIsAConfigOnlyGetWithARealNotFound(t *testing.T) {
	r := newSourceRig(t, NotYetImplementedGate{})
	rec := r.get(t, describePath+"?folder="+sourceFolder)
	if rec.Code != http.StatusOK {
		t.Fatalf("describe = %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"available": true, "host": "db1.example.internal", "user": "retnd-pull", "remote_path": "/var/backups/postgres",
		"folder": sourceFolder, "write_access": "not_checked", "destination_id": sourceAck(),
		"url": "sftp://retnd-pull@db1.example.internal/var/backups/postgres/" + sourceFolder + "/",
	} {
		if body[k] != want {
			t.Errorf("describe %s = %v, want %v (body %s)", k, body[k], want, rec.Body.String())
		}
	}
	if len(r.backend.checks) != 0 {
		t.Errorf("describing ran %d write checks; it must never touch the host", len(r.backend.checks))
	}
	if rec := r.get(t, "/api/v1/backup-sets/production/nope/source-host"); rec.Code != http.StatusNotFound || codeOf(rec) != "BACKUP_SET_NOT_FOUND" {
		t.Errorf("describe of an unknown set = %d %s, want 404 BACKUP_SET_NOT_FOUND", rec.Code, rec.Body.String())
	}
}

// #1193: the write check writes on production, so a shut gate refuses it
// before the service is even asked, and CSRF guards it like every POST.
func TestSourceHost1193_AShutGateNeverRunsTheWriteCheck(t *testing.T) {
	for _, gate := range []DestructiveGate{NotYetImplementedGate{}, NewProfileGate(profile.UGOS)} {
		r := newSourceRig(t, gate)
		rec := r.post(t, writeCheckPath, "")
		if rec.Code != http.StatusForbidden || codeOf(rec) != "DESTRUCTIVE_OPERATIONS_DISABLED" {
			t.Errorf("write check on a shut gate = %d %s", rec.Code, rec.Body.String())
		}
		if len(r.backend.checks) != 0 {
			t.Errorf("a shut gate let %d write checks reach the service", len(r.backend.checks))
		}
	}
	r := newSourceRig(t, NewProfileGate(profile.Generic))
	req := httptest.NewRequest(http.MethodPost, writeCheckPath, nil)
	req.RemoteAddr = "127.0.0.1:40000"
	out := httptest.NewRecorder()
	r.router.ServeHTTP(out, req)
	if out.Code != http.StatusForbidden || len(r.backend.checks) != 0 {
		t.Errorf("write check without CSRF = %d, %d checks", out.Code, len(r.backend.checks))
	}
}

func TestSourceHost1193_AnOpenGateRunsTheWriteCheckAndSaysWhatItFound(t *testing.T) {
	r := newSourceRig(t, NewProfileGate(profile.Generic))
	rec := r.post(t, writeCheckPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("write check = %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["write_access"] != "read_only" || body["code"] != "RECOVERY_SOURCE_HOST_READ_ONLY" || body["checked_at"] != "2026-10-15T09:00:00Z" {
		t.Errorf("write check body = %s", rec.Body.String())
	}
	if len(r.backend.checks) != 1 || r.backend.checks[0] != "alice production/postgres" {
		t.Errorf("checks = %v; the audit needs who asked", r.backend.checks)
	}
	r.backend.checkErr = &service.SourceWriteCheckRateLimited{RetryAfter: 1400 * time.Millisecond}
	rec = r.post(t, writeCheckPath, "")
	if rec.Code != http.StatusTooManyRequests || codeOf(rec) != "RATE_LIMITED" || rec.Header().Get("Retry-After") != "1" {
		t.Errorf("rate limited write check = %d %s Retry-After %q", rec.Code, rec.Body.String(), rec.Header().Get("Retry-After"))
	}
	r.backend.checkErr = &service.RecoveryError{Code: service.RecoverySourceHostUnavailable, Message: "not SFTP"}
	if rec := r.post(t, writeCheckPath, ""); rec.Code != http.StatusConflict || codeOf(rec) != "RECOVERY_SOURCE_HOST_UNAVAILABLE" {
		t.Errorf("write check on a set with no source host = %d %s", rec.Code, rec.Body.String())
	}
}

// #1193: the redeem is where a source grant writes, so a gate that shut
// after the mint stops it; every other grant is untouched.
func TestSourceHost1193_RedeemingASourceGrantChecksTheGateAgain(t *testing.T) {
	r := newSourceRig(t, NotYetImplementedGate{})
	r.backend.grants = map[string]bool{"src-grant": true}
	if rec := r.get(t, "/api/v1/recoveries/src-grant"); rec.Code != http.StatusForbidden || codeOf(rec) != "DESTRUCTIVE_OPERATIONS_DISABLED" {
		t.Errorf("redeem of a source grant on a shut gate = %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.get(t, "/api/v1/recoveries/other-grant"); rec.Code == http.StatusForbidden && codeOf(rec) == "DESTRUCTIVE_OPERATIONS_DISABLED" {
		t.Errorf("a non-source grant was refused by the gate: %s", rec.Body.String())
	}
}

// #1193: the gate compares the acknowledged destination with what the
// configuration resolves to now, and refuses a stale one before it spends a
// password check on it.
func TestSourceHost1193_TheGateRefusesAStaleAcknowledgementBeforeThePassword(t *testing.T) {
	r := newSourceRig(t, NewProfileGate(profile.Generic))
	r.backend.sourceHost = "db2.example.internal"
	rec := r.post(t, mintPath, sourceMintAcked, withStepUp(stepUpPassword))
	if rec.Code != http.StatusConflict || codeOf(rec) != "RECOVERY_SOURCE_HOST_NOT_ACKNOWLEDGED" || !strings.Contains(messageOf(rec), "db2.example.internal") {
		t.Errorf("mint with a stale acknowledgement = %d %s", rec.Code, rec.Body.String())
	}
	if n := r.stepUp.asked(); n != 0 || len(r.backend.minted) != 0 {
		t.Errorf("a stale acknowledgement spent %d password checks and reached the service %d times", n, len(r.backend.minted))
	}
}

// O2: the body is decoded once per request, and asking for it as another
// type is an error, never an empty body that would read as "no target".
func TestSourceHost1193_ASecondDecodeAsAnotherTypeIsAnError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"target":{"kind":"source"}}`))
	w := httptest.NewRecorder()
	p, req := recoveryRequest[recoveryMintRequest](w, req, 1<<10)
	if p.err != nil || p.body.Target.Kind != "source" {
		t.Fatalf("first decode = %+v", p)
	}
	again, _ := recoveryRequest[recoveryMintRequest](w, req, 1<<10)
	if again != p {
		t.Error("asking again for the same type decoded again")
	}
	other, _ := recoveryRequest[recoverManyRequestBody](w, req, 1<<10)
	if other.err == nil {
		t.Error("asking for the consumed body as another type gave a zero value instead of an error")
	}
}

// The step-up for the source target is the restore's step-up, not a copy of
// it: a tripped limiter is 429 RATE_LIMITED with Retry-After, and a
// deployment with nothing to check a password against is 503.
func TestRecoverySource1191_TheStepUpAnswersLikeEveryOtherStepUp(t *testing.T) {
	limited := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: &sourceRecoveryBackend{fakeRecoveryBackend: &fakeRecoveryBackend{syncFakeBackend: newSyncFakeBackend(), content: []byte("x")}},
		Gate: NewProfileGate(profile.Generic), StepUp: rateLimitedStepUp{wait: 1500 * time.Millisecond}, BinaryVersion: "test", Commit: "test",
	})
	r := &sourceRig{router: limited}
	rec := r.post(t, mintPath, sourceMintAcked, withStepUp(stepUpPassword))
	if rec.Code != http.StatusTooManyRequests || codeOf(rec) != "RATE_LIMITED" {
		t.Errorf("tripped limiter = %d %s, want 429 RATE_LIMITED", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}

	none := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: &sourceRecoveryBackend{fakeRecoveryBackend: &fakeRecoveryBackend{syncFakeBackend: newSyncFakeBackend(), content: []byte("x")}},
		Gate: NewProfileGate(profile.Generic), BinaryVersion: "test", Commit: "test",
	})
	r = &sourceRig{router: none}
	rec = r.post(t, "/api/v1/recoveries", sourceManyAcked, withStepUp(stepUpPassword))
	if rec.Code != http.StatusServiceUnavailable || codeOf(rec) != "STEP_UP_UNAVAILABLE" {
		t.Errorf("no verifier = %d %s, want 503 STEP_UP_UNAVAILABLE", rec.Code, rec.Body.String())
	}
	if !strings.Contains(messageOf(rec), "retnd recover --acknowledge-source-write") {
		t.Errorf("message %q does not name the command that does this on the host", messageOf(rec))
	}
}

// The transport half reads the one verdict every gated act reads, including
// the one serve-ui stamps about the browser's own connection.
func TestRecoverySource1191_TheTransportRuleTakesServeUIsVerdictAboutTheBrowser(t *testing.T) {
	r := newSourceRig(t, NewProfileGate(profile.Generic))
	// serve-ui on loopback in front of the engine, browser on the LAN.
	rec := r.post(t, mintPath, sourceMintAcked, withStepUp(stepUpPassword), func(req *http.Request) {
		req.Header.Set(browserTransportHeader, "http")
	})
	if rec.Code != http.StatusForbidden || codeOf(rec) != "INSECURE_TRANSPORT_ACK_REQUIRED" {
		t.Errorf("browser on plain HTTP behind serve-ui = %d %s", rec.Code, rec.Body.String())
	}
	// And the browser on HTTPS while the engine's own peer is the LAN.
	rec = r.post(t, mintPath, sourceMintAcked, withStepUp(stepUpPassword), withPeer("192.168.0.20:51000"), func(req *http.Request) {
		req.Header.Set(browserTransportHeader, "https")
	})
	if rec.Code != http.StatusCreated {
		t.Errorf("browser on HTTPS behind serve-ui = %d %s", rec.Code, rec.Body.String())
	}
}

// The gate and the handler read one decoded body. A body that names the
// target twice (the decoder matches keys without regard to case, and the
// later one wins) is gated on the target the service is then handed.
func TestRecoverySource1191_TheGateAndTheHandlerReadOneDecodedBody(t *testing.T) {
	r := newSourceRig(t, NotYetImplementedGate{})
	twice := `{"target":{"kind":"staging","path":"x"},"Target":{"kind":"source","acknowledged_destination":"sh1-x"}}`
	rec := r.post(t, mintPath, twice, withStepUp(stepUpPassword))
	if rec.Code != http.StatusForbidden || codeOf(rec) != "DESTRUCTIVE_OPERATIONS_DISABLED" {
		t.Errorf("a body whose last target is the source = %d %s, want the shut gate", rec.Code, rec.Body.String())
	}
	if n := len(r.backend.minted); n != 0 {
		t.Errorf("it reached the service %d times", n)
	}

	reversed := `{"Target":{"kind":"source","acknowledged_destination":"sh1-x"},"target":{"kind":"staging","path":"x"}}`
	rec = r.post(t, mintPath, reversed)
	if rec.Code != http.StatusCreated {
		t.Fatalf("a body whose last target is staging = %d %s", rec.Code, rec.Body.String())
	}
	if got := r.backend.minted[0].Target.Kind; got != "staging" {
		t.Errorf("the service was handed a %q target; the gate let it by as staging", got)
	}
}
