package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/retnd/retnd/core/envcompat"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/service"
)

// The remote terminal mode (#1250's review).
//
// A handful of verbs can do nothing at all without the engine: operation
// list, show and run, assurance cancel, backup-set edit-hold, the auth verbs
// and activity --follow. Each answers from the memory of the process serving
// the deployment and never from a file. Until this, every one of them still
// insisted on a config.yaml of its own before it would dial, so a laptop or a
// CI runner that only had RETND_API_URL and the credentials could not ask the
// engine anything, while `system` and `version --engine` already dialled the
// URL directly.
//
// # When, and only when
//
// Remote only when this host has NO configuration and a clean probe of its
// default state database finds nothing serving it. Everything else keeps the
// rules it had. A host with a config.yaml whose deployment is stopped still
// refuses even with a URL exported, because that URL may name somebody
// else's engine and #535/#555 exist to stop a stray variable aiming a
// command at the wrong deployment. A probe that cannot answer refuses for
// the same reason: "I could not tell" is not "nothing is here".
//
// # Which deployment
//
// Every remote call says which deployment it reached, on the mode line. A
// verb that starts or stops work (operation run, assurance cancel) also
// wants that deployment named in advance with RETND_API_DEPLOYMENT_ID, and
// refuses without it, naming the id the engine reported so the operator can
// check it and set it. With the pin set, an engine that names a different
// deployment, or none, is refused before anything is sent. A deployment
// identity left on this host (a state database that once served) has to
// match too.
//
// # Transport
//
// Plain http to anything but loopback is refused unless
// RETND_API_INSECURE_TRANSPORT=acknowledged says the operator means it, and
// only that opt-in sends the acknowledgement header the engine asks a gated
// act over plain http for. RETND_API_CA_FILE trusts a self-signed reverse
// proxy; there is deliberately no switch that skips verification.

const (
	// apiDeploymentIDEnv pins the deployment a remote command may act on.
	apiDeploymentIDEnv = "RETND_API_DEPLOYMENT_ID"

	// apiInsecureTransportEnv is the opt-in for plain http to a host that
	// is not this machine, and its one accepted value.
	apiInsecureTransportEnv       = "RETND_API_INSECURE_TRANSPORT"
	apiInsecureTransportOptInWord = "acknowledged"

	// apiCAFileEnv names a PEM file of certificates to trust for an https
	// route, beside the system's own roots.
	apiCAFileEnv = "RETND_API_CA_FILE"
)

var (
	apiDeploymentIDRoute    = envcompat.Rename{Current: apiDeploymentIDEnv}
	apiInsecureTransportRte = envcompat.Rename{Current: apiInsecureTransportEnv}
	apiCAFileRoute          = envcompat.Rename{Current: apiCAFileEnv}
)

// insecureTransportAcknowledged reports whether the operator opted in to
// plain http to a host that is not this machine.
func insecureTransportAcknowledged() bool {
	return strings.TrimSpace(envcompat.Value(apiInsecureTransportRte)) == apiInsecureTransportOptInWord
}

// loopbackHost is isLoopbackHost's real answer: localhost, or an address in
// the loopback range. A name that merely resolves there is not trusted to,
// because what it resolves to is not this command's to promise.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// routeHTTPClient is the transport every route is built on: the default one,
// or one that also trusts the certificates RETND_API_CA_FILE names.
func routeHTTPClient() (*http.Client, error) {
	path := strings.TrimSpace(envcompat.Value(apiCAFileRoute))
	if path == "" {
		return nil, nil
	}
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("$%s names %s, which could not be read: %w", apiCAFileEnv, path, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("$%s names %s, which holds no PEM certificate", apiCAFileEnv, path)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Transport: transport}, nil
}

// transportRefusal is plain http to a host that is not this machine, with
// no opt-in. It exits exitInsecureTransport, the code the engine's own
// INSECURE_TRANSPORT_ACK_REQUIRED gets, because the remedy is the same.
type transportRefusal struct{ address string }

func (e *transportRefusal) Error() string {
	return fmt.Sprintf("%s is plain http to a host that is not this machine, so the password and the session would cross the network readable, and nothing was sent. Use https (with %s for a self-signed proxy), or set %s=%s to say you mean it",
		e.address, apiCAFileEnv, apiInsecureTransportEnv, apiInsecureTransportOptInWord)
}

// plainHTTPRefusal is the transport rule as every route without a
// deployment of its own applies it (the remote mode, and the three auth
// verbs that reach the engine as nobody): plain http to a host that is not
// this machine, with no opt-in, is refused with nothing sent.
func plainHTTPRefusal(base string) error {
	if u, err := url.Parse(base); err == nil && u.Scheme == "http" && !isLoopbackHost(u.Hostname()) && !insecureTransportAcknowledged() {
		return &transportRefusal{address: u.Scheme + "://" + u.Host}
	}
	return nil
}

// remoteRoute is a route this command took with no deployment of its own.
type remoteRoute struct {
	client     *apiclient.Client
	deployment string
}

