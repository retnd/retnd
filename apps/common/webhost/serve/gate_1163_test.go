package serve_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/apps/common/auth/local"
	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/apps/common/webhost"
	"github.com/retnd/retnd/apps/common/webhost/serve"
	"github.com/retnd/retnd/core/service"
)

// Issue #1163 through the real engine: the gate each profile gets, and what
// a forged identity or forwarding header can and cannot do to it.
//
// Everything here goes through serve.NewEngine over a real backend and, for a
// local-account profile, the real local authentication service, because the
// claims are about the composition: that the gate a profile is wired with is
// the one the router asks, and that the layers in front of it (the identity
// strip, the authenticator, the CSRF check) leave a forged header with no way
// to change either the caller or the answer.

type gateStack struct {
	server *httptest.Server
	client *http.Client
	auth   *local.Service
	p      profile.Profile
}

func newGateStack(t *testing.T, id string, mutate func(*profile.Profile), trustFwd bool) *gateStack {
	t.Helper()
	backend, cleanup, err := service.Open(context.Background(), writeTestConfig(t))
	if err != nil {
		t.Fatalf("service.Open: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	authSvc, err := local.New(local.Config{StorePath: filepath.Join(t.TempDir(), "auth.json"), TrustForwardedHeaders: trustFwd})
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	p := profileFor(t, id)
	if mutate != nil {
		mutate(&p)
	}
	adapter, err := p.Adapter(profile.AdapterConfig{LocalAuth: authSvc.Authenticator()})
	if err != nil {
		t.Fatalf("Adapter: %v", err)
	}
	cfg := serve.EngineConfig{
		Platform:              adapter,
		Backend:               backend,
		Gate:                  webhost.NewProfileGate(p.ID),
		TrustForwardedHeaders: trustFwd,
		BinaryVersion:         "test",
		Commit:                "test",
	}
	if p.Gateway == nil {
		cfg.AuthRoutes = authSvc.Handler()
		cfg.StepUp = authSvc
	}
	srv := httptest.NewServer(serve.NewEngine(cfg))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &gateStack{server: srv, client: &http.Client{Jar: jar}, auth: authSvc, p: p}
}

func (s *gateStack) signIn(t *testing.T) {
	t.Helper()
	if s.p.Gateway == nil {
		enrollAndLogIn(t, &engineHarness{auth: s.auth}, s.client, s.server.URL)
	}
}

type reply struct {
	status int
	body   []byte
}

func (r reply) code() string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.body, &env)
	return env.Error.Code
}

func (s *gateStack) do(t *testing.T, method, path, body string, headers map[string]string) reply {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, s.server.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "gate-1163-"+path)
		req.Header.Set(local.CSRFHeaderName, csrfToken(t, s.client, s.server.URL))
	}
	if g := s.p.Gateway; g != nil && headers[g.UsernameHeader] == "" && headers["-no-identity"] == "" {
		req.Header.Set(g.UsernameHeader, "operator")
	}
	for k, v := range headers {
		if k != "-no-identity" {
			req.Header.Set(k, v)
		}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, b}
}

func gateStateOf(t *testing.T, r reply) string {
	t.Helper()
	var body struct {
		Destructive struct {
			State string `json:"state"`
		} `json:"destructive_operations"`
	}
	if err := json.Unmarshal(r.body, &body); err != nil {
		t.Fatalf("capabilities: %v: %s", err, r.body)
	}
	return body.Destructive.State
}

const (
	applyPath      = "/api/v1/backup-sets/production/postgres-primary/retention/apply"
	operationsPath = "/api/v1/operations"
)

