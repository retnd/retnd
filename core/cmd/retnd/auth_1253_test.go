package main

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// Issue #1253, the auth slice: the three account acts a browser can do
// with no session, and the one question it asks on every load, from a
// terminal.
//
//   - `auth enroll` makes the first administrator through POST
//     /auth/enroll, the request the enrollment page sends, with the
//     bootstrap token out of the enrollment link. `retnd-web auth
//     create-admin` refuses while a server holds the store, and on a
//     headless install the server is the container (#1254), so until this
//     the terminal had no way to make the account at all.
//   - `auth forgot-password` and `auth reset-password` are the reset flow
//     the sign-in page offers, over the same two routes and the same
//     emailed token. `auth password` needs the current password, which is
//     the one thing the flow exists to do without.
//   - `auth whoami` is GET /auth/session: who the credentials this
//     terminal carries sign in as.
//
// Every secret (the bootstrap token, the reset token, every password)
// arrives on stdin and goes nowhere else, exactly as #1155's verbs have it.

const (
	bootstrapToken1253 = "bootstrap-token-1253-abcdef"
	resetToken1253     = "reset-token-1253-abcdef"
	newPW1253          = "new-passphrase-1253-longer"
	smtpPW1253         = "smtp-secret-1253"
)

func noSecrets1253(t *testing.T, what string, streams ...string) {
	t.Helper()
	for _, s := range streams {
		for _, secret := range []string{bootstrapToken1253, resetToken1253, newPW1253, smtpPW1253} {
			if strings.Contains(s, secret) {
				t.Errorf("%s: a secret reached an output stream: %q", what, s)
			}
		}
	}
}

// anonymousRecorder keeps what one unauthenticated route received: the
// body, and the one header enrollment carries its authority in.
type anonymousRecorder struct {
	mu      sync.Mutex
	bodies  []string
	headers []string
}

func (r *anonymousRecorder) handler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, string(b))
		r.headers = append(r.headers, req.Header.Get("X-Bootstrap-Token"))
		r.mu.Unlock()
		w.WriteHeader(status)
	}
}

func (r *anonymousRecorder) only(t *testing.T) (body, bootstrap string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) != 1 {
		t.Fatalf("the engine received %d bodies, want exactly 1: %q", len(r.bodies), r.bodies)
	}
	return r.bodies[0], r.headers[0]
}

func (r *anonymousRecorder) none(t *testing.T, what string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) != 0 {
		t.Errorf("%s reached the engine: %q", what, r.bodies)
	}
}

// addressOnly is a terminal that knows the engine's address and nothing
// else: no deployment of its own, and no credentials, because none of the
// three unauthenticated verbs has an account to sign in as.
func addressOnly(t *testing.T, e *fakeEngine) (cliConfig string) {
	t.Helper()
	clearInheritedRouteSettings()
	t.Setenv("RETND_API_URL", e.baseURL())
	return filepath.Join(t.TempDir(), "no-deployment-here", "config.yaml")
}

