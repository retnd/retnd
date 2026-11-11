package serve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// U1.2 (#1345): /api/v1/auth/* is mounted beside the API router, not inside
// it, so the router's own denied-prefix check never sees it. A bearer token
// presented there has to be refused AGENT_ROUTE_DENIED by the mount itself,
// whatever the token is.
//
// Falsify: mount the auth routes without the bearer refusal.
func TestABearerOnTheAuthMountIsRefusedAndACookieIsNot(t *testing.T) {
	reached := 0
	auth := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusNoContent)
	})
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	mux := newMux(engineMounts(api, auth))

	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/enroll", "/api/v1/auth/logout", "/api/v1/auth/step-up"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer rat_whatever_0000000000000000000000000000")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AGENT_ROUTE_DENIED") {
			t.Errorf("%s with a bearer = %d %s, want 403 AGENT_ROUTE_DENIED", path, rec.Code, rec.Body.String())
		}
	}
	if reached != 0 {
		t.Fatalf("a bearer request reached the auth routes %d times", reached)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || reached != 1 {
		t.Errorf("a request with no bearer = %d, reached %d; want it to pass untouched", rec.Code, reached)
	}
}
