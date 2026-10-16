package webhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1203 over HTTP: the custom validator routes, what each sends to the
// backend, what each answers, and the gate and step-up on the two that put
// code where the engine runs it.

type validatorFakeBackend struct {
	*syncFakeBackend
	mu      sync.Mutex
	created []service.CreateCustomValidatorRequest
	updated []service.CreateCustomValidatorRequest
	removed []string
	filter  service.CustomValidatorsFilter
	preview []string
	err     error
}

func newValidatorFakeBackend() *validatorFakeBackend {
	return &validatorFakeBackend{syncFakeBackend: newSyncFakeBackend()}
}

func (f *validatorFakeBackend) sample(name string) service.CustomValidator {
	return service.CustomValidator{
		Name: name, Pattern: `^MANIFEST-\d{8}T\d{6}Z\.txt$`, Anchored: true, BackupSetID: "src/set-1", Scope: "backup_set",
		ScriptFile: name + ".local.sh", ScriptPresent: true, ScriptSHA256: strings.Repeat("ab", 32), ScriptSize: 42,
		UploadedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Matches: 3, MatchSets: []string{"src/set-1"},
		ExampleName: "MANIFEST-20260902T153521Z.txt",
		LastResult:  &service.CustomValidatorResult{State: "passed", At: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), Artifact: "src/set-1/MANIFEST-20260902T153521Z.txt"},
	}
}

func (f *validatorFakeBackend) ListCustomValidators(_ context.Context, filter service.CustomValidatorsFilter) (service.CustomValidators, error) {
	f.mu.Lock()
	f.filter = filter
	f.mu.Unlock()
	if f.err != nil {
		return service.CustomValidators{}, f.err
	}
	out := service.CustomValidators{Validators: []service.CustomValidator{f.sample("manifest")}, Directory: "recovery-test-validators", Available: true, MaxValidators: 64, MaxScriptBytes: 256 << 10, MaxPatternBytes: 512}
	if filter.ArtifactID != "" {
		out.Resolution = &service.CustomValidatorResolution{ArtifactID: filter.ArtifactID, Validator: "manifest"}
	}
	return out, nil
}

func (f *validatorFakeBackend) CreateCustomValidator(_ context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error) {
	f.mu.Lock()
	f.created = append(f.created, req)
	f.mu.Unlock()
	if f.err != nil {
		return service.CustomValidator{}, f.err
	}
	return f.sample(req.Name), nil
}

func (f *validatorFakeBackend) UpdateCustomValidator(_ context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error) {
	f.mu.Lock()
	f.updated = append(f.updated, req)
	f.mu.Unlock()
	if f.err != nil {
		return service.CustomValidator{}, f.err
	}
	return f.sample(req.Name), nil
}

func (f *validatorFakeBackend) RemoveCustomValidator(_ context.Context, name string) (service.CustomValidator, error) {
	f.mu.Lock()
	f.removed = append(f.removed, name)
	f.mu.Unlock()
	if f.err != nil {
		return service.CustomValidator{}, f.err
	}
	return f.sample(name), nil
}

func (f *validatorFakeBackend) CustomValidatorScript(_ context.Context, name string) (service.ValidatorScript, error) {
	if f.err != nil {
		return service.ValidatorScript{}, f.err
	}
	return service.ValidatorScript{FileName: name + ".local.sh", SHA256: strings.Repeat("cd", 32), SizeBytes: 5, Content: "exit\n"}, nil
}

func (f *validatorFakeBackend) PreviewCustomValidator(_ context.Context, pattern, set, replacing string) (service.CustomValidatorPreview, error) {
	f.mu.Lock()
	f.preview = []string{pattern, set, replacing}
	f.mu.Unlock()
	return service.CustomValidatorPreview{Valid: true, Matches: 1, MatchSets: []string{"src/set-1"}, Sample: []string{"src/set-1/a"},
		Conflicts: []service.CustomValidatorConflict{{Validator: "other", Example: "src/set-1/a"}}}, nil
}

func (f *validatorFakeBackend) CustomValidatorPatterns(_ context.Context, set string) (service.CustomValidatorPatterns, error) {
	return service.CustomValidatorPatterns{BackupSetID: set, Patterns: []service.CustomValidatorPattern{
		{Pattern: `^db-\d{4}\.gz$`, Example: "db-2026.gz", Matches: 2, MatchSets: []string{"src/set-1"}},
	}}, nil
}

func validatorRouter(b BackupServiceClient, gate DestructiveGate, stepUp StepUpVerifier) http.Handler {
	return NewRouter(RouterConfig{Platform: allowingPlatform("alice"), Backend: b, Gate: gate, StepUp: stepUp, BinaryVersion: "test", Commit: "test"})
}

func validatorRequest(method, path, body string, password string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:5000"
	if password != "" {
		req.Header.Set(StepUpHeader, password)
	}
	return csrfPaired(req)
}