// TestAuthEnroll1253_MakesTheFirstAdministratorBesideAFirstRunEngine is the
// headless case: an engine still on its first run, no config.yaml, and a
// terminal with the address and the enrollment token. The verb sends the
// enrollment page's request with the token in its header, signs in as
// nobody beforehand (there is nobody yet), and prints no secret.
func TestAuthEnroll1253_MakesTheFirstAdministratorBesideAFirstRunEngine(t *testing.T) {
	engine, _, _ := firstRunFixture(t)
	cfg := addressOnly(t, engine.fakeEngine)
	rec := &anonymousRecorder{}
	engine.handleUnauthenticated("POST /auth/enroll", rec.handler(http.StatusNoContent))

	code, stdout, stderr := runAuth1155(t, bootstrapToken1253+"\n"+newPW1253+"\n",
		"auth", "enroll", "--username", "admin", "--token-stdin", "--password-stdin", "--config", cfg)
	if code != 0 {
		t.Fatalf("auth enroll exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	body, bootstrap := rec.only(t)
	var got apicontract.EnrollRequest
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Username != "admin" || got.Password != newPW1253 {
		t.Errorf("the engine received %+v; the first stdin line is the token and the second the password", got)
	}
	if got.RecoveryEmail != "" || got.Smtp != (apicontract.SmtpSettings{}) {
		t.Errorf("an enrollment with no recovery flags sent a recovery section: %+v", got)
	}
	if bootstrap != bootstrapToken1253 {
		t.Errorf("X-Bootstrap-Token = %q, want the first stdin line", bootstrap)
	}
	for _, never := range []string{"POST /auth/login", "GET /system/version"} {
		if sawRequest(engine.fakeEngine, never) {
			t.Errorf("enrollment tried %s, and there is no account to sign in as before it", never)
		}
	}
	if !strings.Contains(stdout, "admin") {
		t.Errorf("the confirmation does not name the administrator:\n%s", stdout)
	}
	noSecrets1253(t, "auth enroll", stdout, stderr)
}

// The enrollment link is what an operator has in front of them, so the
// stdin line may be the whole link rather than the token cut out of it.
func TestAuthEnroll1253_TakesTheWholeEnrollmentLinkOnStdin(t *testing.T) {
	engine, _, _ := firstRunFixture(t)
	cfg := addressOnly(t, engine.fakeEngine)
	rec := &anonymousRecorder{}
	engine.handleUnauthenticated("POST /auth/enroll", rec.handler(http.StatusNoContent))

	link := engine.baseURL() + "/enroll?token=" + bootstrapToken1253
	code, stdout, stderr := runAuth1155(t, link+"\n"+newPW1253+"\n",
		"auth", "enroll", "--username", "admin", "--token-stdin", "--password-stdin", "--config", cfg)
	if code != 0 {
		t.Fatalf("auth enroll exited %d\n%s", code, stderr)
	}
	if _, bootstrap := rec.only(t); bootstrap != bootstrapToken1253 {
		t.Errorf("X-Bootstrap-Token = %q, want the token out of the link", bootstrap)
	}
	noSecrets1253(t, "auth enroll with a link", stdout, stderr)
}

func TestAuthEnroll1253_CarriesTheRecoverySectionWithTheSMTPPasswordOnStdin(t *testing.T) {
	engine, _, _ := firstRunFixture(t)
	cfg := addressOnly(t, engine.fakeEngine)
	rec := &anonymousRecorder{}
	engine.handleUnauthenticated("POST /auth/enroll", rec.handler(http.StatusNoContent))

	code, stdout, stderr := runAuth1155(t, bootstrapToken1253+"\n"+newPW1253+"\n"+smtpPW1253+"\n",
		"auth", "enroll", "--username", "admin", "--token-stdin", "--password-stdin",
		"--recovery-email", "ops@example.org", "--smtp-host", "smtp.example.org", "--smtp-port", "2525",
		"--smtp-security", "tls", "--smtp-username", "mailer", "--smtp-from", "retnd@example.org",
		"--smtp-password-stdin", "--config", cfg)
	if code != 0 {
		t.Fatalf("auth enroll exited %d\n%s", code, stderr)
	}
	body, _ := rec.only(t)
	var got apicontract.EnrollRequest
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	want := apicontract.SmtpSettings{Host: "smtp.example.org", Port: 2525, Security: "tls",
		Username: "mailer", From: "retnd@example.org", Password: smtpPW1253}
	if got.RecoveryEmail != "ops@example.org" || got.Smtp != want {
		t.Errorf("the engine received %+v, want the recovery address and %+v (the SMTP password is the THIRD stdin line)", got, want)
	}
	noSecrets1253(t, "auth enroll with recovery", stdout, stderr)
}

func TestAuthEnroll1253_UsageMistakesNeverReachTheEngineOrReadASecret(t *testing.T) {
	engine, _, _ := firstRunFixture(t)
	cfg := addressOnly(t, engine.fakeEngine)
	rec := &anonymousRecorder{}
	engine.handleUnauthenticated("POST /auth/enroll", rec.handler(http.StatusNoContent))
	stdin := bootstrapToken1253 + "\n" + newPW1253 + "\n"
	for name, argv := range map[string][]string{
		"no username":                     {"auth", "enroll", "--token-stdin", "--password-stdin"},
		"no token":                        {"auth", "enroll", "--username", "admin", "--password-stdin"},
		"no password":                     {"auth", "enroll", "--username", "admin", "--token-stdin"},
		"the token as a flag":             {"auth", "enroll", "--username", "admin", "--token", bootstrapToken1253, "--password-stdin"},
		"the password as a flag":          {"auth", "enroll", "--username", "admin", "--token-stdin", "--password", newPW1253},
		"an operand":                      {"auth", "enroll", "admin", "--token-stdin", "--password-stdin"},
		"a recovery address with no smtp": {"auth", "enroll", "--username", "admin", "--token-stdin", "--password-stdin", "--recovery-email", "ops@example.org"},
		"smtp with no recovery address":   {"auth", "enroll", "--username", "admin", "--token-stdin", "--password-stdin", "--smtp-host", "smtp.example.org"},
		"the smtp password as a flag":     {"auth", "enroll", "--username", "admin", "--token-stdin", "--password-stdin", "--recovery-email", "ops@example.org", "--smtp-host", "h", "--smtp-password", smtpPW1253},
	} {
		code, stdout, stderr := runAuth1155(t, stdin, append(argv, "--config", cfg)...)
		if code != exitUsage {
			t.Errorf("%s: exited %d, want the usage code", name, code)
		}
		noSecrets1253(t, name, stdout, stderr)
	}
	rec.none(t, "a usage mistake")
}

func TestAuthEnroll1253_ARefusalNamesTheReasonAndNoSecret(t *testing.T) {
	engine, _, _ := firstRunFixture(t)
	cfg := addressOnly(t, engine.fakeEngine)
	engine.handleUnauthenticated("POST /auth/enroll", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusForbidden, apicontract.AuthErrorResponse{
			Code: apicontract.ErrorCodeEnrollmentClosed, Message: "an administrator account already exists",
		})
	})
	code, stdout, stderr := runAuth1155(t, bootstrapToken1253+"\n"+newPW1253+"\n",
		"auth", "enroll", "--username", "admin", "--token-stdin", "--password-stdin", "--config", cfg)
	if code == 0 || code == exitUsage {
		t.Errorf("a refused enrollment exited %d, want a plain failure", code)
	}
	if !strings.Contains(stderr, "an administrator account already exists") {
		t.Errorf("the engine's reason is not shown: %q", stderr)
	}
	noSecrets1253(t, "a refused enrollment", stdout, stderr)
}