// Each profile, wired the way retnd-web wires it, reports its own gate and
// the retention route agrees with the report.
func TestEngineGate1163_EveryProfileIsWiredToItsOwnGate(t *testing.T) {
	for _, id := range profile.IDs() {
		t.Run(string(id), func(t *testing.T) {
			s := newGateStack(t, string(id), nil, false)
			s.signIn(t)
			state := gateStateOf(t, s.do(t, http.MethodGet, "/api/v1/system/capabilities", "", nil))
			wantState, wantCode := "open", "RETENTION_PLAN_NOT_FOUND"
			if s.p.Gateway != nil {
				wantState, wantCode = "shut", "DESTRUCTIVE_OPERATIONS_DISABLED"
			}
			if state != wantState {
				t.Errorf("capabilities say %s, want %s", state, wantState)
			}
			// A plan nobody issued: past an open gate it is a 404 of its own, and
			// behind a shut one it never gets that far.
			r := s.do(t, http.MethodPost, applyPath, `{"plan_id":"retplan_nobody_issued_this"}`, nil)
			if r.code() != wantCode {
				t.Errorf("retention apply: %d %s, want %s", r.status, r.code(), wantCode)
			}
		})
	}
}

// The gateway profile with a trusted peer is authenticated, so the gate is
// the only thing left between it and the route, and it holds. The three
// exempt acts are not behind it.
func TestEngineGate1163_TheGatewayProfileStaysShutEvenWhenAuthenticated(t *testing.T) {
	s := newGateStack(t, "ugos", nil, false)
	for name, tc := range map[string]struct{ path, body string }{
		"run_cycle":        {operationsPath, `{"action":"run_cycle","config_revision":"x"}`},
		"restore_snapshot": {operationsPath, `{"action":"restore_snapshot","config_revision":"x","snapshot_restore":{"backup_set_id":"production/postgres-primary","snapshot_id":"s","target_path":"/t","acknowledged":true}}`},
		"retention apply":  {applyPath, `{"plan_id":"p"}`},
	} {
		r := s.do(t, http.MethodPost, tc.path, tc.body, map[string]string{
			webhost.StepUpHeader: "x", webhost.InsecureTransportHeader: webhost.InsecureTransportAcknowledged,
		})
		if r.status != http.StatusForbidden || r.code() != "DESTRUCTIVE_OPERATIONS_DISABLED" {
			t.Errorf("%s: %d %s, want 403 DESTRUCTIVE_OPERATIONS_DISABLED", name, r.status, r.code())
		}
	}
	for name, body := range map[string]string{
		"verify_snapshot":       `{"action":"verify_snapshot","config_revision":"x","snapshot_verify":{"backup_set_id":"production/postgres-primary"}}`,
		"hold_snapshot":         `{"action":"hold_snapshot","config_revision":"x","snapshot_hold":{"backup_set_id":"production/postgres-primary","reason":"r"}}`,
		"release_snapshot_hold": `{"action":"release_snapshot_hold","config_revision":"x","snapshot_hold_release":{"backup_set_id":"production/postgres-primary","hold_id":"h","acknowledged":true}}`,
	} {
		r := s.do(t, http.MethodPost, operationsPath, body, nil)
		if r.code() == "DESTRUCTIVE_OPERATIONS_DISABLED" || r.status == http.StatusUnauthorized {
			t.Errorf("%s on the gateway profile: %d %s, want it past the gate and the login", name, r.status, r.code())
		}
	}
}

// A forged identity header from a peer the gateway profile does not trust is
// a 401 and nothing else, on every route kind.
func TestEngineGate1163_AForgedIdentityHeaderFromAnUntrustedPeerGetsNothing(t *testing.T) {
	s := newGateStack(t, "ugos", func(p *profile.Profile) { p.Gateway.TrustedPeers = []string{"10.99.0.0/16"} }, true)
	forged := map[string]string{
		"X-Ugos-User": "admin", "X-Forwarded-For": "10.99.0.5", "X-Forwarded-Proto": "https",
		"Forwarded": "for=10.99.0.5;proto=https", "X-Real-Ip": "10.99.0.5",
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/system/capabilities", ""},
		{http.MethodPost, operationsPath, `{"action":"verify_snapshot","config_revision":"x","snapshot_verify":{"backup_set_id":"a/b"}}`},
		{http.MethodPost, operationsPath, `{"action":"run_cycle","config_revision":"x"}`},
		{http.MethodPost, applyPath, `{"plan_id":"p"}`},
	} {
		r := s.do(t, tc.method, tc.path, tc.body, forged)
		if r.status != http.StatusUnauthorized {
			t.Errorf("%s %s with a forged identity: %d %s, want 401", tc.method, tc.path, r.status, r.code())
		}
	}
}