const uploadBody = `{"name":"manifest","pattern":"^MANIFEST-\\d{8}T\\d{6}Z\\.txt$","backup_set_id":"src/set-1","script":"#!/usr/bin/env bash\nexit 0\n"}`

func TestCustomValidatorRoutes_UploadOnAnOpenGateWithThePasswordIsCreated(t *testing.T) {
	b := newValidatorFakeBackend()
	step := &stubStepUp{user: "alice", password: stepUpPassword}
	router := validatorRouter(b, NewProfileGate(profile.Generic), step)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/assurance/validators", uploadBody, stepUpPassword))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(b.created) != 1 || b.created[0].Name != "manifest" || b.created[0].BackupSetID != "src/set-1" ||
		b.created[0].Pattern != `^MANIFEST-\d{8}T\d{6}Z\.txt$` || !strings.HasPrefix(b.created[0].Script, "#!/usr/bin/env bash") {
		t.Errorf("backend was asked %+v", b.created)
	}
	var got apicontract.CustomValidator
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "manifest" || got.MatchCount != 3 || got.UploadedAt != "2026-10-01T12:00:00Z" || got.LastResult == nil || got.LastResult.State != "passed" {
		t.Errorf("body = %+v", got)
	}
	if step.asked() != 1 {
		t.Errorf("the password was checked %d times, want once", step.asked())
	}
}

func TestCustomValidatorRoutes_UploadAndEditAreRefusedWithoutThePasswordOrOnAShutGate(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, password string
		gate                               DestructiveGate
		status                             int
		code                               string
	}{
		{"no password", http.MethodPost, "/api/v1/assurance/validators", uploadBody, "", NewProfileGate(profile.Generic), http.StatusForbidden, "STEP_UP_REQUIRED"},
		{"wrong password", http.MethodPost, "/api/v1/assurance/validators", uploadBody, "nope", NewProfileGate(profile.Generic), http.StatusForbidden, "STEP_UP_FAILED"},
		{"shut gate", http.MethodPost, "/api/v1/assurance/validators", uploadBody, stepUpPassword, NewProfileGate(profile.UGOS), http.StatusForbidden, "DESTRUCTIVE_OPERATIONS_DISABLED"},
		{"edit, no password", http.MethodPut, "/api/v1/assurance/validators/manifest", `{"pattern":"^x"}`, "", NewProfileGate(profile.Generic), http.StatusForbidden, "STEP_UP_REQUIRED"},
		{"edit, shut gate", http.MethodPut, "/api/v1/assurance/validators/manifest", `{"pattern":"^x"}`, stepUpPassword, NotYetImplementedGate{}, http.StatusForbidden, "DESTRUCTIVE_OPERATIONS_DISABLED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newValidatorFakeBackend()
			router := validatorRouter(b, tc.gate, &stubStepUp{user: "alice", password: stepUpPassword})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, validatorRequest(tc.method, tc.path, tc.body, tc.password))
			if rec.Code != tc.status || responseErrorCode(rec.Body.String()) != tc.code {
				t.Errorf("got %d %s, want %d %s", rec.Code, rec.Body.String(), tc.status, tc.code)
			}
			if len(b.created)+len(b.updated) != 0 {
				t.Error("a refused upload reached the backend")
			}
		})
	}
}

func TestCustomValidatorRoutes_PlainHTTPFromAnotherMachineNeedsTheTransportAcknowledgement(t *testing.T) {
	b := newValidatorFakeBackend()
	router := validatorRouter(b, NewProfileGate(profile.Generic), &stubStepUp{user: "alice", password: stepUpPassword})
	req := validatorRequest(http.MethodPost, "/api/v1/assurance/validators", uploadBody, stepUpPassword)
	req.RemoteAddr = "192.168.0.50:1"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if responseErrorCode(rec.Body.String()) != "INSECURE_TRANSPORT_ACK_REQUIRED" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	req = validatorRequest(http.MethodPost, "/api/v1/assurance/validators", uploadBody, stepUpPassword)
	req.RemoteAddr = "192.168.0.50:1"
	req.Header.Set(InsecureTransportHeader, InsecureTransportAcknowledged)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("acknowledged: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCustomValidatorRoutes_RemoveAndPreviewWorkOnAShutGate(t *testing.T) {
	b := newValidatorFakeBackend()
	router := validatorRouter(b, NotYetImplementedGate{}, nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodDelete, "/api/v1/assurance/validators/manifest", "", ""))
	if rec.Code != http.StatusOK || len(b.removed) != 1 || b.removed[0] != "manifest" {
		t.Errorf("remove: %d %s, backend %v", rec.Code, rec.Body.String(), b.removed)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/assurance/validator-preview", `{"pattern":"^a","backup_set_id":"src/set-1","replacing":"manifest"}`, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	if fmt.Sprint(b.preview) != "[^a src/set-1 manifest]" {
		t.Errorf("preview asked %v", b.preview)
	}
	var p apicontract.CustomValidatorPreview
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if !p.Valid || p.MatchCount != 1 || len(p.Conflicts) != 1 || p.Conflicts[0].Validator != "other" {
		t.Errorf("preview body = %+v", p)
	}
}

