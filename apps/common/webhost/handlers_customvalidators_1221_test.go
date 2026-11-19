package webhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1221 over HTTP: a validator's env reaches the service from the body of
// both writes, and comes back on the validator the routes answer with.

// envEchoBackend answers each write with the validator it was asked for,
// env included, the way the real service does.
type envEchoBackend struct{ *validatorFakeBackend }

func (f envEchoBackend) CreateCustomValidator(ctx context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error) {
	v, err := f.validatorFakeBackend.CreateCustomValidator(ctx, req)
	v.Env = req.Env
	return v, err
}

func (f envEchoBackend) UpdateCustomValidator(ctx context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error) {
	v, err := f.validatorFakeBackend.UpdateCustomValidator(ctx, req)
	v.Env = req.Env
	return v, err
}

func (f envEchoBackend) ListCustomValidators(ctx context.Context, filter service.CustomValidatorsFilter) (service.CustomValidators, error) {
	l, err := f.validatorFakeBackend.ListCustomValidators(ctx, filter)
	for i := range l.Validators {
		l.Validators[i].Env = "workflow"
	}
	return l, err
}

func TestIssue1221_UploadAndEditCarryTheEnvBothWays(t *testing.T) {
	b := envEchoBackend{newValidatorFakeBackend()}
	step := &stubStepUp{user: "alice", password: stepUpPassword}
	router := validatorRouter(b, NewProfileGate(profile.Generic), step)

	post := strings.Replace(uploadBody, `"script"`, `"env":"workflow","script"`, 1)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/assurance/validators", post, stepUpPassword))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(b.created) != 1 || b.created[0].Env != "workflow" {
		t.Errorf("the backend was asked %+v, want env workflow", b.created)
	}
	var got apicontract.CustomValidator
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Env != "workflow" {
		t.Errorf("created body env = %q (%v)", got.Env, err)
	}

	rec = httptest.NewRecorder()
	put := `{"pattern":"^M","backup_set_id":"src/set-1","script_sha256":"` + strings.Repeat("ab", 32) + `","env":"workflow"}`
	router.ServeHTTP(rec, validatorRequest(http.MethodPut, "/api/v1/assurance/validators/manifest", put, stepUpPassword))
	if rec.Code != http.StatusOK || len(b.updated) != 1 || b.updated[0].Env != "workflow" {
		t.Fatalf("edit: %d, backend asked %+v", rec.Code, b.updated)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Env != "workflow" {
		t.Errorf("edit body env = %q (%v)", got.Env, err)
	}

	rec = httptest.NewRecorder()
	put = `{"pattern":"^M","backup_set_id":"src/set-1","script_sha256":"` + strings.Repeat("ab", 32) + `"}`
	router.ServeHTTP(rec, validatorRequest(http.MethodPut, "/api/v1/assurance/validators/manifest", put, stepUpPassword))
	if rec.Code != http.StatusOK || len(b.updated) != 2 || b.updated[1].Env != "" {
		t.Errorf("an edit that leaves env out must reach the service with none, so it is a replacement: %d %+v", rec.Code, b.updated)
	}
}

func TestIssue1221_TheListAnswersWithEachValidatorsEnv(t *testing.T) {
	router := validatorRouter(envEchoBackend{newValidatorFakeBackend()}, NotYetImplementedGate{}, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assurance/validators", nil))
	var l apicontract.ListCustomValidatorsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil || len(l.Validators) != 1 || l.Validators[0].Env != "workflow" {
		t.Errorf("list = %+v (%v)", l, err)
	}
	if !strings.Contains(rec.Body.String(), `"env":"workflow"`) {
		t.Errorf("the body does not carry env:\n%s", rec.Body.String())
	}
}
