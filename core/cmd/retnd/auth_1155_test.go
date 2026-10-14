package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// Issue #1155: the one administrator's password and recovery settings from a
// terminal. Every verb here is routed, because the account belongs to the
// process serving the deployment, and every secret arrives on stdin and goes
// nowhere else: not argv, not a stream the command prints, not a file.

const (
	currentPW1155 = "current-passphrase-1155"
	nextPW1155    = "next-passphrase-1155-longer"
	smtpPW1155    = "smtp-secret-1155"
	tokenSecret   = "verify-token-1155-abcdef"
)

// withStdin1155 feeds content to the command's standard input.
func withStdin1155(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = orig; _ = f.Close() })
}

// runAuth1155 runs one invocation and returns its exit code and both streams.
func runAuth1155(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	withStdin1155(t, stdin)
	var code int
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() { code = run(args) })
	})
	return code, stdout, stderr
}

// recorder keeps the bodies one scripted route received.
type recorder1155 struct {
	mu     sync.Mutex
	bodies []string
}

func (r *recorder1155) handler(status int, reply any) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, string(b))
		r.mu.Unlock()
		writeJSON(w, status, reply)
	}
}

func (r *recorder1155) only(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) != 1 {
		t.Fatalf("the engine received %d bodies, want exactly 1: %q", len(r.bodies), r.bodies)
	}
	return r.bodies[0]
}

func noSecrets1155(t *testing.T, what string, streams ...string) {
	t.Helper()
	for _, s := range streams {
		for _, secret := range []string{currentPW1155, nextPW1155, smtpPW1155, tokenSecret} {
			if strings.Contains(s, secret) {
				t.Errorf("%s: a secret reached an output stream: %q", what, s)
			}
		}
	}
}

func attachedEngine1155(t *testing.T) (*fakeEngine, string) {
	t.Helper()
	cfg := writeTestConfig(t)
	engine := startFakeEngineFor(t, cfg)
	engine.attach(t)
	return engine, cfg
}

func TestAuthPassword1155_SendsBothPasswordsFromStdinAndPrintsNeither(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	rec := &recorder1155{}
	engine.handle("POST /auth/password", rec.handler(http.StatusNoContent, nil))

	code, stdout, stderr := runAuth1155(t, currentPW1155+"\n"+nextPW1155+"\n",
		"auth", "password", "--password-stdin", "--config", cfg)
	if code != 0 {
		t.Fatalf("auth password exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var got apicontract.RotatePasswordRequest
	if err := json.Unmarshal([]byte(rec.only(t)), &got); err != nil {
		t.Fatal(err)
	}
	if got.CurrentPassword != currentPW1155 || got.NewPassword != nextPW1155 {
		t.Errorf("the engine received %+v; the first stdin line is the current password and the second the new one", got)
	}
	noSecrets1155(t, "auth password", stdout, stderr)
}

func TestAuthPassword1155_ANewPasswordIsNeverTakenFromArgv(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	rec := &recorder1155{}
	engine.handle("POST /auth/password", rec.handler(http.StatusNoContent, nil))

	for _, argv := range [][]string{
		{"auth", "password", "--config", cfg},
		{"auth", "password", "--password-stdin", "--password", nextPW1155, "--config", cfg},
		{"auth", "password", "--password-stdin", "--new-password", nextPW1155, "--config", cfg},
		{"auth", "password", "--password-stdin", "--current-password", currentPW1155, "--config", cfg},
	} {
		code, stdout, stderr := runAuth1155(t, currentPW1155+"\n"+nextPW1155+"\n", argv...)
		if code != exitUsage {
			t.Errorf("%v exited %d, want the usage code", argv, code)
		}
		noSecrets1155(t, strings.Join(argv, " "), stdout, stderr)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.bodies) != 0 {
		t.Errorf("a usage error still reached the engine: %q", rec.bodies)
	}
}

func TestAuthPassword1155_ARefusalNamesTheReasonAndNoSecret(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	engine.handle("POST /auth/password", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusUnauthorized, apicontract.AuthErrorResponse{
			Code: apicontract.ErrorCodeUnauthenticated, Message: "the current password is not right",
		})
	})
	code, stdout, stderr := runAuth1155(t, currentPW1155+"\n"+nextPW1155+"\n",
		"auth", "password", "--password-stdin", "--config", cfg)
	if code == 0 || code == exitUsage {
		t.Errorf("a refused rotation exited %d, want a plain failure", code)
	}
	if !strings.Contains(stderr, "the current password is not right") {
		t.Errorf("the engine's reason is not shown: %q", stderr)
	}
	noSecrets1155(t, "a refused rotation", stdout, stderr)
}