// decision is the remote route as the read verbs consume one.
func (r *remoteRoute) decision() readDecision {
	return readDecision{mode: engineAttachedMode, client: r.client, remote: r.client.BaseURL()}
}

// hasLocalConfig reports whether this host has a configuration at cfgPath.
// Anything but "it is not there" counts as there, so a file that cannot be
// read is reported by the path that reads it rather than routed past.
func hasLocalConfig(cfgPath string) bool {
	_, err := os.Stat(config.ResolvePath(cfgPath))
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

// engineOnlyRemote settles whether an engine-only verb goes remote. It
// returns (nil, nil) when this host has a configuration, and the caller then
// keeps the path it always had. starts is true for a verb that starts or
// stops work, which wants the deployment pinned.
func engineOnlyRemote(ctx context.Context, cfgPath string, starts bool) (*remoteRoute, error) {
	if hasLocalConfig(cfgPath) {
		return nil, nil
	}
	db := service.StateDatabaseDefault()
	engine, err := service.DetectRunningEngineForJournal(db)
	if err != nil {
		return nil, fmt.Errorf("this host has no configuration at %s, and it could not be asked whether anything is serving its state database %s (%v), so this command does not go remote: a deployment here that could not be seen is not the same as none", config.ResolvePath(cfgPath), db, err)
	}
	if engine != nil {
		return nil, fmt.Errorf("this host has no configuration at %s, but a process is serving its state database %s, so this command does not go remote past it. Point --config at that deployment's configuration", config.ResolvePath(cfgPath), db)
	}
	urlEnv, base := routeAddress()
	if base == "" {
		return nil, fmt.Errorf("this host has no deployment of its own (no configuration at %s) and %s is not set, so there is no engine to ask. Set %s and the two credential variables beside it to the engine you mean", config.ResolvePath(cfgPath), apiURLEnv, apiURLEnv)
	}
	if err := plainHTTPRefusal(base); err != nil {
		return nil, err
	}
	client, err := newRouteClient(urlEnv, base)
	if err != nil {
		return nil, err
	}
	served, err := client.SystemVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("the engine at %s did not say which deployment it serves: %w", client.BaseURL(), err)
	}
	mine, err := service.DeploymentIdentity(db)
	if err != nil {
		return nil, fmt.Errorf("this host could not read the deployment identity beside %s: %w", db, err)
	}
	if mine != "" && served.DeploymentID != mine {
		return nil, fmt.Errorf("this host's state database %s belongs to deployment %s and the engine at %s serves %s, so nothing was sent", db, mine, client.BaseURL(), nameOrNone(served.DeploymentID))
	}
	pinned := strings.TrimSpace(envcompat.Value(apiDeploymentIDRoute))
	switch {
	case pinned != "" && served.DeploymentID == "":
		return nil, fmt.Errorf("%s pins deployment %s and the engine at %s named no deployment at all, so it cannot be shown to be that one and nothing was sent", apiDeploymentIDEnv, pinned, client.BaseURL())
	case pinned != "" && served.DeploymentID != pinned:
		return nil, fmt.Errorf("%s pins deployment %s and the engine at %s serves %s, so nothing was sent", apiDeploymentIDEnv, pinned, client.BaseURL(), served.DeploymentID)
	case pinned == "" && starts:
		return nil, fmt.Errorf("this starts or stops work on a deployment this host has no record of, so it wants that deployment named first. The engine at %s says it serves %s: check that is the one you mean and set %s=%s", client.BaseURL(), nameOrNone(served.DeploymentID), apiDeploymentIDEnv, nameOrNone(served.DeploymentID))
	}
	_, _ = fmt.Fprintf(os.Stderr, "%s%s (remote, deployment %s, %s)\n", modeLinePrefix, engineAttachedMode, nameOrNone(served.DeploymentID), client.BaseURL())
	return &remoteRoute{client: client, deployment: served.DeploymentID}, nil
}

func nameOrNone(id string) string {
	if id == "" {
		return "<none>"
	}
	return id
}

// noEngineRefusal is what an engine-only verb says when this host has a
// deployment of its own and no route to the process serving it. what is the
// verb's own half ("an edit hold is held in memory by the process serving
// this deployment"). The rest says what works where, and does not tell
// somebody who already exported a URL to export it.
func noEngineRefusal(what string) error {
	_, base := routeAddress()
	if base != "" {
		return fmt.Errorf("%s, and nothing on this host is serving that deployment. %s is set, but this host has a deployment of its own, so the address is not used: it could name a different deployment's engine. Start this deployment's engine, or run the command from a terminal with no deployment of its own, where %s and the two credential variables beside it are enough", what, apiURLEnv, apiURLEnv)
	}
	return fmt.Errorf("%s, and this command has no route to one. Beside the engine (inside its container, or on its host) set %s and the two credential variables beside it; from a terminal with no deployment of its own the same three are enough, with %s for a verb that starts or stops work", what, apiURLEnv, apiDeploymentIDEnv)
}