func TestCustomValidatorRoutes_ReadsPassTheirFiltersAndAnswerTheContractShapes(t *testing.T) {
	b := newValidatorFakeBackend()
	router := validatorRouter(b, NotYetImplementedGate{}, nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assurance/validators?backup_set=src%2Fset-1&artifact=src%2Fset-1%2FMANIFEST-1.txt", nil))
	if rec.Code != http.StatusOK || b.filter.BackupSetID != "src/set-1" || b.filter.ArtifactID != "src/set-1/MANIFEST-1.txt" {
		t.Fatalf("list: %d filter %+v", rec.Code, b.filter)
	}
	var l apicontract.ListCustomValidatorsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	if len(l.Validators) != 1 || l.Resolution == nil || l.Resolution.Validator != "manifest" || l.MaxValidators != 64 {
		t.Errorf("list body = %+v", l)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assurance/validator-patterns?backup_set=src%2Fset-1", nil))
	var p apicontract.CustomValidatorPatternsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusOK || p.BackupSetID != "src/set-1" || len(p.Patterns) != 1 || p.Patterns[0].MatchCount != 2 {
		t.Errorf("patterns: %d %+v", rec.Code, p)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assurance/validators/manifest/script", nil))
	var s apicontract.ValidatorScriptResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	if rec.Code != http.StatusOK || s.FileName != "manifest.local.sh" || s.Content != "exit\n" {
		t.Errorf("script: %d %+v", rec.Code, s)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assurance/default-validator-script", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	want := service.DefaultCustomValidatorScript()
	if rec.Code != http.StatusOK || s.Content != want.Content || s.Sha256 != want.SHA256 || s.FileName != "recovery-validator.local.sh" {
		t.Errorf("default script: %d %s", rec.Code, s.FileName)
	}
}

func TestCustomValidatorRoutes_ServiceRefusalsMapOntoTheirCodes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: %q", service.ErrValidatorNotFound, "x"), http.StatusNotFound, "VALIDATOR_NOT_FOUND"},
		{fmt.Errorf("%w: x", service.ErrValidatorExists), http.StatusConflict, "VALIDATOR_EXISTS"},
		{fmt.Errorf("%w: x", service.ErrValidatorConflict), http.StatusConflict, "VALIDATOR_PATTERN_CONFLICT"},
		{fmt.Errorf("%w (64)", service.ErrValidatorLimit), http.StatusConflict, "VALIDATOR_LIMIT_REACHED"},
		{fmt.Errorf("%w: no root", service.ErrValidatorsUnavailable), http.StatusServiceUnavailable, "VALIDATORS_UNAVAILABLE"},
		{fmt.Errorf("%w: bad", service.ErrInvalidRequest), http.StatusBadRequest, "INVALID_REQUEST"},
		{fmt.Errorf("%w: src/x", service.ErrBackupSetNotFound), http.StatusNotFound, "BACKUP_SET_NOT_FOUND"},
	} {
		b := newValidatorFakeBackend()
		b.err = tc.err
		router := validatorRouter(b, NewProfileGate(profile.Generic), &stubStepUp{user: "alice", password: stepUpPassword})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/assurance/validators", uploadBody, stepUpPassword))
		if rec.Code != tc.status || responseErrorCode(rec.Body.String()) != tc.code {
			t.Errorf("%v: got %d %s, want %d %s", tc.err, rec.Code, rec.Body.String(), tc.status, tc.code)
		}
		if strings.HasPrefix(tc.code, "VALIDATOR") && strings.Contains(rec.Body.String(), `"service: `) {
			t.Errorf("the package prefix reached the client: %s", rec.Body.String())
		}
	}
}

func TestCustomValidatorRoutes_ABackendWithoutValidatorsIsUnavailableNotA500(t *testing.T) {
	router := validatorRouter(newSyncFakeBackend(), NotYetImplementedGate{}, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assurance/validators", nil))
	if rec.Code != http.StatusServiceUnavailable || responseErrorCode(rec.Body.String()) != "VALIDATORS_UNAVAILABLE" {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestCustomValidatorRoutes_UnknownBodyFieldsAreRefused(t *testing.T) {
	b := newValidatorFakeBackend()
	router := validatorRouter(b, NewProfileGate(profile.Generic), &stubStepUp{user: "alice", password: stepUpPassword})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/assurance/validators", `{"name":"x","pattern":"x","script":"x","replace":true}`, stepUpPassword))
	if rec.Code != http.StatusBadRequest || len(b.created) != 0 {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
}
