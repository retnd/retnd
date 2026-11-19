package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// Issue #1253, the auth slice: the three /auth operations a browser makes
// with no session, from this client.
//
// All three are declared unauthenticated and CSRF-required, which is the
// awkward pair: the double-submit cookie only arrives with a response,
// and this client has no account to sign in as (enrollment makes it;
// the reset flow exists because its password is lost). So each one reads
// GET /auth/session first, purely to be issued the cookie, takes the 401
// as the expected answer, and sends. None of them ever POSTs /auth/login.

func TestEnroll1253_SendsTheBootstrapTokenAndSignsInAsNobody(t *testing.T) {
	engine := newFakeEngine(t)
	engine.password = ""
	var gotHeader string
	var gotBody apicontract.EnrollRequest
	engine.answer["enrollAdministrator"] = func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Bootstrap-Token")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusNoContent)
	}
	client, err := New(Config{BaseURL: engine.start()})
	if err != nil {
		t.Fatal(err)
	}
	req := apicontract.EnrollRequest{Username: "admin", Password: "a-long-enough-password"}
	if err := client.Enroll(context.Background(), req, "bootstrap-1253"); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if gotHeader != "bootstrap-1253" {
		t.Errorf("X-Bootstrap-Token = %q, want the token handed over", gotHeader)
	}
	if gotBody != req {
		t.Errorf("the engine received %+v, want %+v", gotBody, req)
	}
	for _, r := range engine.requests() {
		if r.Operation == "login" {
			t.Error("Enroll signed in, and there is no account to sign in as before it")
		}
		if r.Operation == "enrollAdministrator" && (r.CSRFHeader == "" || r.CSRFHeader != r.CSRFCookie) {
			t.Errorf("the enrollment did not echo the double-submit cookie: header %q, cookie %q", r.CSRFHeader, r.CSRFCookie)
		}
	}
}

func TestResetFlow1253_BothCallsEchoTheCookieAndNeverSignIn(t *testing.T) {
	engine := newFakeEngine(t)
	engine.password = ""
	client, err := New(Config{BaseURL: engine.start()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.RequestPasswordReset(ctx, apicontract.ForgotPasswordRequest{Username: "admin"}); err != nil {
		t.Fatalf("RequestPasswordReset: %v", err)
	}
	if err := client.ResetPassword(ctx, apicontract.ResetPasswordRequest{Token: "reset-1253", NewPassword: "a-long-enough-password"}); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	sent := map[string]bool{}
	for _, r := range engine.requests() {
		switch r.Operation {
		case "login":
			t.Error("the reset flow signed in, and not having the password is the whole reason it exists")
		case "requestPasswordReset", "resetPassword":
			sent[r.Operation] = true
			if r.CSRFHeader == "" || r.CSRFHeader != r.CSRFCookie {
				t.Errorf("%s did not echo the double-submit cookie: header %q, cookie %q", r.Operation, r.CSRFHeader, r.CSRFCookie)
			}
		}
	}
	if !sent["requestPasswordReset"] || !sent["resetPassword"] {
		t.Errorf("the engine saw %v, want both reset operations", engine.operations())
	}
}

// The engine's refusal comes back as the typed error every other refusal
// is, so a verb can show its code and message.
func TestResetFlow1253_ARefusalIsTheEnginesOwn(t *testing.T) {
	engine := newFakeEngine(t)
	engine.answer["resetPassword"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(apicontract.AuthErrorResponse{Code: apicontract.ErrorCodeResetTokenInvalid, Message: "spent"})
	}
	client, err := New(Config{BaseURL: engine.start()})
	if err != nil {
		t.Fatal(err)
	}
	err = client.ResetPassword(context.Background(), apicontract.ResetPasswordRequest{Token: "stale", NewPassword: "a-long-enough-password"})
	var refused *Error
	if !errors.As(err, &refused) || refused.Code != apicontract.ErrorCodeResetTokenInvalid {
		t.Fatalf("ResetPassword returned %v, want the engine's RESET_TOKEN_INVALID", err)
	}
}