func TestAuthPassword1155_NeedsAnEngineAndSaysHowToReachOne(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	code, stdout, stderr := runAuth1155(t, currentPW1155+"\n"+nextPW1155+"\n",
		"auth", "password", "--password-stdin", "--config", cfg)
	if code == 0 || code == exitUsage {
		t.Errorf("auth password with nothing serving exited %d, want a plain failure", code)
	}
	if !strings.Contains(stderr, "RETND_API_URL") || !strings.Contains(stderr, "retnd-web auth set-password") {
		t.Errorf("the refusal does not name $RETND_API_URL and the offline way, retnd-web auth set-password: %q", stderr)
	}
	noSecrets1155(t, "auth password without an engine", stdout, stderr)
}

func recoveryReply1155() apicontract.RecoverySettingsResponse {
	return apicontract.RecoverySettingsResponse{
		RecoveryEmail: "ops@example.org", RecoveryEmailConfirmed: true, RecoveryEmailVerified: false,
		VerificationDeadline: "2026-10-14T12:00:00Z",
		Smtp: &apicontract.SmtpSettingsView{Host: "smtp.example.org", Port: 587, Security: "starttls",
			Username: "mailer", From: "retnd@example.org", PasswordSet: true},
	}
}

func TestAuthRecoveryShow1155_PrintsTheSettingsAndJSONIsTheWireObject(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	reply := recoveryReply1155()
	engine.handle("GET /auth/recovery", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, reply) })

	code, stdout, stderr := runAuth1155(t, "", "auth", "recovery", "show", "--config", cfg)
	if code != 0 {
		t.Fatalf("auth recovery show exited %d\n%s", code, stderr)
	}
	for _, want := range []string{"ops@example.org", "smtp.example.org", "587", "starttls", "mailer", "retnd@example.org"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("auth recovery show does not print %q:\n%s", want, stdout)
		}
	}

	code, jsonOut, stderr := runAuth1155(t, "", "auth", "recovery", "show", "--json", "--config", cfg)
	if code != 0 {
		t.Fatalf("auth recovery show --json exited %d\n%s", code, stderr)
	}
	body := jsonOut
	if i := strings.LastIndex(jsonOut, "\n{\n"); i >= 0 {
		body = jsonOut[i+1:]
	}
	wire, _ := json.Marshal(reply)
	var want, got map[string]any
	_ = json.Unmarshal(wire, &want)
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("--json did not print a JSON object: %v\n%s", err, jsonOut)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("--json is not the object the API returned, field for field\n got %v\nwant %v", got, want)
	}
}

func TestAuthRecoverySet1155_SendsTheUpdateAndKeepsEverySecretOnStdin(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	rec := &recorder1155{}
	engine.handle("PATCH /auth/recovery", rec.handler(http.StatusOK, recoveryReply1155()))

	code, stdout, stderr := runAuth1155(t, currentPW1155+"\n"+smtpPW1155+"\n",
		"auth", "recovery", "set", "--current-password-stdin", "--recovery-email", "ops@example.org",
		"--smtp-host", "smtp.example.org", "--smtp-port", "2525", "--smtp-security", "tls",
		"--smtp-username", "mailer", "--smtp-from", "retnd@example.org", "--smtp-password-stdin",
		"--config", cfg)
	if code != 0 {
		t.Fatalf("auth recovery set exited %d\n%s", code, stderr)
	}
	var got apicontract.RecoverySettingsUpdate
	if err := json.Unmarshal([]byte(rec.only(t)), &got); err != nil {
		t.Fatal(err)
	}
	if got.CurrentPassword != currentPW1155 {
		t.Errorf("currentPassword = %q, want the first stdin line", got.CurrentPassword)
	}
	if got.RecoveryEmail == nil || *got.RecoveryEmail != "ops@example.org" {
		t.Errorf("recoveryEmail = %v", got.RecoveryEmail)
	}
	want := apicontract.SmtpSettings{Host: "smtp.example.org", Port: 2525, Security: "tls",
		Username: "mailer", From: "retnd@example.org", Password: smtpPW1155}
	if got.Smtp == nil || *got.Smtp != want {
		t.Errorf("smtp = %+v, want %+v (the password is the SECOND stdin line)", got.Smtp, want)
	}
	noSecrets1155(t, "auth recovery set", stdout, stderr)
}

