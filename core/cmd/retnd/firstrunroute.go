package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/retnd/retnd/core/apicontract"
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

// The bounds the first-run route runs under (#1254's review).
//
// The attach is a sign-in and one read, so it gets a short bound of its
// own rather than apiclient's thirty seconds per request, plus whatever a
// login spends sitting out a 429: an engine that will not answer that
// quickly is reported as not answering, and Ctrl-C ends the wait. Ten
// seconds rather than five because the sign-in is an Argon2id check on
// whatever CPU the NAS has.
//
// POST /system/first-run is the opposite. The handler proves the
// connection and then brings the engine's service up in place under
// apps/common/webhost's activationTimeout (ten minutes: a journal open and
// possibly a first migration), so this one call waits longer than that.
// Every other call on the route keeps the thirty seconds it has always
// had. They are variables so a test can shorten them.
var (
	firstRunAttachTimeout   = 10 * time.Second
	firstRunCompleteTimeout = 12 * time.Minute
)

// firstRunCallTimeout bounds every other request on a first-run route,
// the same thirty seconds apiclient gives a request by default. The
// route's client is built with the longer bound above, so each call says
// its own.
const firstRunCallTimeout = 30 * time.Second

// firstRunFollowUpTimeout bounds the one read a first run that went quiet
// gets afterwards, to find out whether it happened.
const firstRunFollowUpTimeout = 10 * time.Second

// firstRunEngineRoute is the route a first configuration, and the key
// store verbs beside it, take to an engine still on its first run.
//
// A type of its own rather than the engineRoute a configured write gets,
// for two reasons. It is typed all the way from the attach to the write, so
// nothing has to assert its way from a configured route to a first-run
// one. And it carries the bounds above: its client waits as long as the
// slowest call needs, and every call says how long it may take.
type firstRunEngineRoute struct {
	engine *engineRoute
}

// firstConfigRoute is what a routed first configuration needs: the key and
// trust steps a create runs, the connection check in front of the write,
// and the write itself. The direct path keeps calling *service.FirstRun,
// which has no CompleteFirstRun because in that process there is nobody to
// hand anything to.
type firstConfigRoute interface {
	backupSetCreatePrereqs
	sourceConnectionTester
	CompleteFirstRun(ctx context.Context, req service.CreateBackupSetRequest) (set service.BackupSet, restartRequired bool, err error)
}

var (
	_ firstConfigRoute = (*firstRunEngineRoute)(nil)
	_ sshKeyRoute      = (*firstRunEngineRoute)(nil)
)

func (r *firstRunEngineRoute) ImportSSHKey(ctx context.Context, raw []byte, passphrase string) (service.SSHKeyRef, error) {
	ctx, cancel := context.WithTimeout(ctx, firstRunCallTimeout)
	defer cancel()
	return r.engine.ImportSSHKey(ctx, raw, passphrase)
}

func (r *firstRunEngineRoute) ProbeHostKey(ctx context.Context, host string, port int) (service.HostKeyProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, firstRunCallTimeout)
	defer cancel()
	return r.engine.ProbeHostKey(ctx, host, port)
}

func (r *firstRunEngineRoute) TestConnection(ctx context.Context, req service.ConnectionTestRequest) (service.ConnectionTestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, firstRunCallTimeout)
	defer cancel()
	return r.engine.TestConnection(ctx, req)
}

func (r *firstRunEngineRoute) ListSSHKeys(ctx context.Context) ([]service.SSHKeyListing, error) {
	ctx, cancel := context.WithTimeout(ctx, firstRunCallTimeout)
	defer cancel()
	return r.engine.ListSSHKeys(ctx)
}

func (r *firstRunEngineRoute) DiscoverSSHKeyCandidates(ctx context.Context) (service.SSHKeyDiscovery, error) {
	ctx, cancel := context.WithTimeout(ctx, firstRunCallTimeout)
	defer cancel()
	return r.engine.DiscoverSSHKeyCandidates(ctx)
}

func (r *firstRunEngineRoute) ImportSSHKeyCandidate(ctx context.Context, candidateID string) (service.SSHKeyRef, error) {
	ctx, cancel := context.WithTimeout(ctx, firstRunCallTimeout)
	defer cancel()
	return r.engine.ImportSSHKeyCandidate(ctx, candidateID)
}

// CompleteFirstRun is POST /system/first-run, under the long bound.
func (r *firstRunEngineRoute) CompleteFirstRun(ctx context.Context, req service.CreateBackupSetRequest) (service.BackupSet, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, firstRunCompleteTimeout)
	defer cancel()
	return r.engine.CompleteFirstRun(ctx, req)
}

// configuredNow asks the engine whether it has a configuration, for a
// first run whose reply never arrived.
func (r *firstRunEngineRoute) configuredNow(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, firstRunFollowUpTimeout)
	defer cancel()
	status, err := r.engine.client.FirstRunStatus(ctx)
	if err != nil {
		return false, err
	}
	return status.Configured, nil
}

