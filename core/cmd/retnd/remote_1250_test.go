package main

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1250's review, the remote terminal mode.
//
// The verbs that can do nothing without the engine (operation list, show and
// run, assurance cancel, backup-set edit-hold, the auth verbs and activity
// --follow) work from a terminal with no deployment of its own, when all it
// has is RETND_API_URL and the credentials. Only there: a host with a
// config.yaml keeps today's rules, a stopped local deployment with a URL
// exported still refuses, and so does a probe that cannot tell. A verb that
// starts or stops work wants the deployment pinned, every remote call says
// which deployment it reached, and plain http to anything but loopback needs
// an explicit opt-in.

// remoteFixture is an engine and a terminal with nothing of its own: a
// --config that names no file, and a default state database that is not
// there either.
func remoteFixture(t *testing.T) (cliConfig string, e *fakeEngine) {
	t.Helper()
	e = startFakeEngine(t, writeTestConfig(t))
	e.attach(t)
	dir := t.TempDir()
	t.Setenv(service.StateDatabaseEnv, filepath.Join(dir, "no-state", "state.db"))
	return filepath.Join(dir, "no-deployment-here", "config.yaml"), e
}

func pin(t *testing.T, id string) { t.Helper(); t.Setenv("RETND_API_DEPLOYMENT_ID", id) }

func remoteModeLine(e *fakeEngine) string {
	return "mode: engine-attached (remote, deployment " + e.svc.DeploymentID() + ", " + e.baseURL() + ")"
}

func TestRemote1250_EveryEngineOnlyVerbWorksWithNoLocalState(t *testing.T) {
	fastPolls(t)
	for _, c := range []struct {
		name   string
		args   []string
		pinned bool
		script func(e *fakeEngine)
		want   string
	}{
		{name: "operation list", args: []string{"operation", "list"}, want: "GET /operations",
			script: func(e *fakeEngine) { e.holdOperations(opOf("op-1", "scrub", "a/one", "running")) }},
		{name: "operation show", args: []string{"operation", "show", "op-1"}, want: "GET /operations/op-1",
			script: func(e *fakeEngine) { e.holdOperations(opOf("op-1", "scrub", "a/one", "completed")) }},
		{name: "operation run", args: []string{"operation", "run"}, pinned: true, want: "POST /operations",
			script: func(e *fakeEngine) {
				rec := &submissions{}
				e.handle("POST /operations", rec.handler(accepted(queuedRun("op-r", "run_cycle", ""))))
			}},
		{name: "assurance cancel", args: []string{"assurance", "cancel", "op-1"}, pinned: true, want: "POST /assurance/operations/op-1/cancel",
			script: func(e *fakeEngine) {
				e.handle("POST /assurance/operations/op-1/cancel", func(w http.ResponseWriter, _ *http.Request) {
					writeJSON(w, http.StatusAccepted, apicontract.AssuranceOperationCancelled{OperationID: "op-1", Kind: "scrub", BackupSetID: "a/one", Status: "cancelling"})
				})
			}},
		{name: "backup-set edit-hold", args: []string{"backup-set", "edit-hold", "a/one"}, want: "GET /backup-sets/a/one/edit-hold",
			script: func(e *fakeEngine) {
				e.handle("GET /backup-sets/a/one/edit-hold", func(w http.ResponseWriter, _ *http.Request) {
					writeJSON(w, http.StatusOK, apicontract.BackupSetEditHoldState{BackupSetID: "a/one"})
				})
			}},
		{name: "auth recovery show", args: []string{"auth", "recovery", "show"}, want: "GET /auth/recovery",
			script: func(e *fakeEngine) {
				e.handle("GET /auth/recovery", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, recoveryReply1155()) })
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cliCfg, e := remoteFixture(t)
			if c.pinned {
				pin(t, e.svc.DeploymentID())
			}
			c.script(e)
			code, out, errOut := runOp(t, cliCfg, c.args...)
			if code != 0 {
				t.Fatalf("%v from a terminal with no deployment exited %d\nstdout: %s\nstderr: %s", c.args, code, out, errOut)
			}
			if !containsRequest(e.requests(), c.want) {
				t.Errorf("the engine never saw %s; it saw %v", c.want, e.requests())
			}
			if !strings.Contains(errOut, remoteModeLine(e)) {
				t.Errorf("stderr does not say which deployment it reached, want %q:\n%s", remoteModeLine(e), errOut)
			}
		})
	}
}

func TestRemote1250_ActivityFollowWorksWithNoLocalState(t *testing.T) {
	cliCfg, e := remoteFixture(t)
	e.holdLiveActivity(oneLiveReading("epoch-1", 1, liveEvent(1, "info", "cycle_start", "remote cycle starting")))
	interruptOnceSeen(t, e, "GET /activity/live")
	code, out, errOut := runOp(t, cliCfg, "activity", "--follow")
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(errOut, remoteModeLine(e)) {
		t.Errorf("stderr does not carry the remote mode line:\n%s", errOut)
	}
}

