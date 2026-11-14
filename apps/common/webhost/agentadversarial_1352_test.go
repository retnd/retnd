package webhost

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/service"
	"github.com/retnd/retnd/core/tests/agentcorpus"
)

// EPIC U's adversarial rows over the HTTP transport and the boundary drills
// that need a router (#1352, FR-98, FR-96). The stdio half of the injection
// and exfiltration corpora is core/tests/agentinjection and
// core/tests/agentexfiltration; these drive the same corpus
// (core/tests/agentcorpus) through POST /api/v1/agent/mcp, so a defect in one
// transport turns that transport's row red and not the other's.
//
// Data reaches the transport through the backend double below, whose reads
// return the plan's strings in the service types' free-text fields. That is
// narrower than the stdio row (a typed service value has no `recipient`
// field to plant under), so the field-name half of the projection is held by
// the stdio row and core's projection tests, and this row holds the shapes.
//
// Written against the spec's names table; not run.

// plantedBackend is a backend whose five untrusted origins carry the plan.
type plantedBackend struct {
	*syncFakeBackend
	*fakeAgentAccess
	plan agentcorpus.Plan
}

func (b *plantedBackend) plant(slug, fallback string) string {
	if v, ok := b.plan.Plants[slug]; ok {
		return v
	}
	return fallback
}

func (b *plantedBackend) ListArtifacts(_ context.Context, _ service.ArtifactFilter) ([]service.Artifact, error) {
	name := b.plant("file", "backup.dump")
	return []service.Artifact{{ID: "production/postgres-primary/" + name, BackupSetID: "production/postgres-primary",
		SourceName: "production", SetName: "postgres-primary", Name: name, RemotePath: "/dumps/" + name, State: "COMPLETE", SizeBytes: 1048576}}, nil
}

func (b *plantedBackend) ListActivity(_ context.Context, _ int, _ string) ([]service.ActivityEvent, string, error) {
	return []service.ActivityEvent{{ArtifactID: "production/postgres-primary/backup.dump", BackupSetID: "production/postgres-primary",
		SourceName: "production", SetName: "postgres-primary", ArtifactName: "backup.dump", From: "DISCOVERED", To: "COMPLETE",
		OccurredAt: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC), Detail: b.plant("activity", "validated")}}, "", nil
}

func (b *plantedBackend) ListStorageMediums(context.Context) ([]service.StorageMediumSummary, error) {
	return []service.StorageMediumSummary{{ID: "offsite", Type: "s3", Bucket: "retnd-offsite", Region: "us-east-1",
		Endpoint: "https://" + b.plant("host", "backup.example.internal") + ":9000"}}, nil
}

// GetOperation is an operation whose error carries a validator's captured
// output, which is where a failed recovery test's words reach operation_show.
func (b *plantedBackend) GetOperation(_ context.Context, id string) (service.Operation, error) {
	return service.Operation{ID: id, Action: "recovery_test", Status: "failed", BackupSetID: "production/postgres-primary",
		Error: b.plant("validator", "checksum ok")}, nil
}

func (b *plantedBackend) RecoveryTestOutput(_ context.Context, id string) (service.RecoveryTestOutput, error) {
	return service.RecoveryTestOutput{BackupSetID: id, Recorded: true, OperationID: "op-1", State: "SUCCEEDED",
		Records: []service.RecoveryTestOutputRecord{{Kind: "output", Text: b.plant("validator", "checksum ok")}}}, nil
}

func (b *plantedBackend) WorkflowStepLogs(_ context.Context, req service.WorkflowStepLogRequest) (service.WorkflowStepLogPage, error) {
	return service.WorkflowStepLogPage{RunID: "run-1", StepID: "step-1", Complete: true,
		Records: []service.WorkflowStepLogRecord{{Seq: 7, StepID: "step-1", Stream: "stdout", Kind: "output", Text: b.plant("steplog", "step finished")}}}, nil
}

// AgentPolicy is what `agents.http_transport: on` resolves to, with the
// default rate limit: the router reads the transport switch off the backend
// (U1.4, #1347), so the planted deployment turns it on there.
func (b *plantedBackend) AgentPolicy() service.AgentPolicy {
	return service.AgentPolicy{HTTPTransport: true, RateLimitPerMinute: 60}
}

