package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/internal/config"
)

// `retnd auth ...` (issue #1155): the one administrator's password and
// recovery settings, from a terminal.
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
		return usageError("auth: expected `auth password`, `auth recovery show|set|test` or `auth verify-email`")
	}
	switch args[0] {
	case "password":
		return authPassword(args[1:])
	case "recovery":
		return authRecovery(args[1:])
	case "verify-email":
		return authVerifyEmail(args[1:])
	default:
		return usageError("auth: unknown subcommand %q (password, recovery and verify-email exist)", args[0])
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

// auth recovery set
func authRecoverySet(args []string) int {
	fs, cfgPath := newFlagSet("auth recovery set")
	currentStdin := fs.Bool("current-password-stdin", false, "read the current password from stdin, as the first line (required)")
	recoveryEmail := fs.String("recovery-email", "", "the address account recovery mails to")
	smtpHost := fs.String("smtp-host", "", "SMTP submission host")
	smtpPort := fs.Int("smtp-port", 587, "SMTP submission port")
	smtpSecurity := fs.String("smtp-security", "starttls", "SMTP connection security: starttls, tls or none")
	smtpUsername := fs.String("smtp-username", "", "SMTP username; empty for a relay that needs none")
	smtpFrom := fs.String("smtp-from", "", "address recovery mail is sent from")
	smtpPasswordStdin := fs.Bool("smtp-password-stdin", false, "read the SMTP password from stdin, as the line after the current password")
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
	smtpFlags := set["smtp-port"] || set["smtp-security"] || set["smtp-username"] || set["smtp-from"] || *smtpPasswordStdin
	if set["recovery-email"] && *recoveryEmail == "" {
		return usageError("auth recovery set: --recovery-email is empty")
	}
	if *smtpHost == "" && smtpFlags {
		return usageError("auth recovery set: the other --smtp-* flags need --smtp-host")
	}
	if !set["recovery-email"] && *smtpHost == "" {
		return usageError("auth recovery set: name something to change: --recovery-email, or --smtp-host with its companions")
	}

	ctx := context.Background()
	client, code := authClient(ctx, *cfgPath, "auth recovery set")
	if client == nil {
		return code
	}
	names := []string{"current password"}
	if *smtpPasswordStdin {
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
	if *smtpHost != "" {
		req.Smtp = &apicontract.SmtpSettings{
			Host: *smtpHost, Port: *smtpPort, Security: *smtpSecurity,
			Username: *smtpUsername, From: *smtpFrom,
		}
		if *smtpPasswordStdin {
			req.Smtp.Password = secrets[1]
		}
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
