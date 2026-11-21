package webhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// Issue #1611, the API half: a FAILED backup whose own copy sits at its final
// name, whose source is still there and whose copy does not match it. The
// engine refuses the retry by name with RETRY_OWN_COPY_MISMATCH. The browser,
// the terminal and an agent have to read the same code.

func mismatchRefusal() error {
	return &service.RetryRefusal{Code: "RETRY_OWN_COPY_MISMATCH",
		Sentence: "This backup's own copy does not match the object on the source. Move that file aside and retry."}
}

func TestTheRetryRouteAnswersAMismatchedOwnCopyByName_1611(t *testing.T) {
	rt := newReadSurfaceRouter(t)
	rt.backend.errOnRetryFailed = mismatchRefusal()

	rec := rt.post(t, ownCopyRetryPath, "")
	mustStatus(t, rec, http.StatusConflict)
	if got := responseErrorCode(rec.Body.String()); got != "RETRY_OWN_COPY_MISMATCH" {
		t.Errorf("error code = %q, want RETRY_OWN_COPY_MISMATCH; body: %s", got, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Move that file aside") {
		t.Errorf("the answer does not carry the engine's sentence: %s", rec.Body.String())
	}
}

func TestAnAgentsReplayedRetryReadsTheMismatchRefusal_1611(t *testing.T) {
	rt := newReadSurfaceRouter(t)
	rt.backend.errOnRetryFailed = mismatchRefusal()
	req := httptest.NewRequest(http.MethodPost, ownCopyRetryPath, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	attachValidCSRF(req)
	req = req.WithContext(context.WithValue(req.Context(), replayContextKey{}, service.AgentPrincipal{Name: "triage"}))
	rec := httptest.NewRecorder()
	rt.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || responseErrorCode(rec.Body.String()) != "RETRY_OWN_COPY_MISMATCH" {
		t.Errorf("replayed retry = %d %s, want 409 RETRY_OWN_COPY_MISMATCH", rec.Code, rec.Body.String())
	}
}