// Without an address the three unauthenticated verbs have nothing to send
// to, and the refusal says which variable gives them one. The credential
// variables are not asked for: none of these signs in.
func TestAuth1253_TheUnauthenticatedVerbsNeedOnlyAnAddress(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := filepath.Join(t.TempDir(), "no-deployment-here", "config.yaml")
	for name, c := range map[string]struct {
		stdin string
		argv  []string
	}{
		"enroll":          {bootstrapToken1253 + "\n" + newPW1253 + "\n", []string{"auth", "enroll", "--username", "admin", "--token-stdin", "--password-stdin"}},
		"forgot-password": {"", []string{"auth", "forgot-password", "--username", "admin"}},
		"reset-password":  {resetToken1253 + "\n" + newPW1253 + "\n", []string{"auth", "reset-password", "--token-stdin", "--password-stdin"}},
	} {
		code, stdout, stderr := runAuth1155(t, c.stdin, append(c.argv, "--config", cfg)...)
		if code == 0 || code == exitUsage {
			t.Errorf("%s with no address exited %d, want a plain failure", name, code)
		}
		if !strings.Contains(stderr, "RETND_API_URL") {
			t.Errorf("%s: the refusal does not name $RETND_API_URL: %q", name, stderr)
		}
		if strings.Contains(stderr, "RETND_API_USERNAME") || strings.Contains(stderr, "RETND_API_PASSWORD") {
			t.Errorf("%s: the refusal asks for credentials, and this verb signs in as nobody: %q", name, stderr)
		}
		noSecrets1253(t, name, stdout, stderr)
	}
}

