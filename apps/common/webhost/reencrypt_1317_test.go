package webhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1317 over HTTP: the two routes behind `retnd encryption reencrypt`. The dry
// run is a GET (it changes nothing), the run is a CSRF-protected POST that
// answers 202 with the operation to follow or cancel, and a backend with no
// encryption answers the same 503 the other encryption routes do.
//
// They were written by reading the router and the existing encryption route
// tests and have not been run.

// The fake answers the dry run with two groups, so the route test can check
// the grouping survives the wire exactly as the service built it.
func (f *encryptionFakeBackend) PlanReencrypt(_ context.Context, req apicontract.StartReencryptRequest) (apicontract.ReencryptPlan, error) {
	plan := apicontract.ReencryptPlan{
		Groups: []apicontract.ReencryptGroup{
			{SuiteID: "age/v1", KeyID: "kold", Candidates: []apicontract.ReencryptCandidate{
				{ArtifactID: "src/set-1/a.dump", SourceMedium: "warm", DestinationMedium: "offsite", Verdict: "would_reencrypt"},
			}},
			{SuiteID: "age/v1", KeyID: "knew", Candidates: []apicontract.ReencryptCandidate{
				{ArtifactID: "src/set-1/b.dump", SourceMedium: "warm", DestinationMedium: "offsite", Verdict: "refused", Refusal: "already_current", Reason: "already under the active suite and key"},
			}},
		},
		WouldReencrypt: 1, Refused: 1,
	}
	return plan, f.note(fmt.Sprintf("plan-reencrypt suite=%s key=%s deprecated=%t to=%s", req.SuiteID, req.KeyID, req.Deprecated, req.DestinationMedium))
}

func (f *encryptionFakeBackend) StartReencrypt(_ context.Context, req apicontract.StartReencryptRequest) (service.ScrubStarted, error) {
	return service.ScrubStarted{OperationID: "op-17"}, f.note(fmt.Sprintf("reencrypt suite=%s key=%s deprecated=%t to=%s", req.SuiteID, req.KeyID, req.Deprecated, req.DestinationMedium))
}

// The route twin of TestReencryptDryRunGroupsBySuiteAndKey_1317: the dry run
// reaches the backend with the selector and destination the query carried and
// answers the groups by suite and key as they were built. Red if a query value
// is dropped or renamed, or the groups are flattened.
func TestTheReencryptDryRunRouteGroupsBySuiteAndKey_1317(t *testing.T) {
	b := newEncryptionFakeBackend()
	router := encryptionRouter(b, NewProfileGate(profile.Generic), nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodGet, "/api/v1/encryption/reencrypt?suite=age%2Fv1&destination=offsite", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("the dry run: %d %s", rec.Code, rec.Body.String())
	}
	var plan apicontract.ReencryptPlan
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatalf("decoding the plan: %v", err)
	}
	if len(plan.Groups) != 2 || plan.Groups[0].KeyID != "kold" || plan.Groups[1].KeyID != "knew" ||
		plan.Groups[1].Candidates[0].Refusal != "already_current" || plan.Groups[0].Candidates[0].DestinationMedium != "offsite" {
		t.Fatalf("the plan came back as %+v, want the two groups as built", plan)
	}
	if got, want := strings.Join(b.called(), "|"), "plan-reencrypt suite=age/v1 key= deprecated=false to=offsite"; !strings.Contains(got, want) {
		t.Errorf("the backend saw %q, want %q", got, want)
	}
}

// Red until the run is registered and reaches the backend with the values the
// request carried, and answers 202 naming the operation.
func TestTheReencryptRunRouteReachesItsBackendMethod_1317(t *testing.T) {
	b := newEncryptionFakeBackend()
	router := encryptionRouter(b, NewProfileGate(profile.Generic), nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/encryption/reencrypt", `{"deprecated":true,"destination_medium":"offsite"}`, ""))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"operation_id":"op-17"`) {
		t.Fatalf("the run: %d %s, want 202 naming the operation", rec.Code, rec.Body.String())
	}
	if got, want := strings.Join(b.called(), "|"), "reencrypt suite= key= deprecated=true to=offsite"; !strings.Contains(got, want) {
		t.Errorf("the backend saw %q, want %q", got, want)
	}
}

