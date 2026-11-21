package webhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// Issue #1594, the API half. A FAILED backup whose own copy sits at its final
// name and whose source is gone gets two answers from the engine: retry keeps
// a copy that still verifies, and refuses one that does not with
// RETRY_OWN_COPY_UNVERIFIED; retire takes that second kind only with the
// operator's accept_unverified_copy acknowledgement. The browser, the
// terminal and an agent have to read the same codes, and an agent may not
// send the acknowledgement. See docs/design/1594-own-copy-source-gone.md.

const ownCopyRetryPath = "/api/v1/backups/production/postgres/bad.dump/retry"
const ownCopyRetirePath = "/api/v1/backups/production/postgres/bad.dump/retire"

func unverifiedCopyRefusal() error {
	return &service.RetryRefusal{Code: "RETRY_OWN_COPY_UNVERIFIED",
		Sentence: "This backup's source is gone and its own copy at the final name could not be verified."}
}

// The retry route answers the engine's refusal under its own code and with
// the engine's sentence, never as a 500.
func TestTheRetryRouteAnswersAnUnverifiableOwnCopyByName_1594(t *testing.T) {
	rt := newReadSurfaceRouter(t)
	rt.backend.errOnRetryFailed = unverifiedCopyRefusal()

	rec := rt.post(t, ownCopyRetryPath, "")
	mustStatus(t, rec, http.StatusConflict)
	if got := responseErrorCode(rec.Body.String()); got != "RETRY_OWN_COPY_UNVERIFIED" {
		t.Errorf("error code = %q, want RETRY_OWN_COPY_UNVERIFIED; body: %s", got, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "could not be verified") {
		t.Errorf("the answer does not carry the engine's sentence: %s", rec.Body.String())
	}
}

// MCP parity: an agent's approved retry is replayed through the same handler,
// so it reads the same code a person does.
func TestAnAgentsReplayedRetryReadsTheSameRefusal_1594(t *testing.T) {
	rt := newReadSurfaceRouter(t)
	rt.backend.errOnRetryFailed = unverifiedCopyRefusal()
	req := httptest.NewRequest(http.MethodPost, ownCopyRetryPath, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	attachValidCSRF(req)
	req = req.WithContext(context.WithValue(req.Context(), replayContextKey{}, service.AgentPrincipal{Name: "triage"}))
	rec := httptest.NewRecorder()
	rt.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || responseErrorCode(rec.Body.String()) != "RETRY_OWN_COPY_UNVERIFIED" {
		t.Errorf("replayed retry = %d %s, want 409 RETRY_OWN_COPY_UNVERIFIED", rec.Code, rec.Body.String())
	}
}

// The acknowledgement travels from the body and from the preview's query to
// the engine, and the terminal line the preview hands back carries it.
func TestTheUnverifiedCopyAcknowledgementReachesTheEngine_1594(t *testing.T) {
	rt := newReadSurfaceRouter(t)
	rec := rt.post(t, ownCopyRetirePath, `{"accept_unverified_copy":true,"no_successor":true}`)
	mustStatus(t, rec, http.StatusOK)
	if got := rt.backend.lastRetireRequest; !got.AcceptUnverifiedCopy || !got.NoSuccessor {
		t.Errorf("the engine was asked %+v, want accept_unverified_copy and no_successor", got)
	}

	rt = newReadSurfaceRouter(t)
	rec = rt.get(t, ownCopyRetirePath+"?accept_unverified_copy=true")
	mustStatus(t, rec, http.StatusOK)
	if !rt.backend.lastRetireRequest.AcceptUnverifiedCopy {
		t.Errorf("the preview asked the engine %+v, want accept_unverified_copy", rt.backend.lastRetireRequest)
	}
	var got struct {
		Command string `json:"command"`
	}
	decodeInto(t, rec, &got)
	if !strings.Contains(got.Command, "--accept-unverified-copy") {
		t.Errorf("the preview's terminal line %q does not carry --accept-unverified-copy", got.Command)
	}
}

// An agent may not send the acknowledgement, in any spelling, and the
// replay guard refuses it on its own too.
func TestAnAgentCannotAcknowledgeAnUnverifiedCopy_1594(t *testing.T) {
	for _, body := range []string{`{"accept_unverified_copy":true}`, `{"Accept_Unverified_Copy":true}`} {
		rig := retireRig(t)
		rec := serve(rig.router, toolCall(http.MethodPost, "/api/v1/backups/src/set-1/dump.tar/retire", body, operateToken, "retire_backup"))
		if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_RETIRE_ACK_REFUSED" {
			t.Errorf("%s = %d %s, want 403 AGENT_RETIRE_ACK_REFUSED", body, rec.Code, rec.Body.String())
		}
		if n := len(rig.backend.created); n != 0 {
			t.Errorf("%s created %d confirmations; a refused proposal must ask nobody", body, n)
		}
	}

	rt := newReadSurfaceRouter(t)
	req := httptest.NewRequest(http.MethodPost, ownCopyRetirePath, strings.NewReader(`{"accept_unverified_copy":true}`))
	req.Header.Set("Content-Type", "application/json")
	attachValidCSRF(req)
	req = req.WithContext(context.WithValue(req.Context(), replayContextKey{}, service.AgentPrincipal{Name: "triage"}))
	rec := httptest.NewRecorder()
	rt.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_RETIRE_ACK_REFUSED" {
		t.Errorf("replayed acknowledgement = %d %s, want 403 AGENT_RETIRE_ACK_REFUSED", rec.Code, rec.Body.String())
	}
	if rt.backend.lastRetired != "" {
		t.Errorf("the engine was asked to retire %q", rt.backend.lastRetired)
	}
}