// A verb that starts or stops work wants the pin; a read does not, and
// prints the id it reached instead (checked above).
func TestRemote1250_StartingOrStoppingWorkWantsThePin(t *testing.T) {
	for _, args := range [][]string{{"operation", "run"}, {"assurance", "cancel", "op-1"}} {
		t.Run(args[0], func(t *testing.T) {
			cliCfg, e := remoteFixture(t)
			code, _, errOut := runOp(t, cliCfg, args...)
			if code == 0 || code == exitUsage {
				t.Fatalf("%v with no pin exited %d, want a refusal\n%s", args, code, errOut)
			}
			if !strings.Contains(errOut, "RETND_API_DEPLOYMENT_ID") || !strings.Contains(errOut, e.svc.DeploymentID()) {
				t.Errorf("the refusal does not say which variable to set and what the engine says it is:\n%s", errOut)
			}
			for _, r := range e.requests() {
				if strings.HasPrefix(r, "POST /operations") || strings.HasPrefix(r, "POST /assurance") {
					t.Errorf("an unpinned %v still sent %s", args, r)
				}
			}
		})
	}
}

func TestRemote1250_AMismatchedOrEmptyPinRefuses(t *testing.T) {
	for _, c := range []struct {
		name      string
		anonymous bool
		pin       string
	}{
		{"mismatched", false, "0123456789abcdef0123456789abcdef"},
		{"engine names none", true, "0123456789abcdef0123456789abcdef"},
	} {
		for _, args := range [][]string{{"operation", "run"}, {"operation", "list"}} {
			t.Run(c.name+" "+args[1], func(t *testing.T) {
				cliCfg, e := remoteFixture(t)
				e.anonymous = c.anonymous
				pin(t, c.pin)
				e.holdOperations()
				code, _, errOut := runOp(t, cliCfg, args...)
				if code == 0 || code == exitUsage {
					t.Fatalf("%v with pin %s against %s exited %d\n%s", args, c.pin, c.name, code, errOut)
				}
				if containsRequest(e.requests(), "POST /operations") || containsRequest(e.requests(), "GET /operations") {
					t.Errorf("a refused pin still reached the operations: %v", e.requests())
				}
			})
		}
	}
}

// A stopped local deployment with a URL exported is the shape #535 and
// #555 closed: the URL may name somebody else's engine, so the verb refuses
// rather than going remote.
func TestRemote1250_AStoppedLocalDeploymentStillRefuses(t *testing.T) {
	_, e := remoteFixture(t)
	local := writeTestConfig(t)
	pin(t, e.svc.DeploymentID())
	for _, args := range [][]string{{"operation", "list"}, {"operation", "run"}, {"assurance", "cancel", "op-1"}, {"backup-set", "edit-hold", "a/one"}} {
		code, _, errOut := runOp(t, local, args...)
		if code == 0 || code == exitUsage {
			t.Errorf("%v beside a stopped local deployment exited %d\n%s", args, code, errOut)
		}
		if strings.Contains(errOut, "remote") && strings.Contains(errOut, "mode: engine-attached") {
			t.Errorf("%v went remote beside a local deployment:\n%s", args, errOut)
		}
	}
	for _, r := range e.requests() {
		if !strings.HasPrefix(r, "GET /auth") && !strings.HasPrefix(r, "POST /auth") && r != "GET /system/version" {
			t.Errorf("an engine the local deployment never named was asked %s", r)
		}
	}
}

// Something serving this host's default state database is a local engine,
// and a terminal that cannot see a config for it must not route past it.
func TestRemote1250_ALocalEngineOnTheDefaultJournalRefuses(t *testing.T) {
	cliCfg, e := remoteFixture(t)
	t.Setenv(service.StateDatabaseEnv, journalNamedByTestConfig(t, e.configPath))
	e.holdOperations()
	code, _, errOut := runOp(t, cliCfg, "operation", "list")
	if code == 0 || code == exitUsage {
		t.Fatalf("exit %d beside a local engine with no config\n%s", code, errOut)
	}
}

// A local deployment identity that is not the engine's refuses too.
func TestRemote1250_ALocalIdentityThatDisagreesRefuses(t *testing.T) {
	cliCfg, e := remoteFixture(t)
	db := os.Getenv(service.StateDatabaseEnv)
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db+".deployment-id", []byte("ffffffffffffffffffffffffffffffff\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id, err := service.DeploymentIdentity(db); err != nil || id == "" {
		t.Fatalf("the fixture did not plant a local identity (%q, %v); fix the file name", id, err)
	}
	e.holdOperations()
	code, _, errOut := runOp(t, cliCfg, "operation", "list")
	if code == 0 || code == exitUsage {
		t.Fatalf("exit %d with a local identity the engine does not have\n%s", code, errOut)
	}
}

