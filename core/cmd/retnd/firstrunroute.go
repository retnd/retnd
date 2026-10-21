package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/service"
)

// Issue #1254: the first configuration, handed to an engine that is still
// serving its first-run setup flow.
//
// # Why this write has a route now
//
// It had none, on purpose, and the reasoning was that a process found
// serving the journal --state-database names is either past its first run
// or is a setup wizard the operator is standing in front of. The second
// half is not true of a headless install. There the engine serving the
// setup flow IS the container, so stopping it to let this command write
// config.yaml stops the container the command was typed in, and nobody is
// in front of the wizard to finish it. The terminal had no way through at
// all, while cliecho printed `backup-set create` as the twin of the very
// request the wizard sends.
//
// So the request the wizard sends is the one this sends: POST
// /system/first-run, with the same BackupSetSpec, to the process that will
// go on serving what it writes. Nothing is written on this side.
//
// # Why a configured engine still refuses
//
// The first half of that old reasoning is still true. POST
// /system/first-run is accepted once, and only by an instance with no
// configuration, so beside an engine that already has one (a mistyped
// --config, a config.yaml renamed out from under it) it is simply the wrong
// request. The command asks the engine which it is before anything else,
// and on "configured" it hands back no route at all, which is the refusal
// a first configuration beside a serving process has always had: exit 3,
// nothing sent, nothing written.
//
// # Why it asks "configured" before "which deployment"
//
// Every routed write confirms which deployment it reached before it sends
// anything (deploymentcheck.go), and this one does too. It asks the
// configured question first only so that the configured case stays
// byte-for-byte the refusal it was: a configured engine that is ALSO a
// different deployment is still told "a first configuration has no route
// here", rather than being reported as a mistyped address for a write that
// was never going to be sent to it either way.

// firstConfigRoute is what a routed first configuration needs: the key and
// trust steps a create runs, the connection check in front of the write,
// and the write itself. *engineRoute is the one implementation; the direct
// path keeps calling *service.FirstRun, which has no CompleteFirstRun
// because in that process there is nobody to hand anything to.
type firstConfigRoute interface {
	backupSetCreatePrereqs
	sourceConnectionTester
	CompleteFirstRun(ctx context.Context, req service.CreateBackupSetRequest) (set service.BackupSet, restartRequired bool, err error)
}

// attachToFirstRunEngine is attachToEngine for the first configuration:
// the same three answers, with one more reason to answer "no route".
//
// No address is (nil, "", nil), as it is there. An engine that says it is
// already configured is (nil, "", nil) as well, deliberately, because
// that is the refusal this write has always had beside a configured
// deployment and #1254 changes nothing about it. An address that cannot be
// used, an engine that will not say what it is, and an unconfigured engine
// that is a different deployment are routeRefusals, exactly as a named
// route that fails is everywhere else.
func attachToFirstRunEngine(ctx context.Context, engine *service.RunningEngine) (configWriteRoute, string, error) {
	urlEnv, base := routeAddress()
	if base == "" {
		return nil, "", nil
	}
	client, err := newRouteClient(urlEnv, base)
	if err != nil {
		return nil, "", err
	}
	address := client.BaseURL()
	served, err := client.SystemVersion(ctx)
	if err != nil {
		return nil, "", &routeRefusal{
			address: address,
			reason:  fmt.Sprintf("the engine at %s did not answer when this command asked whether it is still on its first run", address),
			detail: fmt.Sprintf("$%s names an engine that did not answer when this command asked whether it is still on its first run, so nothing was written: %v%s",
				routeEnvName(apiURLRoute), err, firstRunSignInHint(err)),
			cause: err,
		}
	}
	if served.Configured {
		return nil, "", nil
	}
	if err := confirmDeployment(ctx, client, engine); err != nil {
		return nil, "", err
	}
	return &engineRoute{client: client}, address, nil
}

// firstRunSignInHint is the one thing worth adding when a first-run engine
// turned the credentials down: on a fresh install the account they name
// may not exist yet. The rest of the sentence already says the sign-in
// failed; this says why that is likely here and nowhere else.
func firstRunSignInHint(err error) string {
	if !errors.Is(err, apiclient.ErrUnauthenticated) {
		return ""
	}
	return fmt.Sprintf(". On a fresh install the administrator $%s names has to exist before anything can sign in as it: enroll it through the link the container log or the installer's enroll-link prints, or create it with `%s auth create-admin` while nothing is serving",
		routeEnvName(apiUsernameRoute), cliecho.WebBinary)
}

// createFirstConfigOnEngine is createFirstConfig once the first
// configuration has a route: the same key, trust and connection steps the
// direct path runs, each against the engine, and then POST
// /system/first-run instead of a write here.
//
// It prints what the engine wrote, said as the engine's: the address, then
// the set exactly as it came back. restart_required is the engine telling
// us the configuration is durably written and it could not start serving
// it in place, which is a first run that happened, so it is said and the
// exit is still 0, as the wizard treats it.
func createFirstConfigOnEngine(ctx context.Context, route firstConfigRoute, address, keyFile, keyPassphrase string, trustHostKey, noVerify bool, req service.CreateBackupSetRequest) int {
	if err := resolveKeyAndTrust(ctx, route, keyFile, keyPassphrase, trustHostKey, &req); err != nil {
		return fail(err)
	}
	if code := proveSourceConnection(ctx, route, noVerify, connectionTestFor(req), cliecho.Binary+" backup-set test-connection "+req.SourceName+"/"+req.Name); code != 0 {
		return code
	}
	created, restartRequired, err := route.CompleteFirstRun(ctx, req)
	if err != nil {
		return fail(err)
	}
	fmt.Printf("the engine at %s wrote this deployment's first configuration\n", address)
	printBackupSet(created)
	if restartRequired {
		fmt.Println("  the engine wrote the configuration and could not start serving it in place; restart it to finish the first run")
	}
	return 0
}
