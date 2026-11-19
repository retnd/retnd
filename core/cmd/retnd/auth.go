package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/internal/config"
)

// `retnd auth ...` (issue #1155): the one administrator's password and
// recovery settings, from a terminal. #1253 added the acts a browser does
// with no session (enroll, forgot-password, reset-password) and the
// question it asks on every load (whoami).
//
// # Routed only
//
// The account belongs to the process serving the deployment: its store is
// locked by that process, its sessions live in that process's memory, and a
// password changed under it would leave signed-in sessions standing on a
// credential that no longer exists. So every verb here is the same call the
// browser makes, to the engine, and refuses without a route to one, saying
// how to reach one and what to do when there is none and the account is
// locked out (`retnd-web auth set-password`, offline).
//
// Three of them reach the engine as nobody (anonymousAuthClient): there is
// no account before enrollment, and the reset flow exists because the
// password is lost. Those need the address and nothing else.
//
// # Secrets
//
// Every secret arrives on stdin, one per line, in the order the flags name
// them, and is held in memory for the one request. None is accepted as a
// flag (an unknown flag is a usage error, which is what keeps `--password x`
// out of argv and the shell history), none is printed, and none is written
// anywhere. Flag and operand mistakes are settled BEFORE stdin is read, so a
// usage error never consumes a secret it was never going to use.
func cmdAuth(args []string) int {
	if len(args) == 0 {
		return usageError("auth: expected `auth enroll`, `auth password`, `auth forgot-password`, `auth reset-password`, `auth recovery show|set|test`, `auth verify-email` or `auth whoami`")
	}
	switch args[0] {
	case "enroll":
		return authEnroll(args[1:])
	case "password":
		return authPassword(args[1:])
	case "forgot-password":
		return authForgotPassword(args[1:])
	case "reset-password":
		return authResetPassword(args[1:])
	case "recovery":
		return authRecovery(args[1:])
	case "verify-email":
		return authVerifyEmail(args[1:])
	case "whoami":
		return authWhoami(args[1:])
	default:
		return usageError("auth: unknown subcommand %q (enroll, password, forgot-password, reset-password, recovery, verify-email and whoami exist)", args[0])
	}
}

// authClient settles which engine a verb talks to, or refuses.
//
// It does not go through enterReadMode, and the difference is deliberate.
// A read compares the configuration this command loaded with the one the
// engine serves, because it is going to print the engine's world next to
// this file's. These verbs print nothing from the file: the account is not
// in config.yaml, so a revision comparison would refuse a perfectly good
// route over a fact that has nothing to do with it. What does matter is
// that the engine reached is THIS deployment's, and attachToEngine settles
// exactly that, the way every routed write does.
//
// From a terminal with no deployment of its own the verbs go remote
// (remote.go), unpinned like the other reads: the engine has to be told the
// current password before it changes anything, which is a check of its own.
func authClient(ctx context.Context, cfgPath, verb string) (*apiclient.Client, int) {
	remote, err := engineOnlyRemote(ctx, cfgPath, false)
	if err != nil {
		return nil, fail(fmt.Errorf("%s: %w", verb, err))
	}
	if remote != nil {
		return remote.client, exitOK
	}
	if _, err := config.LoadAndValidate(cfgPath); err != nil {
		return nil, fail(fmt.Errorf("config: %w", err))
	}
	noRoute := fmt.Errorf("%w. If the account is locked out and nothing is serving, `%s auth set-password --username U --password-stdin` resets it offline",
		noEngineRefusal(verb+" acts on the administrator's account, which belongs to the process serving this deployment"), cliecho.WebBinary)

	engine, err := detectRunningEngine(cfgPath)
	if err != nil {
		return nil, fail(fmt.Errorf("%s: this host could not be asked whether any process is serving this deployment: %w", verb, err))
	}
	if engine == nil {
		return nil, fail(noRoute)
	}
	route, _, err := attachToEngine(ctx, engine)
	if err != nil {
		return nil, fail(fmt.Errorf("%s: %w", verb, err))
	}
	er, ok := route.(*engineRoute)
	if !ok || er == nil {
		return nil, fail(noRoute)
	}
	return er.client, exitOK
}