// Plain http to anything but loopback is refused unless the operator opts
// in, and only the opt-in sends the acknowledgement.
func TestRemote1250_PlainHTTPToANonLoopbackHostNeedsTheOptIn(t *testing.T) {
	old := isLoopbackHost
	isLoopbackHost = func(string) bool { return false }
	t.Cleanup(func() { isLoopbackHost = old })

	t.Run("without", func(t *testing.T) {
		cliCfg, e := remoteFixture(t)
		pin(t, e.svc.DeploymentID())
		code, _, errOut := runOp(t, cliCfg, "operation", "run")
		if code != insecureTransportExit {
			t.Fatalf("exit %d, want %d\n%s", code, insecureTransportExit, errOut)
		}
		if len(e.requests()) != 0 {
			t.Errorf("a refused plain-http route still sent %v", e.requests())
		}
		if !strings.Contains(errOut, "RETND_API_INSECURE_TRANSPORT") {
			t.Errorf("the refusal does not name the opt-in: %s", errOut)
		}
	})
	t.Run("with", func(t *testing.T) {
		cliCfg, e := remoteFixture(t)
		pin(t, e.svc.DeploymentID())
		t.Setenv("RETND_API_INSECURE_TRANSPORT", "acknowledged")
		var ack string
		e.handle("POST /operations", func(w http.ResponseWriter, r *http.Request) {
			ack = r.Header.Get("X-Retnd-Insecure-Transport-Ack")
			writeJSON(w, http.StatusAccepted, queuedRun("op-h", "run_cycle", ""))
		})
		code, _, errOut := runOp(t, cliCfg, "operation", "run")
		if code != 0 {
			t.Fatalf("exit %d with the opt-in\n%s", code, errOut)
		}
		if ack != "acknowledged" {
			t.Errorf("the opt-in did not send the acknowledgement (%q)", ack)
		}
	})
}

func TestRemote1250_LoopbackSendsNoAcknowledgement(t *testing.T) {
	cliCfg, e := remoteFixture(t)
	pin(t, e.svc.DeploymentID())
	var ack = "unset"
	e.handle("POST /operations", func(w http.ResponseWriter, r *http.Request) {
		ack = r.Header.Get("X-Retnd-Insecure-Transport-Ack")
		writeJSON(w, http.StatusAccepted, queuedRun("op-l", "run_cycle", ""))
	})
	if code, _, errOut := runOp(t, cliCfg, "operation", "run"); code != 0 {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	if ack != "" {
		t.Errorf("a loopback route with no opt-in sent the acknowledgement %q", ack)
	}
}

// A self-signed reverse proxy is trusted through a CA file, and only then.
func TestRemote1250_ACAFileTrustsASelfSignedProxy(t *testing.T) {
	cliCfg, e := remoteFixture(t)
	e.server.Close()
	e.server = httptest.NewTLSServer(e)
	t.Cleanup(e.server.Close)
	e.attach(t)
	e.holdOperations()

	code, _, errOut := runOp(t, cliCfg, "operation", "list")
	if code == 0 {
		t.Fatalf("an untrusted certificate was accepted:\n%s", errOut)
	}

	ca := filepath.Join(t.TempDir(), "proxy-ca.pem")
	block := &pem.Block{Type: "CERTIFICATE", Bytes: e.server.Certificate().Raw}
	if err := os.WriteFile(ca, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RETND_API_CA_FILE", ca)
	code, _, errOut = runOp(t, cliCfg, "operation", "list")
	if code != 0 {
		t.Fatalf("with the CA file the list exited %d\n%s", code, errOut)
	}
}

// The engine's own insecure-transport refusal is not a failed run.
func TestRemote1250_TheEnginesTransportRefusalHasItsOwnCode(t *testing.T) {
	cliCfg, e := remoteFixture(t)
	pin(t, e.svc.DeploymentID())
	e.handle("POST /operations", func(w http.ResponseWriter, _ *http.Request) {
		refuse(w, http.StatusForbidden, apicontract.ErrorCodeInsecureTransportAckRequired, "plain http needs the acknowledgement")
	})
	code, _, errOut := runOp(t, cliCfg, "operation", "run")
	if code != insecureTransportExit {
		t.Fatalf("exit %d, want %d\n%s", code, insecureTransportExit, errOut)
	}
}

// The refusals say what works where instead of telling somebody who has
// already set RETND_API_URL to set it.
func TestRemote1250_TheNoRouteRefusalSaysWhatWorksWhere(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	for _, args := range [][]string{{"operation", "list"}, {"assurance", "cancel", "op-1"}, {"backup-set", "edit-hold", "a/one"}} {
		_, _, errOut := runOp(t, cfg, args...)
		if !strings.Contains(errOut, "no deployment of its own") {
			t.Errorf("%v: the refusal does not say where a route alone is enough:\n%s", args, errOut)
		}
	}
}