func TestAuthForgotPassword1253_AsksForTheResetLinkAndSaysWhatItCannotKnow(t *testing.T) {
	engine := startFakeEngine(t, writeTestConfig(t))
	cfg := addressOnly(t, engine)
	rec := &anonymousRecorder{}
	engine.handleUnauthenticated("POST /auth/forgot-password", rec.handler(http.StatusNoContent))

	code, stdout, stderr := runAuth1155(t, "", "auth", "forgot-password", "--username", "admin", "--config", cfg)
	if code != 0 {
		t.Fatalf("auth forgot-password exited %d\n%s", code, stderr)
	}
	body, _ := rec.only(t)
	var got apicontract.ForgotPasswordRequest
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Username != "admin" {
		t.Errorf("the engine received %q (%v), want the username", body, err)
	}
	if sawRequest(engine, "POST /auth/login") {
		t.Error("forgot-password tried to sign in, and not knowing the password is the whole reason it exists")
	}
	// The route answers the same way for every username, so the verb has
	// to say that the link is on its way only IF the account matches, not
	// that it is on its way.
	if !strings.Contains(stdout, "if ") && !strings.Contains(stdout, "If ") {
		t.Errorf("the confirmation claims more than the engine said:\n%s", stdout)
	}

	for name, argv := range map[string][]string{
		"no username": {"auth", "forgot-password"},
		"an operand":  {"auth", "forgot-password", "admin"},
	} {
		if code, _, _ := runAuth1155(t, "", append(argv, "--config", cfg)...); code != exitUsage {
			t.Errorf("%s: exited %d, want the usage code", name, code)
		}
	}
}

func TestAuthResetPassword1253_RedeemsTheTokenAndSetsThePasswordFromStdin(t *testing.T) {
	engine := startFakeEngine(t, writeTestConfig(t))
	cfg := addressOnly(t, engine)
	rec := &anonymousRecorder{}
	engine.handleUnauthenticated("POST /auth/reset-password", rec.handler(http.StatusNoContent))

	code, stdout, stderr := runAuth1155(t, resetToken1253+"\n"+newPW1253+"\n",
		"auth", "reset-password", "--token-stdin", "--password-stdin", "--config", cfg)
	if code != 0 {
		t.Fatalf("auth reset-password exited %d\n%s", code, stderr)
	}
	body, _ := rec.only(t)
	var got apicontract.ResetPasswordRequest
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Token != resetToken1253 || got.NewPassword != newPW1253 {
		t.Errorf("the engine received %+v; the first stdin line is the token and the second the new password", got)
	}
	if sawRequest(engine, "POST /auth/login") {
		t.Error("reset-password tried to sign in before redeeming the token")
	}
	// Redeeming the token signs every session out and opens none, and an
	// operator who does not hear that reads the next 401 as a failure.
	if !strings.Contains(stdout, "sign") {
		t.Errorf("the confirmation does not say to sign in with the new password:\n%s", stdout)
	}
	noSecrets1253(t, "auth reset-password", stdout, stderr)

	for name, argv := range map[string][]string{
		"no token":               {"auth", "reset-password", "--password-stdin"},
		"no password":            {"auth", "reset-password", "--token-stdin"},
		"the token as a flag":    {"auth", "reset-password", "--token", resetToken1253, "--password-stdin"},
		"the password as a flag": {"auth", "reset-password", "--token-stdin", "--password", newPW1253},
	} {
		code, stdout, stderr := runAuth1155(t, resetToken1253+"\n"+newPW1253+"\n", append(argv, "--config", cfg)...)
		if code != exitUsage {
			t.Errorf("%s: exited %d, want the usage code", name, code)
		}
		noSecrets1253(t, name, stdout, stderr)
	}
}

func TestAuthResetPassword1253_ASpentTokenIsRefusedWithTheEngineReason(t *testing.T) {
	engine := startFakeEngine(t, writeTestConfig(t))
	cfg := addressOnly(t, engine)
	engine.handleUnauthenticated("POST /auth/reset-password", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusUnauthorized, apicontract.AuthErrorResponse{
			Code: apicontract.ErrorCodeResetTokenInvalid, Message: "that password reset link has expired or has already been used",
		})
	})
	code, stdout, stderr := runAuth1155(t, resetToken1253+"\n"+newPW1253+"\n",
		"auth", "reset-password", "--token-stdin", "--password-stdin", "--config", cfg)
	if code == 0 || code == exitUsage {
		t.Errorf("a refused reset exited %d, want a plain failure", code)
	}
	if !strings.Contains(stderr, "expired or has already been used") {
		t.Errorf("the engine's reason is not shown: %q", stderr)
	}
	noSecrets1253(t, "a refused reset", stdout, stderr)
}