// anonymousAuthClient is authClient for the three verbs that reach the
// engine with no session: enroll, forgot-password and reset-password.
//
// None of them can sign in. Before enrollment there is no account, and the
// reset flow is for an operator who has lost the password. Each is
// authorised by something else: the single-use bootstrap token out of the
// enrollment link, the single-use reset token out of the emailed link, or
// (forgot-password) nothing at all, because the engine mails the address it
// already holds and answers the same way whatever it was asked. So the
// route is $RETND_API_URL alone, with the transport rule every route has
// (plain http past loopback needs the opt-in) and without the deployment
// check a routed write makes, because GET /system/version is answered only
// to a session and there is none to show. A wrong engine refuses the token;
// that is the check here, and it is the engine's rather than this command's.
//
// Beside a deployment of its own (a config.yaml at --config) the verb still
// wants a process serving it, for the reason authClient does: an exported
// address beside a stopped engine may name somebody else's. With no
// deployment of its own (the headless first-run container, a laptop) the
// address is the whole route, and the default journal is deliberately not
// probed: on a headless install the first-run engine is serving exactly
// that journal, and refusing to enroll past it would be the gap #1253 is
// closing.
func anonymousAuthClient(cfgPath, verb string) (*apiclient.Client, int) {
	noRoute := fmt.Errorf("%s is sent to the process serving this deployment as nobody in particular (there is no account to sign in as), and this command has no route to one. Set $%s to the engine's address (http://127.0.0.1:8080 from inside its container, or the published Web UI port); the two credential variables are not needed for this verb",
		verb, apiURLEnv)
	if hasLocalConfig(cfgPath) {
		if _, err := config.LoadAndValidate(cfgPath); err != nil {
			return nil, fail(fmt.Errorf("config: %w", err))
		}
		engine, err := detectRunningEngine(cfgPath)
		if err != nil {
			return nil, fail(fmt.Errorf("%s: this host could not be asked whether any process is serving this deployment: %w", verb, err))
		}
		if engine == nil {
			return nil, fail(fmt.Errorf("%s acts on the administrator's account, which belongs to the process serving this deployment, and nothing on this host is serving it. Start this deployment's engine; with it up, $%s names it (http://127.0.0.1:8080 from inside its container, or the published Web UI port) and the two credential variables are not needed for this verb",
				verb, apiURLEnv))
		}
	}
	urlEnv, base := routeAddress()
	if base == "" {
		return nil, fail(noRoute)
	}
	if err := plainHTTPRefusal(base); err != nil {
		return nil, fail(err)
	}
	client, err := newRouteClient(urlEnv, base)
	if err != nil {
		return nil, fail(fmt.Errorf("%s: %w", verb, err))
	}
	return client, exitOK
}

// bootstrapTokenFromLine is the enrollment token out of what was typed:
// the token itself, or the whole enrollment link the container log and
// the installer print, which is what an operator has in front of them.
func bootstrapTokenFromLine(line string) string {
	line = strings.TrimSpace(line)
	u, err := url.Parse(line)
	if err != nil || u.Scheme == "" {
		return line
	}
	if token := u.Query().Get("token"); token != "" {
		return token
	}
	return line
}