// The preview passes the new acknowledgement name through as the refusal's
// overridable_with, so the dialog can offer it.
func TestThePreviewNamesTheUnverifiedCopyAcknowledgement_1594(t *testing.T) {
	rt := newReadSurfaceRouter(t)
	rt.backend.previewRefusal = &service.RetireRefusal{Code: "RETIRE_OWN_COPY_AT_FINAL_NAME", Sentence: "the engine's sentence",
		OverridableWith: "accept_unverified_copy"}
	rec := rt.get(t, ownCopyRetirePath)
	mustStatus(t, rec, http.StatusOK)
	var got struct {
		Refusal *struct {
			Code            string `json:"code"`
			OverridableWith string `json:"overridable_with"`
		} `json:"refusal"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Refusal == nil || got.Refusal.Code != "RETIRE_OWN_COPY_AT_FINAL_NAME" || got.Refusal.OverridableWith != "accept_unverified_copy" {
		t.Errorf("refusal = %+v, want RETIRE_OWN_COPY_AT_FINAL_NAME overridable with accept_unverified_copy", got.Refusal)
	}
}

// The preview is a GET that a read-scope token may call, and with
// accept_unverified_copy it hashes the whole copy and runs the set's validator.
// That is the operator's deliberate tick in the dialog, never an agent's: the
// preview refuses the acknowledgements from an agent token, so a read token
// cannot make the engine re-read a big dump on every call.
func TestAnAgentTokenCannotAskThePreviewToReadTheFile_1594(t *testing.T) {
	path := "/api/v1/backups/src/set-1/dump.tar/retire"
	for _, q := range []string{"?accept_unverified_copy=true", "?accept_source_present=true", "?no_successor=true"} {
		rig := retireRig(t)
		rec := serve(rig.router, toolCall(http.MethodGet, path+q, "", readToken, "backup_retirement_show"))
		if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_RETIRE_ACK_REFUSED" {
			t.Errorf("agent GET %s = %d %s, want 403 AGENT_RETIRE_ACK_REFUSED", q, rec.Code, rec.Body.String())
		}
		if got := rig.backend.lastRetireRequest; got.AcceptUnverifiedCopy || got.AcceptSourcePresent || got.NoSuccessor {
			t.Errorf("agent GET %s reached the engine as %+v", q, got)
		}
	}
	rig := retireRig(t)
	if rec := serve(rig.router, toolCall(http.MethodGet, path, "", readToken, "backup_retirement_show")); rec.Code != http.StatusOK {
		t.Errorf("the plain agent preview = %d %s, want 200", rec.Code, rec.Body.String())
	}
}
