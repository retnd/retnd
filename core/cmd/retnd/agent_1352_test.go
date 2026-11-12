package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/apiclient"
)

// EPIC U's exit-code decisions (#1352), written down as rows so a later
// change to any of them is a decision and not drift.

// The three agent refusals a person's CLI session never meets (only a bearer
// does) answer 1, which is what the CLI uses for any refusal without a code of
// its own. Falsify: give one of them an exit code in engineRefusalExit.
func TestAgentRefusalsAnswerOneUntilSomeoneDecidesOtherwise_1352(t *testing.T) {
	for _, code := range []apicontract.ErrorCode{
		apicontract.ErrorCodeAgentScopeInsufficient,
		apicontract.ErrorCodeAgentRouteDenied,
		apicontract.ErrorCodeAgentConfirmationRequired,
	} {
		t.Run(string(code), func(t *testing.T) {
			got, hint := engineRefusalExit(&apiclient.Error{Code: code})
			if got != exitFailure || hint != "" {
				t.Errorf("%s = exit %d (%q), want exit %d and no hint", code, got, hint, exitFailure)
			}
		})
	}
	// The partner: the codes that do own an exit must not collapse into 1.
	if got, _ := engineRefusalExit(&apiclient.Error{Code: apicontract.ErrorCodeInsecureTransportAckRequired}); got != exitInsecureTransport {
		t.Errorf("the transport refusal = exit %d, want %d", got, exitInsecureTransport)
	}
	if got, _ := engineRefusalExit(errors.New("plain")); got != exitFailure {
		t.Errorf("an error with no code = exit %d, want %d", got, exitFailure)
	}
}

// A remote mint over plain http to a non-loopback host is refused with exit 52
// until the acknowledgement is set, and the engine's own refusal of the same
// thing lands on the same code. Falsify: let the mint skip the transport check.
func TestARemoteMintOverPlainHTTPNeedsTheAcknowledgement_1352(t *testing.T) {
	old := isLoopbackHost
	isLoopbackHost = func(string) bool { return false }
	t.Cleanup(func() { isLoopbackHost = old })

	t.Run("without the acknowledgement", func(t *testing.T) {
		cliCfg, e := remoteFixture(t)
		pin(t, e.svc.DeploymentID())
		code, stdout, errOut := runOp(t, cliCfg, "agent", "token", "create", "triage")
		if code != insecureTransportExit {
			t.Fatalf("exit %d, want %d\n%s\n%s", code, insecureTransportExit, stdout, errOut)
		}
		if strings.Contains(stdout, "rat_") {
			t.Errorf("a refused mint still printed a token: %s", stdout)
		}
		if !strings.Contains(errOut, "RETND_API_INSECURE_TRANSPORT") {
			t.Errorf("the refusal does not name the opt-in: %s", errOut)
		}
	})
	t.Run("the engine refuses it too", func(t *testing.T) {
		cliCfg, e := remoteFixture(t)
		pin(t, e.svc.DeploymentID())
		t.Setenv("RETND_API_INSECURE_TRANSPORT", "acknowledged")
		e.handle("POST /agent/tokens", func(w http.ResponseWriter, _ *http.Request) {
			refuse(w, http.StatusForbidden, apicontract.ErrorCodeInsecureTransportAckRequired, "plain http needs the acknowledgement")
		})
		if code, _, errOut := runOp(t, cliCfg, "agent", "token", "create", "triage"); code != insecureTransportExit {
			t.Fatalf("exit %d, want %d\n%s", code, insecureTransportExit, errOut)
		}
	})
	t.Run("with the acknowledgement", func(t *testing.T) {
		cliCfg, e := remoteFixture(t)
		pin(t, e.svc.DeploymentID())
		t.Setenv("RETND_API_INSECURE_TRANSPORT", "acknowledged")
		code, stdout, errOut := runOp(t, cliCfg, "agent", "token", "create", "triage")
		if code != exitOK || !strings.HasPrefix(stdout, "rat_") {
			t.Fatalf("exit %d with the acknowledgement\n%s\n%s", code, stdout, errOut)
		}
	})
}