// stdinLines reads up to n lines from r. A trailing CR is dropped with the
// LF so a CRLF source gives the secret that was typed and not that secret
// plus a carriage return. Fewer lines than asked for is reported by the
// caller, which knows what each line was.
func stdinLines(r io.Reader, n int) ([]string, error) {
	br := bufio.NewReader(r)
	out := make([]string, 0, n)
	for len(out) < n {
		line, err := br.ReadString('\n')
		if line != "" || err == nil {
			out = append(out, strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
	}
	return out, nil
}

// secretsFromStdin reads one secret per name, in order, refusing a missing
// or empty one by NAME. Neither the values nor their lengths are reported.
func secretsFromStdin(verb string, names ...string) ([]string, error) {
	lines, err := stdinLines(os.Stdin, len(names))
	if err != nil {
		return nil, fmt.Errorf("%s: reading stdin: %w", verb, err)
	}
	for i, name := range names {
		if i >= len(lines) || lines[i] == "" {
			return nil, fmt.Errorf("%s: stdin carried no %s; pipe the secrets in as separate lines, in this order: %s", verb, name, strings.Join(names, ", "))
		}
	}
	return lines, nil
}

func printVerbJSON(verb string, v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fail(fmt.Errorf("%s: writing the result: %w", verb, err))
	}
	return exitOK
}

// auth password --password-stdin
func authPassword(args []string) int {
	fs, cfgPath := newFlagSet("auth password")
	stdin := fs.Bool("password-stdin", false, "read the current password and then the new one from stdin, one per line (required)")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("auth password: takes no operands, got %q", operands[0])
	}
	if !*stdin {
		return usageError("auth password: --password-stdin is required (this command never accepts a password as a flag); pipe the current and then the new password in as two lines")
	}

	ctx := context.Background()
	client, code := authClient(ctx, *cfgPath, "auth password")
	if client == nil {
		return code
	}
	secrets, err := secretsFromStdin("auth password", "current password", "new password")
	if err != nil {
		return fail(err)
	}
	if err := client.RotatePassword(ctx, apicontract.RotatePasswordRequest{
		CurrentPassword: secrets[0],
		NewPassword:     secrets[1],
	}); err != nil {
		return fail(fmt.Errorf("auth password: %w", err))
	}
	fmt.Println("the administrator's password was changed")
	return exitOK
}

func authRecovery(args []string) int {
	if len(args) == 0 {
		return usageError("auth recovery: expected `show`, `set` or `test`")
	}
	switch args[0] {
	case "show":
		return authRecoveryShow(args[1:])
	case "set":
		return authRecoverySet(args[1:])
	case "test":
		return authRecoveryTest(args[1:])
	default:
		return usageError("auth recovery: unknown subcommand %q (show, set and test exist)", args[0])
	}
}

func authRecoveryShow(args []string) int {
	fs, cfgPath := newFlagSet("auth recovery show")
	asJSON := fs.Bool("json", false, "print the engine's answer as JSON, the same object the API serves")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("auth recovery show: takes no operands, got %q", operands[0])
	}
	ctx := context.Background()
	client, code := authClient(ctx, *cfgPath, "auth recovery show")
	if client == nil {
		return code
	}
	got, err := client.GetRecoverySettings(ctx)
	if err != nil {
		return fail(fmt.Errorf("auth recovery show: %w", err))
	}
	if *asJSON {
		return printVerbJSON("auth recovery show", got)
	}
	printRecovery(got)
	return exitOK
}