func TestAuthRecoverySet1155_AnAddressAloneLeavesSMTPOutOfTheRequest(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	rec := &recorder1155{}
	engine.handle("PATCH /auth/recovery", rec.handler(http.StatusOK, recoveryReply1155()))
	code, _, stderr := runAuth1155(t, currentPW1155+"\n",
		"auth", "recovery", "set", "--current-password-stdin", "--recovery-email", "ops@example.org", "--config", cfg)
	if code != 0 {
		t.Fatalf("exited %d\n%s", code, stderr)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(rec.only(t)), &raw); err != nil {
		t.Fatal(err)
	}
	if v, present := raw["smtp"]; present && v != nil {
		t.Errorf("an address-only change sent an smtp block: %v", v)
	}
}

func TestAuthRecoverySet1155_UsageMistakesNeverReachTheEngine(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	rec := &recorder1155{}
	engine.handle("PATCH /auth/recovery", rec.handler(http.StatusOK, recoveryReply1155()))
	stdin := currentPW1155 + "\n" + smtpPW1155 + "\n"
	for name, argv := range map[string][]string{
		"nothing to change":              {"auth", "recovery", "set", "--current-password-stdin"},
		"no current password":            {"auth", "recovery", "set", "--recovery-email", "a@b.example"},
		"smtp password with no host":     {"auth", "recovery", "set", "--current-password-stdin", "--recovery-email", "a@b.example", "--smtp-password-stdin"},
		"the current password as a flag": {"auth", "recovery", "set", "--current-password", currentPW1155, "--recovery-email", "a@b.example"},
		"the smtp password as a flag":    {"auth", "recovery", "set", "--current-password-stdin", "--smtp-host", "h.example", "--smtp-password", smtpPW1155},
	} {
		code, stdout, stderr := runAuth1155(t, stdin, append(argv, "--config", cfg)...)
		if code != exitUsage {
			t.Errorf("%s: exited %d, want the usage code", name, code)
		}
		noSecrets1155(t, name, stdout, stderr)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.bodies) != 0 {
		t.Errorf("a usage mistake reached the engine: %q", rec.bodies)
	}
}

func TestAuthRecoveryTest1155_AsksTheEngineToSendTheTestMessage(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	rec := &recorder1155{}
	engine.handle("POST /auth/recovery/test", rec.handler(http.StatusNoContent, nil))
	code, _, stderr := runAuth1155(t, "", "auth", "recovery", "test", "--config", cfg)
	if code != 0 {
		t.Fatalf("exited %d\n%s", code, stderr)
	}
	rec.only(t)
}

func TestAuthVerifyEmail1155_RedeemsATokenFromStdinOrAsksForAnotherMessage(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	redeem, resend := &recorder1155{}, &recorder1155{}
	engine.handle("POST /auth/verify-email", redeem.handler(http.StatusNoContent, nil))
	engine.handle("POST /auth/verify-email/resend", resend.handler(http.StatusNoContent, nil))

	code, stdout, stderr := runAuth1155(t, tokenSecret+"\n", "auth", "verify-email", "--token-stdin", "--config", cfg)
	if code != 0 {
		t.Fatalf("--token-stdin exited %d\n%s", code, stderr)
	}
	var got apicontract.VerifyEmailRequest
	if err := json.Unmarshal([]byte(redeem.only(t)), &got); err != nil || got.Token != tokenSecret {
		t.Errorf("the engine received %q (%v), want the token from stdin", redeem.bodies, err)
	}
	noSecrets1155(t, "verify-email --token-stdin", stdout, stderr)

	if code, _, stderr = runAuth1155(t, "", "auth", "verify-email", "--resend", "--config", cfg); code != 0 {
		t.Fatalf("--resend exited %d\n%s", code, stderr)
	}
	resend.only(t)

	for name, argv := range map[string][]string{
		"neither":         {"auth", "verify-email"},
		"both":            {"auth", "verify-email", "--token-stdin", "--resend"},
		"token as a flag": {"auth", "verify-email", "--token", tokenSecret},
	} {
		code, stdout, stderr := runAuth1155(t, tokenSecret+"\n", append(argv, "--config", cfg)...)
		if code != exitUsage {
			t.Errorf("%s exited %d, want the usage code", name, code)
		}
		noSecrets1155(t, name, stdout, stderr)
	}
}

func TestAuth1155_UnknownSubcommandsAreUsageErrors(t *testing.T) {
	cfg := writeTestConfig(t)
	for _, argv := range [][]string{
		{"auth"}, {"auth", "frobnicate"}, {"auth", "recovery"}, {"auth", "recovery", "frobnicate"},
	} {
		code, _, _ := runAuth1155(t, "", append(argv, "--config", cfg)...)
		if code != exitUsage {
			t.Errorf("%v exited %d, want the usage code", argv, code)
		}
	}
}
