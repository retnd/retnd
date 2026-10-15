package webhost

import (
	"context"

	"github.com/retnd/retnd/apps/common/platform/profile"
)

// Stubs for the local-account gate (issues #1163 and #1164). Everything here
// fails closed until the implementation lands.

// StepUpVerifier re-checks the signed-in administrator's password.
type StepUpVerifier interface {
	VerifyStepUp(ctx context.Context, username, password string) error
}

// NewProfileGate is the gate for one compiled profile.
func NewProfileGate(id profile.ID) DestructiveGate { return NotYetImplementedGate{} }

// destructiveGateExemptActions names the actions on POST /operations that do
// not pass through the destructive gate.
var destructiveGateExemptActions = map[string]bool{}

const (
	// StepUpHeader carries the password a restore re-authenticates with.
	StepUpHeader = "X-Retnd-Step-Up-Password"
	// InsecureTransportHeader carries the acknowledgement that the session
	// cookie cannot be Secure on this connection.
	InsecureTransportHeader = "X-Retnd-Insecure-Transport-Ack"
	// InsecureTransportAcknowledged is the one value that acknowledges it.
	InsecureTransportAcknowledged = "acknowledged"
)