func printRecovery(got apicontract.RecoverySettingsResponse) {
	if got.RecoveryEmail == "" {
		fmt.Println("recovery email: not set")
	} else {
		fmt.Printf("recovery email: %s (confirmed: %s, verified: %s)\n", got.RecoveryEmail, yesNo(got.RecoveryEmailConfirmed), yesNo(got.RecoveryEmailVerified))
	}
	if got.VerificationDeadline != "" {
		fmt.Printf("verification deadline: %s\n", got.VerificationDeadline)
	}
	if got.Smtp == nil {
		fmt.Println("smtp: not set")
		return
	}
	s := got.Smtp
	fmt.Printf("smtp: %s:%d (%s), user %q, from %s, password %s\n", s.Host, s.Port, s.Security, s.Username, s.From, map[bool]string{true: "set", false: "not set"}[s.PasswordSet])
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// smtpFlags is the SMTP endpoint as `auth recovery set` and `auth enroll`
// both take it: the same five flags, and the password as a stdin line.
type smtpFlags struct {
	host, security, username, from *string
	port                           *int
	passwordStdin                  *bool
}

func addSMTPFlags(fs *flag.FlagSet, passwordLine string) *smtpFlags {
	return &smtpFlags{
		host:          fs.String("smtp-host", "", "SMTP submission host"),
		port:          fs.Int("smtp-port", 587, "SMTP submission port"),
		security:      fs.String("smtp-security", "starttls", "SMTP connection security: starttls, tls or none"),
		username:      fs.String("smtp-username", "", "SMTP username; empty for a relay that needs none"),
		from:          fs.String("smtp-from", "", "address recovery mail is sent from"),
		passwordStdin: fs.Bool("smtp-password-stdin", false, "read the SMTP password from stdin, as "+passwordLine),
	}
}

// companionsSet reports whether any flag other than --smtp-host was given,
// which is a usage error without the host.
func (f *smtpFlags) companionsSet(set map[string]bool) bool {
	return set["smtp-port"] || set["smtp-security"] || set["smtp-username"] || set["smtp-from"] || *f.passwordStdin
}

func (f *smtpFlags) settings(password string) apicontract.SmtpSettings {
	return apicontract.SmtpSettings{
		Host: *f.host, Port: *f.port, Security: *f.security,
		Username: *f.username, From: *f.from, Password: password,
	}
}

// auth recovery set
func authRecoverySet(args []string) int {
	fs, cfgPath := newFlagSet("auth recovery set")
	currentStdin := fs.Bool("current-password-stdin", false, "read the current password from stdin, as the first line (required)")
	recoveryEmail := fs.String("recovery-email", "", "the address account recovery mails to")
	smtp := addSMTPFlags(fs, "the line after the current password")
	asJSON := fs.Bool("json", false, "print the engine's answer as JSON, the same object the API serves")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("auth recovery set: takes no operands, got %q", operands[0])
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !*currentStdin {
		return usageError("auth recovery set: --current-password-stdin is required (the engine asks for the current password on every change, and this command never accepts one as a flag)")
	}
	if set["recovery-email"] && *recoveryEmail == "" {
		return usageError("auth recovery set: --recovery-email is empty")
	}
	if *smtp.host == "" && smtp.companionsSet(set) {
		return usageError("auth recovery set: the other --smtp-* flags need --smtp-host")
	}
	if !set["recovery-email"] && *smtp.host == "" {
		return usageError("auth recovery set: name something to change: --recovery-email, or --smtp-host with its companions")
	}

	ctx := context.Background()
	client, code := authClient(ctx, *cfgPath, "auth recovery set")
	if client == nil {
		return code
	}
	names := []string{"current password"}
	if *smtp.passwordStdin {
		names = append(names, "SMTP password")
	}
	secrets, err := secretsFromStdin("auth recovery set", names...)
	if err != nil {
		return fail(err)
	}

	req := apicontract.RecoverySettingsUpdate{CurrentPassword: secrets[0]}
	if set["recovery-email"] {
		req.RecoveryEmail = recoveryEmail
	}
	if *smtp.host != "" {
		smtpPassword := ""
		if *smtp.passwordStdin {
			smtpPassword = secrets[1]
		}
		settings := smtp.settings(smtpPassword)
		req.Smtp = &settings
	}
	got, err := client.UpdateRecoverySettings(ctx, req)
	if err != nil {
		return fail(fmt.Errorf("auth recovery set: %w", err))
	}
	if *asJSON {
		return printVerbJSON("auth recovery set", got)
	}
	printRecovery(got)
	return exitOK
}

func authRecoveryTest(args []string) int {
	fs, cfgPath := newFlagSet("auth recovery test")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("auth recovery test: takes no operands, got %q", operands[0])
	}
	ctx := context.Background()
	client, code := authClient(ctx, *cfgPath, "auth recovery test")
	if client == nil {
		return code
	}
	if err := client.SendRecoveryTestEmail(ctx); err != nil {
		return fail(fmt.Errorf("auth recovery test: %w", err))
	}
	fmt.Println("a test message was sent to the recovery address")
	return exitOK
}

