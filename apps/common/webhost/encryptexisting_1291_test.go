package webhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1291 over HTTP: the two routes behind `retnd encryption encrypt-existing`.
// The dry run is a GET (it changes nothing), the run is a CSRF-protected POST
// that answers 202 with the operation to follow or cancel, and a backend with
// no encryption answers the same 503 the other encryption routes do.
//
// They were written by reading the router and the existing encryption route
// tests and have not been run.

func (f *encryptionFakeBackend) PlanEncryptExisting(_ context.Context, set, artifact, destination string) (apicontract.EncryptExistingPlan, error) {
	return apicontract.EncryptExistingPlan{Candidates: []apicontract.EncryptExistingCandidate{}}, f.note("plan-existing " + set + " " + artifact + " " + destination)
}

func (f *encryptionFakeBackend) StartEncryptExisting(_ context.Context, req apicontract.StartEncryptExistingRequest) (service.ScrubStarted, error) {
	return service.ScrubStarted{BackupSetID: req.BackupSetID, OperationID: "op-9"}, f.note("encrypt-existing " + req.BackupSetID + " " + req.ArtifactID + " " + req.DestinationMedium)
}

// Red until both routes are registered and reach the backend with the values
// the request carried.
func TestTheEncryptExistingRoutesReachTheirBackendMethods_1291(t *testing.T) {
	b := newEncryptionFakeBackend()
	router := encryptionRouter(b, NewProfileGate(profile.Generic), nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodGet, "/api/v1/encryption/encrypt-existing?set=src/set-1&destination=offsite&artifact=src/set-1/a.dump", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("the dry run: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/encryption/encrypt-existing",
		`{"backup_set_id":"src/set-1","artifact_id":"src/set-1/a.dump","destination_medium":"offsite"}`, ""))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"operation_id":"op-9"`) {
		t.Fatalf("the run: %d %s, want 202 naming the operation", rec.Code, rec.Body.String())
	}
	got := strings.Join(b.called(), "|")
	for _, want := range []string{
		"plan-existing src/set-1 src/set-1/a.dump offsite",
		"encrypt-existing src/set-1 src/set-1/a.dump offsite",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the backend saw %q, want a call %q", got, want)
		}
	}
}

// The run is a mutation, so it needs the CSRF header and the dry run does not.
// Red if the POST is registered without requireCSRF.
func TestTheEncryptExistingRunNeedsCSRFAndTheDryRunDoesNot_1291(t *testing.T) {
	b := newEncryptionFakeBackend()
	router := encryptionRouter(b, alwaysPassGate{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/encryption/encrypt-existing",
		strings.NewReader(`{"backup_set_id":"src/set-1","destination_medium":"offsite"}`))
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

// Each refusal keeps its own status and code, and the message is the service's
// own sentence whole, so the terminal and the browser read the same words.
// Red if a refusal is flattened to 500.
func TestEncryptExistingRefusalsMapToTheirOwnStatusAndCode_1291(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"the set does not require encryption, or a bad input", service.ErrInvalidRequest, http.StatusBadRequest, "INVALID_REQUEST"},
		{"an unknown set", service.ErrBackupSetNotFound, http.StatusNotFound, "BACKUP_SET_NOT_FOUND"},
		{"a run already going for the set", service.ErrEncryptExistingRunning, http.StatusConflict, "OPERATION_ALREADY_RUNNING"},
		{"a key that does not resolve", service.ErrEncryptionKeyUnresolvable, http.StatusConflict, "ENCRYPTION_KEY_UNRESOLVABLE"},
		{"a deployment with no file", service.ErrConfigNotFileBacked, http.StatusServiceUnavailable, "ENCRYPTION_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newEncryptionFakeBackend()
			b.err = tc.err
			router := encryptionRouter(b, alwaysPassGate{}, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/encryption/encrypt-existing",
				`{"backup_set_id":"src/set-1","destination_medium":"offsite"}`, ""))
			if rec.Code != tc.status || responseErrorCode(rec.Body.String()) != tc.code {
				t.Errorf("got %d %s, want %d %s", rec.Code, rec.Body.String(), tc.status, tc.code)
			}
		})
	}
}

// A body field the contract does not have is refused at the door, so a client
// cannot slip key material or a "skip verify" switch past the decoder.
// Red if the request is decoded leniently.
func TestEncryptExistingRefusesUnknownFields_1291(t *testing.T) {
	b := newEncryptionFakeBackend()
	router := encryptionRouter(b, alwaysPassGate{}, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/encryption/encrypt-existing",
		`{"backup_set_id":"src/set-1","destination_medium":"offsite","skip_verify":true,"key":"`+bundleMaterial+`"}`, ""))
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

// A backend without the encrypt-existing methods answers 503, not a panic or a
// 404, on both routes.
func TestABackendWithoutEncryptExistingAnswersUnavailable_1291(t *testing.T) {
	router := encryptionRouter(newSyncFakeBackend(), alwaysPassGate{}, nil)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/encryption/encrypt-existing?set=a/b&destination=m", ""},
		{http.MethodPost, "/api/v1/encryption/encrypt-existing", `{"backup_set_id":"a/b","destination_medium":"m"}`},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, validatorRequest(tc.method, tc.path, tc.body, ""))
		if rec.Code != http.StatusServiceUnavailable || responseErrorCode(rec.Body.String()) != "ENCRYPTION_UNAVAILABLE" {
			t.Errorf("%s: got %d %s, want 503 ENCRYPTION_UNAVAILABLE", tc.method, rec.Code, rec.Body.String())
		}
	}
}