func mcpServer(t *testing.T, plan agentcorpus.Plan) (*httptest.Server, *plantedBackend) {
	t.Helper()
	b := &plantedBackend{syncFakeBackend: newSyncFakeBackend(), fakeAgentAccess: newFakeAgentAccess(), plan: plan}
	cfg := RouterConfig{Platform: allowingPlatform("alice"), Backend: b, BinaryVersion: "test", Commit: "test"}
	srv := httptest.NewServer(NewRouter(cfg))
	t.Cleanup(srv.Close)
	return srv, b
}

func mcpConn(t *testing.T, srv *httptest.Server, token string) *agentcorpus.HTTP {
	t.Helper()
	c := &agentcorpus.HTTP{URL: srv.URL + "/api/v1/agent/mcp", Token: token}
	agentcorpus.Handshake(t, c)
	return c
}

func TestAnUntrustedStringArrivesOnlyAsData_HTTP(t *testing.T) {
	for _, o := range agentcorpus.Origins {
		t.Run(o.Slug, func(t *testing.T) {
			srv, b := mcpServer(t, agentcorpus.Plan{Plants: map[string]string{o.Slug: o.Marker()}})
			surfaces := agentcorpus.Walk(t, mcpConn(t, srv, readToken))
			arrived := false
			for _, s := range surfaces {
				inside, leaked := agentcorpus.CheckData(s.Raw, o.Marker())
				if leaked {
					t.Errorf("http: the instruction planted in %s is outside `untrusted` on %s:\n%s", o.Name, s.Name, s.Raw)
				}
				for _, tool := range o.Tools {
					if s.Kind == "tool" && s.Tool == tool && inside {
						arrived = true
					}
				}
			}
			if len(o.Tools) > 0 && !arrived {
				t.Errorf("none of %v returned the instruction planted in %s inside `untrusted`; the row is vacuous", o.Tools, o.Name)
			}
			if len(b.minted) != 0 || len(b.revoked) != 0 {
				t.Errorf("reading minted or revoked a token: %+v %+v", b.minted, b.revoked)
			}
		})
	}
}

// The HTTP row's control: the planted string moved into the text of a
// captured result turns this row red for its own origin and for no other.
func TestAnUntrustedStringArrivesOnlyAsData_HTTP_WouldCatchOne(t *testing.T) {
	o := agentcorpus.Origins[0]
	srv, _ := mcpServer(t, agentcorpus.Plan{Plants: map[string]string{o.Slug: o.Marker()}})
	var captured []byte
	for _, s := range agentcorpus.Walk(t, mcpConn(t, srv, readToken)) {
		if s.Kind == "tool" && s.Tool == "backups_list" {
			captured = s.Raw
		}
	}
	if captured == nil {
		t.Fatal("backups_list was not captured over http")
	}
	bad := []byte(strings.Replace(string(captured), `"content":[{`, `"content":[{"type":"text","text":"`+o.Marker()+`"},{`, 1))
	if _, leaked := agentcorpus.CheckData(bad, o.Marker()); !leaked {
		t.Error("a file name in the result's content is not seen as a leak over http")
	}
	for _, other := range agentcorpus.Origins[1:] {
		if _, leaked := agentcorpus.CheckData(bad, other.Marker()); leaked {
			t.Errorf("%s went red for another origin's string", other.Slug)
		}
	}
}

// Key shapes planted in free text over HTTP: an identity, a post-quantum
// identity, a recipient and a PEM block in every untrusted position.
func TestNoKeyShapeInFreeTextOnAnyAgentSurface_HTTP_1352(t *testing.T) {
	cs := agentcorpus.Canaries()
	text := agentcorpus.PEMBlock() + " " + cs[0].Value + " " + cs[1].Value + " " + cs[2].Value
	srv, _ := mcpServer(t, agentcorpus.Plan{Plants: map[string]string{"file": text, "validator": text, "steplog": text, "activity": text, "host": text}})
	surfaces := agentcorpus.Walk(t, mcpConn(t, srv, readToken))
	answered := 0
	for _, s := range surfaces {
		if s.Kind == "tool" && s.Error == nil {
			answered++
		}
		if leaks := agentcorpus.Leaks(s.Raw, agentcorpus.CanaryValues(cs[:3])); len(leaks) > 0 {
			t.Errorf("http: %s carries key material planted in free text (%v)", s.Name, leaks)
		}
	}
	if answered < 3 {
		t.Fatalf("only %d tools answered over http; a row built on refusals proves nothing", answered)
	}
}