// auth verify-email --token-stdin | --resend
func authVerifyEmail(args []string) int {
	fs, cfgPath := newFlagSet("auth verify-email")
	tokenStdin := fs.Bool("token-stdin", false, "read the verification token out of the emailed link from stdin")
	resend := fs.Bool("resend", false, "ask for the verification message to be sent again")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("auth verify-email: takes no operands, got %q", operands[0])
	}
	if *tokenStdin == *resend {
		return usageError("auth verify-email: pass exactly one of --token-stdin (redeem a token) or --resend (send the message again)")
	}

	ctx := context.Background()
	client, code := authClient(ctx, *cfgPath, "auth verify-email")
	if client == nil {
		return code
	}
	if *resend {
		if err := client.ResendRecoveryEmailVerification(ctx); err != nil {
			return fail(fmt.Errorf("auth verify-email: %w", err))
		}
		fmt.Println("the verification message was sent again")
		return exitOK
	}
	secrets, err := secretsFromStdin("auth verify-email", "verification token")
	if err != nil {
		return fail(err)
	}
	if err := client.VerifyRecoveryEmail(ctx, apicontract.VerifyEmailRequest{Token: secrets[0]}); err != nil {
		return fail(fmt.Errorf("auth verify-email: %w", err))
	}
	fmt.Println("the recovery address is verified")
	return exitOK
}

// auth enroll --username U --token-stdin --password-stdin
//
// The first administrator, through POST /auth/enroll, the request the
// enrollment page sends, with the bootstrap token out of the enrollment
// link in the header that page puts it in. It is the headless half of the
// first run (#1254 handed the first configuration to the serving engine;
// this hands it the account): `retnd-web auth create-admin` writes the
// store offline and refuses while a server holds it, and on a headless
// install the server is the container the command is typed in.
//
// The token is the first stdin line and the password the second, so that
// the line copied out of the container log goes in first and the secret
// the operator chose last. The whole enrollment link is accepted in place
// of the token.
func authEnroll(args []string) int {
	fs, cfgPath := newFlagSet("auth enroll")
	username := fs.String("username", "", "the administrator's username (required)")
	tokenStdin := fs.Bool("token-stdin", false, "read the enrollment token, or the whole enrollment link, from stdin as the first line (required)")
	passwordStdin := fs.Bool("password-stdin", false, "read the administrator's password from stdin as the second line (required)")
	recoveryEmail := fs.String("recovery-email", "", "the address account recovery mails to; needs the --smtp-* flags, and the engine sends a verification message to it before it keeps anything")
	smtp := addSMTPFlags(fs, "the third line")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("auth enroll: takes no operands, got %q", operands[0])
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if *username == "" {
		return usageError("auth enroll: --username is required")
	}
	if !*tokenStdin || !*passwordStdin {
		return usageError("auth enroll: --token-stdin and --password-stdin are both required (this command never accepts a token or a password as a flag); pipe the enrollment link or its token and then the password in as two lines")
	}
	if *recoveryEmail == "" && set["recovery-email"] {
		return usageError("auth enroll: --recovery-email is empty")
	}
	if (*recoveryEmail == "") != (*smtp.host == "") {
		return usageError("auth enroll: --recovery-email and --smtp-host go together; the engine holds a recovery address only beside an endpoint it has delivered to, and the other way round. Leave both out to enroll without recovery and finish it later with `auth recovery set`")
	}
	if *smtp.host == "" && smtp.companionsSet(set) {
		return usageError("auth enroll: the other --smtp-* flags need --smtp-host")
	}

	ctx := context.Background()
	client, code := anonymousAuthClient(*cfgPath, "auth enroll")
	if client == nil {
		return code
	}
	names := []string{"enrollment token", "password"}
	if *smtp.passwordStdin {
		names = append(names, "SMTP password")
	}
	secrets, err := secretsFromStdin("auth enroll", names...)
	if err != nil {
		return fail(err)
	}
	req := apicontract.EnrollRequest{Username: *username, Password: secrets[1]}
	if *recoveryEmail != "" {
		smtpPassword := ""
		if *smtp.passwordStdin {
			smtpPassword = secrets[2]
		}
		req.RecoveryEmail = *recoveryEmail
		req.Smtp = smtp.settings(smtpPassword)
	}
	if err := client.Enroll(ctx, req, bootstrapTokenFromLine(secrets[0])); err != nil {
		return fail(fmt.Errorf("auth enroll: %w", err))
	}
	fmt.Printf("the engine at %s enrolled %s as the administrator\n", client.BaseURL(), *username)
	if *recoveryEmail != "" {
		fmt.Printf("  a verification message was sent to %s; `%s auth verify-email --token-stdin` redeems the token out of it\n", *recoveryEmail, cliecho.Binary)
	} else {
		fmt.Printf("  no recovery address yet: `%s auth recovery set` adds one, and until then a lost password has only the offline way back (`%s auth set-password`)\n", cliecho.Binary, cliecho.WebBinary)
	}
	fmt.Printf("  every routed command signs in as it with $%s and $%s\n", apiUsernameEnv, apiPasswordEnv)
	return exitOK
}

