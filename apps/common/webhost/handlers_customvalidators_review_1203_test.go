package webhost

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/core/service"
)

// #1203 review: the step-up fails closed, and the PUT carries script_sha256.

// A gate that is open but does not implement the package's private
// conditions interface (a test double, whatever #92 writes) used to skip the
// step-up and let the upload through on the CSRF cookie alone.
func TestCustomValidatorRoutes_AnOpenGateWithNoConditionsStillAsksForThePassword(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"upload", http.MethodPost, "/api/v1/assurance/validators", uploadBody},
		{"edit", http.MethodPut, "/api/v1/assurance/validators/manifest", `{"pattern":"^x","script":"#!/usr/bin/env bash\nexit 0\n"}`},
	} {
		t.Run(tc.name+" without the password", func(t *testing.T) {
			b := newValidatorFakeBackend()
			router := validatorRouter(b, alwaysPassGate{}, &stubStepUp{user: "alice", password: stepUpPassword})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, validatorRequest(tc.method, tc.path, tc.body, ""))
			if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "STEP_UP_REQUIRED" {
				t.Errorf("got %d %s, want 403 STEP_UP_REQUIRED", rec.Code, rec.Body.String())
			}
			if len(b.created)+len(b.updated) != 0 {
				t.Error("an upload without the password reached the backend")
			}
		})
		t.Run(tc.name+" with the password", func(t *testing.T) {
			b := newValidatorFakeBackend()
			step := &stubStepUp{user: "alice", password: stepUpPassword}
			router := validatorRouter(b, alwaysPassGate{}, step)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, validatorRequest(tc.method, tc.path, tc.body, stepUpPassword))
			if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
				t.Errorf("got %d %s", rec.Code, rec.Body.String())
			}
			if step.asked() != 1 {
				t.Errorf("the password was checked %d times, want once", step.asked())
			}
		})
		t.Run(tc.name+" where no password check is wired", func(t *testing.T) {
			b := newValidatorFakeBackend()
			router := validatorRouter(b, alwaysPassGate{}, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, validatorRequest(tc.method, tc.path, tc.body, stepUpPassword))
			if rec.Code != http.StatusServiceUnavailable || responseErrorCode(rec.Body.String()) != "STEP_UP_UNAVAILABLE" {
				t.Errorf("got %d %s, want 503 STEP_UP_UNAVAILABLE", rec.Code, rec.Body.String())
			}
			if len(b.created)+len(b.updated) != 0 {
				t.Error("an upload reached the backend with no way to check the password")
			}
		})
	}
}

func TestCustomValidatorRoutes_ThePutPassesScriptSHA256OnAndMapsAChangedScriptToAConflict(t *testing.T) {
	b := newValidatorFakeBackend()
	router := validatorRouter(b, NewProfileGate(profile.Generic), &stubStepUp{user: "alice", password: stepUpPassword})
	hash := strings.Repeat("ab", 32)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPut, "/api/v1/assurance/validators/manifest",
		`{"pattern":"^x","backup_set_id":"src/set-1","script_sha256":"`+hash+`"}`, stepUpPassword))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(b.updated) != 1 || b.updated[0].ScriptSHA256 != hash || b.updated[0].Script != "" || b.updated[0].BackupSetID != "src/set-1" {
		t.Errorf("backend was asked %+v", b.updated)
	}

	b.err = fmt.Errorf("%w: the stored script is not the one you read", service.ErrValidatorScriptChanged)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPut, "/api/v1/assurance/validators/manifest",
		`{"pattern":"^x","script_sha256":"`+hash+`"}`, stepUpPassword))
	if rec.Code != http.StatusConflict || responseErrorCode(rec.Body.String()) != "VALIDATOR_SCRIPT_CHANGED" {
		t.Errorf("got %d %s, want 409 VALIDATOR_SCRIPT_CHANGED", rec.Code, rec.Body.String())
	}
}