// attachFirstRun is attachToEngine for the first configuration: the same
// three answers, with one more reason to answer "no route".
//
// No address is (nil, "", nil), as it is there. An engine that says it is
// already configured is (nil, "", nil) as well, deliberately, because
// that is the refusal this write has always had beside a configured
// deployment and #1254 changes nothing about it. An address that cannot be
// used, an engine that will not say what it is, and an unconfigured engine
// that is a different deployment are routeRefusals, exactly as a named
// route that fails is everywhere else.
//
// It asks once. One GET /system/version says both whether the engine is
// configured and which deployment it is, so the two can never come from
// different answers, and it is the only request made before the route is
// handed back.
//
// The caller bounds ctx (firstRunAttachTimeout) and holds no claim on the
// journal while this runs: enterFirstConfigWriteMode gives it back first,
// for the reason it gives there.
func attachFirstRun(ctx context.Context, engine *service.RunningEngine) (*firstRunEngineRoute, string, error) {
	urlEnv, base := routeAddress()
	if base == "" {
		return nil, "", nil
	}
	client, err := newRouteClientWithin(urlEnv, base, firstRunCompleteTimeout+time.Minute)
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
	if err := confirmServedDeployment(address, served, engine); err != nil {
		return nil, "", err
	}
	return &firstRunEngineRoute{engine: &engineRoute{client: client}}, address, nil
}

// firstRunSignInHint is the one thing worth adding when a first-run engine
// turned the credentials down: on a fresh install the account they name
// may not exist yet. The rest of the sentence already says the sign-in
// failed; this says why that is likely here and nowhere else.
//
// The enrollment link comes first because it works while the engine is
// serving. `auth create-admin` refuses while a server holds the store, so
// it is only offered for a deployment that has not been started yet.
func firstRunSignInHint(err error) string {
	if !errors.Is(err, apiclient.ErrUnauthenticated) {
		return ""
	}
	return fmt.Sprintf(". On a fresh install the administrator $%s names has to exist before anything can sign in as it: enroll it through the link the container log or the installer's enroll-link prints (or, before the engine has ever been started, create it with `%s auth create-admin`)",
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
func createFirstConfigOnEngine(ctx context.Context, route *firstRunEngineRoute, address, keyFile, keyPassphrase string, trustHostKey, noVerify bool, req service.CreateBackupSetRequest) int {
	if err := resolveKeyAndTrust(ctx, route, keyFile, keyPassphrase, trustHostKey, &req); err != nil {
		return fail(err)
	}
	if code := proveSourceConnection(ctx, route, noVerify, connectionTestFor(req), cliecho.Binary+" backup-set test-connection "+req.SourceName+"/"+req.Name); code != 0 {
		return code
	}
	created, restartRequired, err := route.CompleteFirstRun(ctx, req)
	if err != nil {
		return firstRunDidNotAnswer(ctx, route, address, req, err)
	}
	fmt.Printf("the engine at %s wrote this deployment's first configuration\n", address)
	printBackupSet(created)
	if restartRequired {
		fmt.Println("  the engine wrote the configuration and could not start serving it in place; restart it to finish the first run")
	}
	return 0
}

// firstRunDidNotAnswer is what a first run that did not come back with a
// set says, and it says what is true rather than what the request saw.
//
// Two shapes need more than the error. Somebody else finishing first (the
// wizard, another terminal) comes back as the engine's 409, whose sentence
// is written for a browser ("reload and sign in"); a terminal needs to hear
// that its own set was not written and where to look instead. And a reply
// that never arrived (a slow activation, a connection dropped mid-way) is
// not "no engine answered": the engine may well have written the
// configuration. So that one is followed by a read of whether the engine
// is configured now, and the answer is what is reported.
func firstRunDidNotAnswer(ctx context.Context, route *firstRunEngineRoute, address string, req service.CreateBackupSetRequest, err error) int {
	var refused *apiclient.Error
	if errors.As(err, &refused) && refused.Code == apicontract.ErrorCodeAlreadyConfigured {
		return fail(fmt.Errorf("the engine at %s was configured by someone else while this ran, so nothing from this command was written; `%s sources` shows what it has now, and `%s backup-set create` adds %s/%s to it",
			address, cliecho.Binary, cliecho.Binary, req.SourceName, req.Name))
	}
	var silent *apiclient.Unreachable
	if !errors.As(err, &silent) {
		return fail(err)
	}
	// The cause rather than the whole error: apiclient words this one "no
	// engine answered", which is exactly what is not known here. The
	// engine took the request; what failed is its reply arriving.
	err = silent.Err
	// A context of its own: the one the request ran under may be the
	// very deadline that ended it.
	configured, readErr := route.configuredNow(context.WithoutCancel(ctx))
	switch {
	case readErr != nil:
		return fail(fmt.Errorf("the engine at %s did not reply to the first run in time (%v), and could not be asked afterwards whether it wrote it (%v); `%s system first-run` says whether this deployment is configured now",
			address, err, readErr, cliecho.Binary))
	case configured:
		fmt.Printf("the engine at %s is configured now, so the first configuration was written (by this command, unless something else finished first), but its reply did not arrive in time: %v\n", address, err)
		fmt.Printf("  `%s backup-set show %s/%s` shows the set as the engine has it\n", cliecho.Binary, req.SourceName, req.Name)
		return 0
	default:
		return fail(fmt.Errorf("the engine at %s did not reply to the first run in time and still has no configuration, so nothing from this command was written: %v", address, err))
	}
}
