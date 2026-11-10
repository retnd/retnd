package webhost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
)

// EPIC T, T1.5 (#1315): the suite listing's route and its verb are one
// fact. The route answers with the contract's body, the echo for it is
// `retnd encryption suites`, the contract declares the route with that
// response schema, and the route-to-verb guard counts it as answered.
// Written by reading; not run.
func TestSuitesRouteAndVerbAgree(t *testing.T) {
	b := newEncryptionFakeBackend()
	router := encryptionRouter(b, alwaysPassGate{}, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodGet, "/api/v1/encryption/suites", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/encryption/suites = %d %s", rec.Code, rec.Body.String())
	}
	var got apicontract.ListEncryptionSuitesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Suites) == 0 {
		t.Fatalf("the route's body is not the contract's: %v\n%s", err, rec.Body.String())
	}

	line := cliecho.Echo(cliecho.Action{Method: http.MethodGet, Route: "/encryption/suites"})
	if line.Gap != "" {
		t.Fatalf("the route has no verb: %s", line.Gap)
	}
	if want := cliecho.Binary + " encryption suites"; line.Shell() != want {
		t.Errorf("the echo is %q, want %q", line.Shell(), want)
	}

	declared := false
	for _, e := range apicontract.Endpoints {
		if e.Method == http.MethodGet && e.Path == "/encryption/suites" {
			declared = e.ResponseSchema == "ListEncryptionSuitesResponse"
		}
	}
	if !declared {
		t.Error("the contract does not declare GET /encryption/suites answering ListEncryptionSuitesResponse")
	}
	if !answeredSet()["GET /encryption/suites"] {
		t.Error("the route-to-verb guard does not count GET /encryption/suites as answered")
	}

	// Without encryption the route answers the declared refusal.
	bare := encryptionRouter(newSyncFakeBackend(), alwaysPassGate{}, nil)
	rec = httptest.NewRecorder()
	bare.ServeHTTP(rec, validatorRequest(http.MethodGet, "/api/v1/encryption/suites", "", ""))
	if rec.Code != http.StatusServiceUnavailable || responseErrorCode(rec.Body.String()) != "ENCRYPTION_UNAVAILABLE" {
		t.Errorf("got %d %s, want 503 ENCRYPTION_UNAVAILABLE, which the contract now declares", rec.Code, rec.Body.String())
	}
}