func TestAuthWhoami1253_PrintsWhoTheCredentialsSignInAs(t *testing.T) {
	_, cfg := attachedEngine1155(t)
	code, stdout, stderr := runAuth1155(t, "", "auth", "whoami", "--config", cfg)
	if code != 0 {
		t.Fatalf("auth whoami exited %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "operator") {
		t.Errorf("auth whoami does not name the account:\n%s", stdout)
	}

	code, jsonOut, stderr := runAuth1155(t, "", "auth", "whoami", "--json", "--config", cfg)
	if code != 0 {
		t.Fatalf("auth whoami --json exited %d\n%s", code, stderr)
	}
	var got apicontract.SessionResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonOut)), &got); err != nil {
		t.Fatalf("--json did not print a JSON object: %v\n%s", err, jsonOut)
	}
	if got.Username != "operator" {
		t.Errorf("--json printed %+v, want the SessionResponse the API returned", got)
	}

	if code, _, _ := runAuth1155(t, "", "auth", "whoami", "extra", "--config", cfg); code != exitUsage {
		t.Errorf("an operand exited %d, want the usage code", code)
	}
}

func TestAuthWhoami1253_WorksFromARemoteTerminalAndSaysWhichDeployment(t *testing.T) {
	cfg, e := remoteFixture(t)
	code, stdout, stderr := runAuth1155(t, "", "auth", "whoami", "--config", cfg)
	if code != 0 {
		t.Fatalf("auth whoami exited %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "operator") {
		t.Errorf("auth whoami does not name the account:\n%s", stdout)
	}
	if !strings.Contains(stderr, remoteModeLine(e)) {
		t.Errorf("the remote mode line is missing:\n%s", stderr)
	}
}

func TestAuthWhoami1253_NeedsAnEngineAndSaysHowToReachOne(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	code, _, stderr := runAuth1155(t, "", "auth", "whoami", "--config", cfg)
	if code == 0 || code == exitUsage {
		t.Errorf("auth whoami with nothing serving exited %d, want a plain failure", code)
	}
	if !strings.Contains(stderr, "RETND_API_URL") {
		t.Errorf("the refusal does not name $RETND_API_URL: %q", stderr)
	}
}

// usage() lists the four verbs, and says the two tokens are stdin only.
func TestUsage1253_NamesTheNewAuthVerbs(t *testing.T) {
	text := captureStderr(t, usage)
	for _, want := range []string{
		"  auth enroll --username U --token-stdin --password-stdin",
		"  auth forgot-password --username U",
		"  auth reset-password --token-stdin --password-stdin",
		"  auth whoami",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("usage() does not list %q", want)
		}
	}
	block := usageBlock(t, text, "  auth enroll", "  auth password", "  catalog ")
	if !strings.Contains(block, "stdin") || !strings.Contains(block, "POST /api/v1/auth/enroll") {
		t.Errorf("the enroll entry does not say the token is stdin only, or name its route:\n%s", block)
	}
}

// The enrollment token is read from stdin even when stdin is a file with
// no trailing newline, which is what `printf '%s' "$TOKEN" |` produces.
func TestAuthEnroll1253_AStdinWithNoTrailingNewlineStillCarriesBothLines(t *testing.T) {
	engine, _, _ := firstRunFixture(t)
	cfg := addressOnly(t, engine.fakeEngine)
	rec := &anonymousRecorder{}
	engine.handleUnauthenticated("POST /auth/enroll", rec.handler(http.StatusNoContent))

	code, _, stderr := runAuth1155(t, bootstrapToken1253+"\n"+newPW1253,
		"auth", "enroll", "--username", "admin", "--token-stdin", "--password-stdin", "--config", cfg)
	if code != 0 {
		t.Fatalf("auth enroll exited %d\n%s", code, stderr)
	}
	body, bootstrap := rec.only(t)
	var got apicontract.EnrollRequest
	_ = json.Unmarshal([]byte(body), &got)
	if bootstrap != bootstrapToken1253 || got.Password != newPW1253 {
		t.Errorf("token %q, password sent %v; want both lines read", bootstrap, got.Password == newPW1253)
	}
}

// A guard against the fixture itself: the unauthenticated hook refuses a
// request with no double-submit token, the way the real router does, so a
// verb that skipped the cookie handshake would fail here too.
func TestFakeEngine1253_UnauthenticatedRoutesStillWantTheDoubleSubmitToken(t *testing.T) {
	engine := startFakeEngine(t, writeTestConfig(t))
	engine.handleUnauthenticated("POST /auth/forgot-password", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	resp, err := http.Post(engine.baseURL()+apicontract.BasePath+"/auth/forgot-password", "application/json", strings.NewReader(`{"username":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a bare POST got %d, want 403 for the missing double-submit token", resp.StatusCode)
	}
}
