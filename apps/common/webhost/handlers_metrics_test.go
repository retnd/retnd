package webhost

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/service"
)

// EPIC O, O1.1 (#899): the authenticated Prometheus scrape endpoint.
//
// Every test drives the real chi router over a real service opened from a
// real config file, and takes the scrape credential from the real mint route,
// because a handler that is only ever tested against a verifier written for
// the test is a handler whose real verifier nobody has met.
//
// The exposition the endpoint serves is checked by a small parser here, not
// by reading the handler: a body that "looks like" exposition to the code
// that wrote it is the usual way a metrics endpoint ships unreadable.

const (
	scrapeContentType = "text/plain; version=0.0.4; charset=utf-8"
	scrapeBlockOn     = "observability:\n  metrics:\n    enabled: true\n    min_rebuild_interval: 1s\n"
)

type scrapeRig struct {
	t       *testing.T
	svc     *service.BackupService
	admin   http.Handler // a session the platform always accepts
	public  http.Handler // the same service with no session wired at all
	actions *recordingBackend
	cfgPath string
	stateOf string
}

func newScrapeRig(t *testing.T, block string) *scrapeRig {
	t.Helper()
	cfgPath := writeObservabilityRouteConfig(t, block)
	svc, cleanup, err := service.Open(t.Context(), cfgPath)
	if err != nil {
		t.Fatalf("service.Open: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	actions := &recordingBackend{}
	build := func(p any) http.Handler {
		cfg := RouterConfig{Backend: svc, Gate: alwaysPassGate{}, BinaryVersion: "test", Commit: "test", Recorder: actions}
		switch v := p.(type) {
		case noAuthWiredAdapter:
			cfg.Platform = v
		default:
			cfg.Platform = allowingPlatform("alice")
		}

		return NewRouter(cfg)
	}

	return &scrapeRig{
		t: t, svc: svc, admin: build("session"), public: build(noAuthWiredAdapter{}),
		actions: actions, cfgPath: cfgPath, stateOf: filepath.Dir(cfgPath),
	}
}

// mint takes the credential from the real route, as a signed-in operator on
// this machine would.
func (r *scrapeRig) mint() string {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/observability/scrape-token", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	rec := serve(r.admin, csrfPaired(req))
	if rec.Code != http.StatusCreated {
		r.t.Fatalf("minting the scrape credential = %d %s, want 201", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		r.t.Errorf("the mint response carries Cache-Control %q, want no-store", got)
	}
	var out struct {
		Token       string `json:"token"`
		ScrapeToken struct {
			Exists bool   `json:"exists"`
			Prefix string `json:"prefix"`
		} `json:"scrape_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		r.t.Fatalf("decoding the mint: %v\n%s", err, rec.Body.String())
	}
	if !strings.HasPrefix(out.Token, "rsc_") || len(out.Token) < 40 || !out.ScrapeToken.Exists || out.ScrapeToken.Prefix != out.Token[:8] {
		r.t.Fatalf("the mint answered %+v, want an rsc_ token of 256 bits and its eight-character prefix", out)
	}

	return out.Token
}

// scrape is one GET /metrics from addr with the given Authorization header.
func (r *scrapeRig) scrape(addr, authorization string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = addr
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	return serve(r.public, req)
}

func basic(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}

var sampleLine = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{[^}]*\})? [-+0-9.eE]+$`)

// parseExposition is a strict reader of the text format: a comment, a blank
// line or a sample, and nothing else. It returns the samples by name.
func parseExposition(t *testing.T, body string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for i, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		switch {
		case line == "", strings.HasPrefix(line, "# HELP "), strings.HasPrefix(line, "# TYPE "):
		case sampleLine.MatchString(line):
			name := line
			if j := strings.IndexAny(line, "{ "); j >= 0 {
				name = line[:j]
			}
			out[name] = append(out[name], line)
		default:
			t.Fatalf("line %d is not exposition: %q", i+1, line)
		}
	}

	return out
}

func requireRefusal(t *testing.T, what string, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want || rec.Body.Len() != 0 {
		t.Errorf("%s = %d with %d body bytes (%q), want %d and an empty body", what, rec.Code, rec.Body.Len(), rec.Body.String(), want)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("%s carries Cache-Control %q, want no-store on every response", what, got)
	}
}

// O-01. Disabled, the route answers 404 and nothing hints that the feature
// exists: not a body, not a Www-Authenticate challenge.
func TestMetrics_DisabledAnswers404WithNothingThatRevealsTheFeature(t *testing.T) {
	rig := newScrapeRig(t, "")
	for name, header := range map[string]string{"no credential": "", "a bearer": "Bearer rsc_anything"} {
		rec := rig.scrape("192.0.2.10:1000", header)
		requireRefusal(t, "disabled /metrics with "+name, rec, http.StatusNotFound)
		if rec.Header().Get("Www-Authenticate") != "" {
			t.Errorf("disabled /metrics with %s challenges for credentials, which tells a stranger the feature is there", name)
		}
	}
}

// O-01, the load-bearing row (PV1). Four refusals from one address stay under
// the limiter's ceiling, so they are 401s and not 429s, and the same address
// then scrapes with the real credential.
func TestMetrics_AnUnauthenticatedScrapeIsRefusedAndTheCredentialStillWorks(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	token := rig.mint()
	addr := "192.0.2.20:1000"

	requireRefusal(t, "no credential", rig.scrape(addr, ""), http.StatusUnauthorized)
	requireRefusal(t, "a wrong bearer", rig.scrape(addr, "Bearer rsc_"+strings.Repeat("A", 43)), http.StatusUnauthorized)
	requireRefusal(t, "a malformed Authorization header", rig.scrape(addr, "Bearer"), http.StatusUnauthorized)
	requireRefusal(t, "a wrong basic_auth password", rig.scrape(addr, basic("prometheus", "not-the-token")), http.StatusUnauthorized)

	for name, header := range map[string]string{
		"a bearer":                     "Bearer " + token,
		"basic_auth, any username":     basic("prometheus", token),
		"basic_auth, empty username":   basic("", token),
		"basic_auth, another username": basic("whoever-this-is", token),
	} {
		rec := rig.scrape(addr, header)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d %q, want 200", name, rec.Code, rec.Body.String())
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != scrapeContentType {
			t.Errorf("%s: Content-Type = %q, want %q", name, got, scrapeContentType)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", name, got)
		}
		if samples := parseExposition(t, rec.Body.String()); len(samples["retnd_process_info"]) == 0 {
			t.Errorf("%s: the body parses but carries no retnd_process_info, so it is not this product's exposition", name)
		}
	}
}

// O-01, the rate-limit half. The eleventh refusal inside a minute from one
// address is 429 with Retry-After and an empty body, and from then on a valid
// token from that address is 429 too; a clean address is never limited.
func TestMetrics_TheEleventhRefusalFromOneAddressIsRateLimitedAndACleanAddressIsNot(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	token := rig.mint()
	bad := "192.0.2.30:1000"

	for i := 1; i <= 10; i++ {
		requireRefusal(t, "refusal "+string(rune('0'+i%10)), rig.scrape(bad, "Bearer wrong-"+strings.Repeat("x", i)), http.StatusUnauthorized)
	}
	limited := rig.scrape(bad, "Bearer wrong-again")
	requireRefusal(t, "the eleventh refusal", limited, http.StatusTooManyRequests)
	if got := limited.Header().Get("Retry-After"); got == "" || got == "0" {
		t.Errorf("the 429 carries Retry-After %q, want a positive number of seconds", got)
	}
	requireRefusal(t, "a valid token from the limited address", rig.scrape(bad, "Bearer "+token), http.StatusTooManyRequests)

	for i := 0; i < 30; i++ {
		if rec := rig.scrape("192.0.2.31:1000", "Bearer "+token); rec.Code != http.StatusOK {
			t.Fatalf("scrape %d from a clean address = %d, want 200: a 15 second scraper must never trip the limiter", i+1, rec.Code)
		}
	}
}

// O-25. The two credentials never cross, in either direction, and each works
// on its own route (the positive controls).
func TestMetrics_TheScrapeCredentialAndTheAgentTokenNeverCross(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	scrapeToken := rig.mint()
	agent, err := rig.svc.MintAgentToken(t.Context(), service.MintAgentTokenRequest{Name: "triage", Scope: service.AgentScopeRead, Owner: "alice"})
	if err != nil {
		t.Fatalf("minting an agent token: %v", err)
	}

	addr := "192.0.2.40:1000"
	requireRefusal(t, "an rat_ agent token as a bearer on /metrics", rig.scrape(addr, "Bearer "+agent.Token), http.StatusUnauthorized)
	requireRefusal(t, "an rat_ agent token as the basic_auth password on /metrics", rig.scrape(addr, basic("agent", agent.Token)), http.StatusUnauthorized)

	// The scrape credential on /api/v1, through a router with no session
	// wired, so a 200 could only come from the credential being accepted.
	for _, path := range []string{"/api/v1/system/version", "/api/v1/settings/observability", "/api/v1/agent/tools"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = addr
		req.Header.Set("Authorization", "Bearer "+scrapeToken)
		if rec := serve(rig.public, req); rec.Code != http.StatusUnauthorized {
			t.Errorf("the scrape credential on %s = %d %s, want 401", path, rec.Code, rec.Body.String())
		}
	}
	mcp := httptest.NewRequest(http.MethodPost, "/api/v1/agent/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	mcp.RemoteAddr = addr
	mcp.Header.Set("Authorization", "Bearer "+scrapeToken)
	mcp.Header.Set("Content-Type", "application/json")
	if rec := serve(rig.public, mcp); rec.Code != http.StatusUnauthorized {
		t.Errorf("the scrape credential on the agent MCP endpoint = %d %s, want 401", rec.Code, rec.Body.String())
	}

	// Positive controls: each credential on its own route.
	if rec := rig.scrape("192.0.2.41:1000", "Bearer "+scrapeToken); rec.Code != http.StatusOK {
		t.Errorf("the scrape credential on /metrics = %d, want 200", rec.Code)
	}
	read := httptest.NewRequest(http.MethodGet, "/api/v1/system/version", nil)
	read.RemoteAddr = addr
	read.Header.Set("Authorization", "Bearer "+agent.Token)
	if rec := serve(rig.public, read); rec.Code != http.StatusOK {
		t.Errorf("the agent token on a catalogue read = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// failingAudit makes every agent call's audit row fail, which is what a full
// disk does to the trail. A scrape must not depend on it.
type failingAuditBackend struct{ *service.BackupService }

func (failingAuditBackend) RecordAgentCall(_ context.Context, _ service.AgentCall) error {
	return os.ErrClosed
}

// O-25, third red: a scrape still answers while the agent audit trail is
// unavailable, because /metrics is not routed through auditAgentCalls.
func TestMetrics_AScrapeStillAnswersWhileTheAgentAuditTrailIsUnavailable(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	token := rig.mint()
	router := NewRouter(RouterConfig{
		Platform: noAuthWiredAdapter{}, Backend: failingAuditBackend{rig.svc}, Gate: alwaysPassGate{}, BinaryVersion: "test", Commit: "test",
	})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "192.0.2.50:1000"
	req.Header.Set("Authorization", "Bearer "+token)
	if rec := serve(router, req); rec.Code != http.StatusOK {
		t.Errorf("a scrape with the audit trail unavailable = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// O-03. Nothing on disk holds the plaintext: a byte scan of every file under
// the state directory after a mint, a rotate and a scrape. The credential's
// store is a mode-0600 file in a mode-0700 directory, it holds a SHA-256
// digest and the eight-character prefix, and minting again replaces the
// credential at once.
func TestMetrics_NoPlaintextScrapeTokenIsPersistedAnywhere(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	first := rig.mint()
	second := rig.mint()
	if rec := rig.scrape("192.0.2.60:1000", "Bearer "+second); rec.Code != http.StatusOK {
		t.Fatalf("the rotated credential = %d, want 200", rec.Code)
	}
	requireRefusal(t, "the replaced credential", rig.scrape("192.0.2.61:1000", "Bearer "+first), http.StatusUnauthorized)

	digest := sha256.Sum256([]byte(second))
	var storeFiles []string
	err := filepath.Walk(rig.stateOf, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, plaintext := range []string{first, second} {
			if strings.Contains(string(raw), plaintext) {
				t.Errorf("%s holds a plaintext scrape token", path)
			}
		}
		if strings.Contains(string(raw), hex.EncodeToString(digest[:])) {
			storeFiles = append(storeFiles, path)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(storeFiles) != 1 {
		t.Fatalf("the digest is held by %d files (%v), want exactly one store", len(storeFiles), storeFiles)
	}
	info, err := os.Stat(storeFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.Stat(filepath.Dir(storeFiles[0]))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
		t.Errorf("the store is %v in a %v directory, want 0600 in 0700", info.Mode().Perm(), dir.Mode().Perm())
	}
	if !strings.Contains(mustRead(t, storeFiles[0]), second[:8]) {
		t.Errorf("the store does not keep the eight-character display prefix")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(raw)
}

// O-03, the mint rule: over plain HTTP from another machine the credential
// is refused INSECURE_TRANSPORT_ACK_REQUIRED unless the request acknowledges
// it, the rule an agent-token mint has.
func TestMetrics_AMintOverPlainHTTPFromAnotherMachineNeedsTheAcknowledgement(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	mint := func(ack bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/observability/scrape-token", nil)
		req.RemoteAddr = "192.0.2.70:1000"
		if ack {
			req.Header.Set(InsecureTransportHeader, "acknowledged")
		}

		return serve(rig.admin, csrfPaired(req))
	}
	if rec := mint(false); rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "INSECURE_TRANSPORT_ACK_REQUIRED" {
		t.Errorf("an unacknowledged remote mint = %d %s, want 403 INSECURE_TRANSPORT_ACK_REQUIRED", rec.Code, rec.Body.String())
	}
	if rec := mint(true); rec.Code != http.StatusCreated {
		t.Errorf("an acknowledged remote mint = %d %s, want 201", rec.Code, rec.Body.String())
	}
}

// O-03: no agent token can mint a scrape credential.
func TestMetrics_AnAgentTokenCannotMintAScrapeCredential(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	agent, err := rig.svc.MintAgentToken(t.Context(), service.MintAgentTokenRequest{Name: "triage", Scope: service.AgentScopeConfigure, Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/observability/scrape-token", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Authorization", "Bearer "+agent.Token)
	rec := serve(rig.admin, req)
	if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_ROUTE_DENIED" {
		t.Errorf("an agent token minting a scrape credential = %d %s, want 403 AGENT_ROUTE_DENIED", rec.Code, rec.Body.String())
	}
}

// O-04. The scrape writes no action-log row: fifty scrapes leave the log
// exactly as it was, with a control that a request inside the group does
// write one (the same recorder, the same router).
func TestMetrics_FiftyScrapesWriteNoActionLogRow(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	token := rig.mint()
	before := len(rig.actions.recorded())
	if before == 0 {
		t.Fatal("the control is dead: minting a credential through /api/v1 recorded no action, so this test could not see a scrape being recorded either")
	}
	for i := 0; i < 50; i++ {
		if rec := rig.scrape("192.0.2.80:1000", "Bearer "+token); rec.Code != http.StatusOK {
			t.Fatalf("scrape %d = %d, want 200", i+1, rec.Code)
		}
	}
	for i := 0; i < 5; i++ {
		rig.scrape("192.0.2.81:1000", "Bearer nope")
	}
	if after := len(rig.actions.recorded()); after != before {
		t.Errorf("the action log grew from %d to %d rows over 55 scrapes", before, after)
	}
}

// O-04, the counter. Refusals are counted by reason over exactly three words,
// and no label value is derived from the request.
func TestMetrics_RefusalsAreCountedOverAClosedVocabularyAndNeverByRequestValue(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	token := rig.mint()
	canary := "198.51.100.77"
	for i := 0; i < 3; i++ {
		rig.scrape(canary+":4000", "Bearer wrong-"+canary)
	}
	post := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	post.RemoteAddr = "192.0.2.90:1000"
	post.Header.Set("Authorization", "Bearer "+token)
	if rec := serve(rig.public, post); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /metrics with a valid token = %d, want 405", rec.Code)
	}

	rec := rig.scrape("192.0.2.91:1000", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape = %d", rec.Code)
	}
	samples := parseExposition(t, rec.Body.String())["retnd_scrape_rejected_total"]
	got := map[string]string{}
	label := regexp.MustCompile(`^retnd_scrape_rejected_total\{reason="([^"]*)"\} (\S+)$`)
	for _, line := range samples {
		m := label.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("the counter line %q is not retnd_scrape_rejected_total{reason=...} and a value: one label, and it is the reason", line)
		}
		got[m[1]] = m[2]
	}
	if len(got) != 3 || got["disabled"] != "0" || got["unauthenticated"] != "3" || got["method"] != "1" {
		t.Errorf("the counter reads %v, want exactly disabled=0 unauthenticated=3 method=1", got)
	}
	if strings.Contains(rec.Body.String(), canary) || strings.Contains(rec.Body.String(), "wrong-") {
		t.Error("a value the scraper sent reached the exposition")
	}
}

// workflowStub stands in for the one thing a real service cannot show here:
// a process that built no workflow engine, whose WorkflowMetrics is "" on
// purpose. Everything else is the real service.
type workflowStub struct {
	*service.BackupService
	text string
}

func (w workflowStub) WorkflowMetrics() string { return w.text }

// O-05. The workflow exposition is absent, not zero, when no engine was ever
// built, and present when one was. The control matters as much as the
// assertion: a build that always omits it would pass the first half.
func TestMetrics_TheWorkflowExpositionIsAbsentNotZeroWhenNoEngineWasBuiltAndPresentWhenOneWas(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	token := rig.mint()
	const families = "# HELP retnd_workflow_runs_total Workflow runs finished.\n# TYPE retnd_workflow_runs_total counter\nretnd_workflow_runs_total{backup_set=\"production/alpha\",status=\"succeeded\",skipped=\"false\"} 4\n"

	scrapeWith := func(text string) string {
		router := NewRouter(RouterConfig{
			Platform: noAuthWiredAdapter{}, Backend: workflowStub{BackupService: rig.svc, text: text}, Gate: alwaysPassGate{}, BinaryVersion: "test", Commit: "test",
		})
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = "192.0.2.100:1000"
		req.Header.Set("Authorization", "Bearer "+token)
		rec := serve(router, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("scrape = %d %s", rec.Code, rec.Body.String())
		}
		parseExposition(t, rec.Body.String())

		return rec.Body.String()
	}

	without := scrapeWith("")
	if strings.Contains(without, "retnd_workflow_runs_total") || strings.Contains(without, "workflow_step") {
		t.Errorf("a process that runs no workflows rendered workflow families at zero:\n%s", without)
	}
	if !strings.Contains(without, "retnd_scrape_rejected_total") {
		t.Errorf("the one family this issue adds is missing from the exposition")
	}

	with := scrapeWith(families)
	if !strings.Contains(with, `retnd_workflow_runs_total{backup_set="production/alpha",status="succeeded",skipped="false"} 4`) {
		t.Errorf("a process that built an engine lost its workflow families:\n%s", with)
	}
	if !strings.Contains(with, "retnd_process_info") {
		t.Errorf("the workflow half replaced the health half instead of following it")
	}
}

// O-06. The health report is cached: N scrapes inside min_rebuild_interval
// read one report, and the interval is what ends it. The report's own
// generated-at timestamp is the evidence, read off the exposition.
func TestMetrics_TheHealthReportIsCachedForTheRebuildInterval(t *testing.T) {
	rig := newScrapeRig(t, "observability:\n  metrics:\n    enabled: true\n    min_rebuild_interval: 2s\n")
	token := rig.mint()
	stamp := func() string {
		rec := rig.scrape("192.0.2.110:1000", "Bearer "+token)
		if rec.Code != http.StatusOK {
			t.Fatalf("scrape = %d", rec.Code)
		}
		lines := parseExposition(t, rec.Body.String())["retnd_report_generated_timestamp_seconds"]
		if len(lines) != 1 {
			t.Fatalf("the exposition carries %d retnd_report_generated_timestamp_seconds samples, want one", len(lines))
		}

		return lines[0]
	}
	first := stamp()
	for i := 0; i < 9; i++ {
		if got := stamp(); got != first {
			t.Fatalf("scrape %d rebuilt the report inside the interval: %q then %q", i+2, first, got)
		}
	}
	time.Sleep(3 * time.Second)
	if later := stamp(); later == first {
		t.Errorf("the report was not rebuilt after the interval elapsed (still %q): a frozen cache is not a cache", first)
	}
}

// O-02, the exposition third (the corpus over all three surfaces is #903's):
// the credential the endpoint is guarded by, its digest and what a refused
// scraper presented appear in no metric name, label name, label value or
// HELP line.
func TestMetrics_TheExpositionCarriesNoSecret(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	token := rig.mint()
	digest := sha256.Sum256([]byte(token))
	rig.scrape("192.0.2.120:1000", "Bearer attacker-supplied-"+token[4:20])
	rec := rig.scrape("192.0.2.121:1000", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape = %d", rec.Code)
	}
	body := rec.Body.String()
	for what, secret := range map[string]string{
		"the credential":                token,
		"the credential without prefix": token[4:],
		"the digest":                    hex.EncodeToString(digest[:]),
		"what a refused scraper sent":   "attacker-supplied-",
	} {
		if strings.Contains(body, secret) {
			t.Errorf("the exposition carries %s", what)
		}
	}
	parseExposition(t, body)
}