// Under local authentication nothing in the request but the session cookie is
// an identity, and no forwarding header moves the gate. TrustForwardedHeaders
// is on for this stack, which is the setting that could have mattered.
func TestEngineGate1163_UnderLocalAuthForgedHeadersChangeNeitherTheCallerNorTheGate(t *testing.T) {
	s := newGateStack(t, "generic", nil, true)
	forged := map[string]string{
		"X-Ugos-User": "mallory", "X-Forwarded-User": "mallory", "X-Forwarded-For": "127.0.0.1",
		"X-Forwarded-Proto": "https", "Forwarded": "for=127.0.0.1;proto=https", "X-Real-Ip": "127.0.0.1",
	}

	// No session: the forged headers are not one.
	before := s.do(t, http.MethodGet, "/api/v1/system/capabilities", "", forged)
	if before.status != http.StatusUnauthorized {
		t.Fatalf("a forged identity header with no session: %d, want 401", before.status)
	}
	for _, p := range []string{applyPath, operationsPath} {
		if r := s.do(t, http.MethodPost, p, `{"action":"run_cycle","config_revision":"x"}`, forged); r.status != http.StatusUnauthorized {
			t.Errorf("POST %s with a forged identity and no session: %d, want 401", p, r.status)
		}
	}

	s.signIn(t)
	plain := gateStateOf(t, s.do(t, http.MethodGet, "/api/v1/system/capabilities", "", nil))
	withForged := gateStateOf(t, s.do(t, http.MethodGet, "/api/v1/system/capabilities", "", forged))
	if plain != "open" || withForged != plain {
		t.Errorf("gate with no forged headers = %s, with them = %s, want both open", plain, withForged)
	}

	// The session says who the caller is, whatever the headers claim.
	r := s.do(t, http.MethodGet, "/api/v1/auth/session", "", forged)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "bm-admin") || strings.Contains(string(r.body), "mallory") {
		t.Errorf("session under forged headers: %d %s, want bm-admin", r.status, r.body)
	}
}

// A restore through the whole stack: the password is the administrator's, the
// stack is the real one, and a wrong one reaches nothing.
func TestEngineGate1163_ARestoreThroughTheRealStackNeedsTheAdministratorsPassword(t *testing.T) {
	s := newGateStack(t, "generic", nil, false)
	s.signIn(t)
	body := `{"action":"restore_snapshot","config_revision":"x","snapshot_restore":{"backup_set_id":"production/postgres-primary","snapshot_id":"s","target_path":"/t","acknowledged":true}}`

	if r := s.do(t, http.MethodPost, operationsPath, body, nil); r.code() != "STEP_UP_REQUIRED" {
		t.Errorf("no password: %d %s, want STEP_UP_REQUIRED", r.status, r.code())
	}
	if r := s.do(t, http.MethodPost, operationsPath, body, map[string]string{webhost.StepUpHeader: "not it"}); r.code() != "STEP_UP_FAILED" {
		t.Errorf("wrong password: %d %s, want STEP_UP_FAILED", r.status, r.code())
	}
	// The right one gets past the gate, the acknowledgement and the step-up;
	// what the service says next (the set has no incremental engine, or the
	// revision is stale) is its own answer and not one of ours.
	r := s.do(t, http.MethodPost, operationsPath, body, map[string]string{webhost.StepUpHeader: "correct-horse-battery"})
	switch r.code() {
	case "STEP_UP_REQUIRED", "STEP_UP_FAILED", "STEP_UP_UNAVAILABLE", "DESTRUCTIVE_OPERATIONS_DISABLED", "RESTORE_TARGET_NOT_ACKNOWLEDGED", "INSECURE_TRANSPORT_ACK_REQUIRED", "RATE_LIMITED":
		t.Errorf("the right password was refused by our own checks: %d %s", r.status, r.code())
	}
}