// auth forgot-password --username U
//
// POST /auth/forgot-password answers 204 for every username, before any
// mail is attempted, so that nobody on the network can confirm the
// administrator's name. This command therefore cannot know whether a
// message went out, and says so rather than claiming one did.
func authForgotPassword(args []string) int {
	fs, cfgPath := newFlagSet("auth forgot-password")
	username := fs.String("username", "", "the administrator's username (required)")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("auth forgot-password: takes no operands, got %q", operands[0])
	}
	if *username == "" {
		return usageError("auth forgot-password: --username is required")
	}
	ctx := context.Background()
	client, code := anonymousAuthClient(*cfgPath, "auth forgot-password")
	if client == nil {
		return code
	}
	if err := client.RequestPasswordReset(ctx, apicontract.ForgotPasswordRequest{Username: *username}); err != nil {
		return fail(fmt.Errorf("auth forgot-password: %w", err))
	}
	fmt.Printf("the engine at %s took the request. If %s is the administrator and the account has a recovery address, a reset link is on its way to it, good once and for 30 minutes; `%s auth reset-password --token-stdin --password-stdin` redeems it\n", client.BaseURL(), *username, cliecho.Binary)
	fmt.Println("  the engine answers the same way whether or not that account exists, so if no mail arrives check the username and the recovery address (`auth recovery show`, from a signed-in session)")
	return exitOK
}

// auth reset-password --token-stdin --password-stdin
//
// The other end of the emailed link: the token is the first stdin line and
// the new password the second. Redeeming it signs every session out and
// opens none, so the next thing to do is sign in with the new password.
func authResetPassword(args []string) int {
	fs, cfgPath := newFlagSet("auth reset-password")
	tokenStdin := fs.Bool("token-stdin", false, "read the reset token out of the emailed link from stdin as the first line (required)")
	passwordStdin := fs.Bool("password-stdin", false, "read the new password from stdin as the second line (required)")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("auth reset-password: takes no operands, got %q", operands[0])
	}
	if !*tokenStdin || !*passwordStdin {
		return usageError("auth reset-password: --token-stdin and --password-stdin are both required (this command never accepts a token or a password as a flag); pipe the token and then the new password in as two lines")
	}
	ctx := context.Background()
	client, code := anonymousAuthClient(*cfgPath, "auth reset-password")
	if client == nil {
		return code
	}
	secrets, err := secretsFromStdin("auth reset-password", "reset token", "new password")
	if err != nil {
		return fail(err)
	}
	if err := client.ResetPassword(ctx, apicontract.ResetPasswordRequest{Token: bootstrapTokenFromLine(secrets[0]), NewPassword: secrets[1]}); err != nil {
		return fail(fmt.Errorf("auth reset-password: %w", err))
	}
	fmt.Printf("the engine at %s set the administrator's password; every session was signed out and none was opened, so sign in with the new password\n", client.BaseURL())
	return exitOK
}

// auth whoami [--json]
//
// GET /auth/session, the question a browser asks on every load: who the
// credentials this terminal carries sign in as. The answer is the engine's,
// never this process's memory of an earlier call.
func authWhoami(args []string) int {
	fs, cfgPath := newFlagSet("auth whoami")
	asJSON := fs.Bool("json", false, "print the engine's answer as JSON, the same object the API serves")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("auth whoami: takes no operands, got %q", operands[0])
	}
	ctx := context.Background()
	client, code := authClient(ctx, *cfgPath, "auth whoami")
	if client == nil {
		return code
	}
	got, err := client.Session(ctx)
	if err != nil {
		return fail(fmt.Errorf("auth whoami: %w", err))
	}
	if *asJSON {
		return printVerbJSON("auth whoami", got)
	}
	fmt.Printf("signed in as %s at %s\n", got.Username, client.BaseURL())
	return exitOK
}