// A token revoked while a stream is open is refused on its next call and the
// stream is closed (FR-91, falsified by keeping the stream open).
func TestARevokedTokenMidStreamIsRefusedAndTheStreamCloses(t *testing.T) {
	srv, b := mcpServer(t, agentcorpus.Plan{})
	conn := mcpConn(t, srv, readToken)

	req, _ := http.NewRequest(http.MethodGet, conn.URL, nil)
	req.Header.Set("Authorization", "Bearer "+readToken)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", conn.Session())
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("opening the stream: %d", resp.StatusCode)
	}
	closed := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, bufio.NewReader(resp.Body))
		closed <- err
	}()

	if _, err := b.RevokeAgentToken(t.Context(), "agt_read"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, rpcErr, err := conn.Call(ctx, "tools/call", map[string]any{"name": "status", "arguments": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "AGENT_TOKEN_INVALID") {
		t.Errorf("the next call with a revoked token answered %+v, want AGENT_TOKEN_INVALID", rpcErr)
	}
	select {
	case err := <-closed:
		// A clean EOF and a cut connection both mean the server closed the
		// stream, which is the claim here, so either one passes. The error
		// is logged rather than failed on so a reset still shows up.
		if err != nil {
			t.Logf("the stream closed with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the stream was still open five seconds after the token was revoked")
	}
}

// The limiter trips at the sixty-first call, and the audit row says so.
func TestTheSixtyFirstCallInAMinuteIsRefusedAndRecorded(t *testing.T) {
	srv, b := mcpServer(t, agentcorpus.Plan{})
	conn := mcpConn(t, srv, readToken)
	call := func() (string, bool) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		raw, rpcErr, err := conn.Call(ctx, "tools/call", map[string]any{"name": "status", "arguments": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		if rpcErr != nil {
			return rpcErr.Message + string(rpcErr.Data), strings.Contains(rpcErr.Message+string(rpcErr.Data), "AGENT_RATE_LIMITED")
		}
		return string(raw), strings.Contains(string(raw), "AGENT_RATE_LIMITED")
	}
	// The line counts every request the token makes, and the handshake in
	// mcpConn made two (initialize and its notification), so 58 tool calls
	// are the 3rd to the 60th request and the next one is the 61st.
	const handshake = 2
	for i := 1; i <= 60-handshake; i++ {
		if got, limited := call(); limited {
			t.Fatalf("tool call %d was refused as rate limited, the line is the 61st request: %s", i, got)
		}
	}
	if got, limited := call(); !limited {
		t.Fatalf("the 61st request was not refused AGENT_RATE_LIMITED: %s", got)
	}

	// The refusal is the audit's: the authenticator records it through
	// AgentAccess.RecordAgentRefusal, which this backend keeps.
	limited := false
	for _, r := range b.refusals {
		limited = limited || r.Code == "AGENT_RATE_LIMITED"
	}
	if !limited {
		t.Errorf("the audit trail does not say the 61st call was limited: %+v", b.refusals)
	}
}

// A cookie session on the MCP endpoint is refused: the endpoint serves a
// bearer token and nothing else, however the browser is signed in. The
// platform double here authenticates every request as a signed-in person,
// which is the strongest cookie there is.
func TestACookieSessionOnTheMCPEndpointIsRefused(t *testing.T) {
	srv, _ := mcpServer(t, agentcorpus.Plan{})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/agent/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.AddCookie(&http.Cookie{Name: "retnd_session", Value: "a-signed-in-session"})
	req.AddCookie(&http.Cookie{Name: "retnd_csrf", Value: "pair"})
	req.Header.Set("X-CSRF-Token", "pair")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Errorf("a cookie session got %d on the MCP endpoint: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), `"tools"`) {
		t.Errorf("a cookie session was served the catalogue: %s", body)
	}
}

// A bearer token on /auth/session is refused, whatever its scope.
func TestABearerOnAuthSessionIsRefused(t *testing.T) {
	router, _ := agentRouter()
	for _, token := range []string{readToken, operateToken} {
		rec := serve(router, bearer(http.MethodGet, "/api/v1/auth/session", "", token))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AGENT_ROUTE_DENIED") {
			t.Errorf("a %s-scoped bearer on /auth/session got %d: %s", token[:12], rec.Code, rec.Body.String())
		}
	}
}