// deprecated is a boolean on the query, and a value that is neither true nor
// false is refused rather than read as one of them. Red if it reaches the
// backend.
func TestTheReencryptDryRunRefusesAMalformedDeprecatedFlag_1317(t *testing.T) {
	b := newEncryptionFakeBackend()
	router := encryptionRouter(b, alwaysPassGate{}, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodGet, "/api/v1/encryption/reencrypt?deprecated=yes", "", ""))
	if rec.Code != http.StatusBadRequest || responseErrorCode(rec.Body.String()) != "INVALID_REQUEST" {
		t.Errorf("got %d %s, want 400 INVALID_REQUEST", rec.Code, rec.Body.String())
	}
	if len(b.called()) != 0 {
		t.Errorf("a malformed flag reached the backend: %v", b.called())
	}
}

// The run is a mutation, so it needs the CSRF header. Red if the POST is
// registered without requireCSRF.
func TestTheReencryptRunNeedsCSRF_1317(t *testing.T) {
	b := newEncryptionFakeBackend()
	router := encryptionRouter(b, alwaysPassGate{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/encryption/reencrypt", strings.NewReader(`{"suite_id":"age/v1","destination_medium":"offsite"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:5000"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a run with no CSRF token: %d, want 403", rec.Code)
	}
	if len(b.called()) != 0 {
		t.Errorf("a run with no CSRF token reached the backend: %v", b.called())
	}
}

// Each refusal keeps its own status and code. Red if a refusal is flattened to
// 500, or a run already going is not a conflict.
func TestReencryptRefusalsMapToTheirOwnStatusAndCode_1317(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"a bad selector or destination", service.ErrInvalidRequest, http.StatusBadRequest, "INVALID_REQUEST"},
		{"a run already going", service.ErrReencryptRunning, http.StatusConflict, "OPERATION_ALREADY_RUNNING"},
		{"a cycle holding the guard", service.ErrOperationAlreadyRunning, http.StatusConflict, "OPERATION_ALREADY_RUNNING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newEncryptionFakeBackend()
			b.err = tc.err
			router := encryptionRouter(b, alwaysPassGate{}, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/encryption/reencrypt", `{"suite_id":"age/v1","destination_medium":"offsite"}`, ""))
			if rec.Code != tc.status || responseErrorCode(rec.Body.String()) != tc.code {
				t.Errorf("got %d %s, want %d %s", rec.Code, rec.Body.String(), tc.status, tc.code)
			}
		})
	}
}

// A body field the contract does not have is refused at the door, so a client
// cannot slip key material or a "skip verify" switch past the decoder.
func TestReencryptRefusesUnknownFields_1317(t *testing.T) {
	b := newEncryptionFakeBackend()
	router := encryptionRouter(b, alwaysPassGate{}, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/encryption/reencrypt",
		`{"suite_id":"age/v1","destination_medium":"offsite","skip_verify":true,"key":"`+bundleMaterial+`"}`, ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	if len(b.called()) != 0 {
		t.Errorf("a request with unknown fields reached the backend: %v", b.called())
	}
	if strings.Contains(rec.Body.String(), bundleMaterial) {
		t.Errorf("the refusal echoes the key: %s", rec.Body.String())
	}
}

// A backend without the re-encrypt methods answers 503 on both routes.
func TestABackendWithoutReencryptAnswersUnavailable_1317(t *testing.T) {
	router := encryptionRouter(newSyncFakeBackend(), alwaysPassGate{}, nil)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/encryption/reencrypt?suite=age%2Fv1", ""},
		{http.MethodPost, "/api/v1/encryption/reencrypt", `{"suite_id":"age/v1","destination_medium":"m"}`},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, validatorRequest(tc.method, tc.path, tc.body, ""))
		if rec.Code != http.StatusServiceUnavailable || responseErrorCode(rec.Body.String()) != "ENCRYPTION_UNAVAILABLE" {
			t.Errorf("%s: got %d %s, want 503 ENCRYPTION_UNAVAILABLE", tc.method, rec.Code, rec.Body.String())
		}
	}
}
