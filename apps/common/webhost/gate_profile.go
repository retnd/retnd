package webhost

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/retnd/retnd/apps/common/platform/profile"
)

// The gate that opens for a profile with no identity header to spoof, and
// the three conditions that go with it (issues #1163 and #1164).
//
// # What was decided
//
// gate.go says two things lift the shut gate: #92 verifying the trusted-proxy
// identity check on real hardware, or establishing that the running profile
// has no identity header to spoof in the first place. This is the second. A
// local-account profile authenticates with a session cookie this process
// issued and nothing in a request can mint, so there is no header for an
// attacker to forge into an identity, and the reason the gate was shut (a
// gateway header nobody had verified) does not apply to it. A gateway profile
// is exactly the case #92 is for, and it stays shut.
//
// # How it is kept hard to open by accident
//
// The gate is built from a profile ID and decides from the profile table
// itself, not from anything the caller passes. It opens only when the ID is
// one of localAccountProfiles below AND the table's row for that ID has no
// Gateway and does not claim native authentication. Both halves are needed:
// the list is the human decision, the table is the structural fact, and a
// profile that is on the list and later grows a Gateway shuts again without
// anybody editing this file. A profile that is on neither opens nothing, so
// a new profile starts shut and has to be added here on purpose, which
// shippedProfileGateStates in the tests makes unmissable.
//
// # What an open gate still asks for
//
// Opening is not the same as trusting every request that arrives. The router
// asks three more things of a gate that opens this way (conditions below):
//
//   - a restore re-authenticates the administrator and carries an explicit
//     acknowledgement, because a restore writes into a directory somebody
//     named and a stolen session should not be enough to do that;
//   - a request that arrives over a connection whose session cookie cannot
//     have been Secure, from a peer that is not loopback, carries one more
//     acknowledgement, because on that connection the session is travelling
//     in the clear;
//   - nothing about a request can change the answer, because the gate is
//     static and the conditions read only the connection and the body.

// localAccountProfiles is the human half of the decision: the profiles whose
// identity is a session cookie this process issued. It is written out rather
// than derived so that opening the gate for a profile is a line in a diff.
var localAccountProfiles = map[profile.ID]bool{
	profile.Generic:        true,
	profile.TrueNAS:        true,
	profile.Unraid:         true,
	profile.OpenMediaVault: true,
	profile.Proxmox:        true,
	profile.Synology:       true,
}

// profileGate is the DestructiveGate for one compiled profile.
type profileGate struct{ id profile.ID }

// NewProfileGate returns the gate for the profile with this ID. An ID that is
// not a profile gets a gate that never opens.
func NewProfileGate(id profile.ID) DestructiveGate { return profileGate{id: id} }

// row resolves the table's row for this gate's ID, or reports that there is
// none. Lookup is exact, so "GENERIC" and " generic" are not profiles.
func (g profileGate) row() (profile.Profile, bool) {
	p, err := profile.Lookup(string(g.id))
	if err != nil || p.ID != g.id {
		return profile.Profile{}, false
	}
	return p, true
}

// Passed reports whether the profile is a local-account profile.
func (g profileGate) Passed() bool {
	if !localAccountProfiles[g.id] {
		return false
	}
	p, ok := g.row()
	return ok && p.Gateway == nil && !p.Capabilities.NativeAuth
}

// Reason says why the gate is shut for this profile, and nothing when it is
// open.
func (g profileGate) Reason() string {
	if g.Passed() {
		return ""
	}
	if p, ok := g.row(); ok && (p.Gateway != nil || p.Capabilities.NativeAuth) {
		return "gateway profile: its identity is a header this build cannot verify, so the gate stays shut until #92"
	}
	return "gate shut until #92: this profile is not known to be a local-account profile"
}

// gateConditions is what a gate may add to being open. The methods are
// unexported on purpose: only a gate written in this package can ask for
// them, and a gate that does not implement the interface (the shipped shut
// gate, a test double, whatever #92 writes) is judged on Passed() alone.
type gateConditions interface {
	requiresSecureTransport() bool
	requiresStepUpForRestores() bool
}

func (profileGate) requiresSecureTransport() bool   { return true }
func (profileGate) requiresStepUpForRestores() bool { return true }

// conditionsOf reports the conditions an OPEN gate carries. A shut gate has
// none to report: it refuses before they are reached.
func conditionsOf(gate DestructiveGate) (secureTransport, stepUp bool) {
	if !destructiveGatePassed(gate) {
		return false, false
	}
	c, ok := gate.(gateConditions)
	if !ok {
		return false, false
	}
	return c.requiresSecureTransport(), c.requiresStepUpForRestores()
}

// ---------------------------------------------------------------------
// Verify, hold and release-hold: off the gate (#1164)
// ---------------------------------------------------------------------

// destructiveGateExemptActions names the actions on POST /operations that do
// not pass through the destructive gate. The route is gated as a whole; this
// is the list of what it lets by.
//
// Verify reads, and a hold only protects a snapshot from pruning, so neither
// destroys anything. Release-hold lifts that protection, which makes a
// snapshot prunable by the next retention run, but the scheduler runs
// retention unattended already, so it adds no destructive power the process
// did not have. It asks for an explicit acknowledgement instead
// (handlers_operations_snapshot.go), and the activity feed records who
// released which hold.
//
// restore_snapshot is not here and must not be: it writes a tree into a
// directory somebody named. Every other action is gated because it starts
// work, restores, or reaches a remote delete.
//
// An action that cannot be read plainly out of the body is not exempt. See
// exemptActionOf.
var destructiveGateExemptActions = map[string]bool{
	"verify_snapshot":       true,
	"hold_snapshot":         true,
	"release_snapshot_hold": true,
}

// exemptActionOf reads the action out of a POST /operations body and reports
// whether it is on the exempt list.
//
// The decision is made before the handler decodes the body, by a different
// decoder, so the two must agree about what the action is or an attacker
// picks the reading that suits them. The handler decodes into a struct, which
// matches keys case-insensitively and lets a later duplicate win. This
// decoder therefore matches case-insensitively too, and it treats any body
// with more than one action key, a non-string action, or anything that is not
// a plain JSON object as having no action at all, which is gated. The cost of
// that strictness is nil: no client writes a body like that on purpose.
func exemptActionOf(body []byte) (action string, exempt bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", false
	}
	seen := 0
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return "", false
		}
		key, ok := keyTok.(string)
		if !ok {
			return "", false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return "", false
		}
		if !strings.EqualFold(key, "action") {
			continue
		}
		seen++
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", false
		}
		action = s
	}
	if _, err := dec.Token(); err != nil || seen != 1 {
		return "", false
	}
	return action, destructiveGateExemptActions[action]
}

// ---------------------------------------------------------------------
// The step-up and the transport rule
// ---------------------------------------------------------------------

// StepUpVerifier re-checks the signed-in administrator's password.
//
// It exists so this package can ask "is this person who the session says
// they are" without learning how a credential is stored: the local-account
// service implements it, a gateway profile has nothing to implement, and a
// router built without one refuses every restore (STEP_UP_UNAVAILABLE)
// rather than letting it through.
type StepUpVerifier interface {
	VerifyStepUp(ctx context.Context, username, password string) error
}

const (
	// StepUpHeader carries the password a restore re-authenticates with. A
	// header and not a body field, so the request recorder, which echoes the
	// body into the activity feed's command line, can never see it.
	StepUpHeader = "X-Retnd-Step-Up-Password"
	// InsecureTransportHeader carries the acknowledgement that the session
	// cookie cannot be Secure on this connection.
	InsecureTransportHeader = "X-Retnd-Insecure-Transport-Ack"
	// InsecureTransportAcknowledged is the one value that acknowledges it.
	InsecureTransportAcknowledged = "acknowledged"
)

// gateEnforcement is what the gated routes and the capabilities read share.
type gateEnforcement struct {
	gate     DestructiveGate
	stepUp   StepUpVerifier
	trustFwd bool
}

// secureTransport reports whether the session cookie would have been issued
// Secure on this connection: the connection is TLS, or the project's own edge
// proxy (the only peer trustFwd ever lets us believe) says it is. Anything
// else, including a forged X-Forwarded-Proto or a Forwarded header on a
// deployment that does not trust forwarding, is plain HTTP.
func (e gateEnforcement) secureTransport(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !e.trustFwd {
		return false
	}
	proto := firstForwardedEntry(r.Header.Get("X-Forwarded-Proto"))
	return strings.EqualFold(proto, "https")
}

// clientIsLoopback reports whether the browser is on this machine. Without
// trusted forwarding the peer is the client. With it the peer is the edge
// proxy and the client is the FIRST X-Forwarded-For entry, which is the one
// the first hop wrote; a loopback entry further along proves nothing.
func (e gateEnforcement) clientIsLoopback(r *http.Request) bool {
	addr := r.RemoteAddr
	if e.trustFwd {
		if first := firstForwardedEntry(r.Header.Get("X-Forwarded-For")); first != "" {
			addr = first
		}
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// insecureTransportAckNeeded reports whether this request must carry the
// insecure-transport acknowledgement and does not.
func (e gateEnforcement) insecureTransportAckNeeded(r *http.Request) bool {
	secureTransport, _ := conditionsOf(e.gate)
	if !secureTransport {
		return false
	}
	return !e.secureTransport(r) && !e.clientIsLoopback(r)
}

func insecureTransportAcknowledged(r *http.Request) bool {
	return r.Header.Get(InsecureTransportHeader) == InsecureTransportAcknowledged
}

// firstForwardedEntry is the first comma-separated entry of a forwarding
// header, trimmed.
func firstForwardedEntry(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

const insecureTransportRefusal = "this connection is not encrypted and is not from this machine, so the session cookie is travelling in the clear; open retnd over HTTPS, or acknowledge it in the request with the " + InsecureTransportHeader + " header"

// refuseInsecureTransport writes the refusal for a request that needed the
// acknowledgement and lacked it.
func refuseInsecureTransport(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "INSECURE_TRANSPORT_ACK_REQUIRED", insecureTransportRefusal)
}

// restoreAcknowledged reads the restore acknowledgement out of a POST
// /operations body for the two restore actions.
func restoreAcknowledged(action string, body []byte) bool {
	var req struct {
		Restore *struct {
			Acknowledged bool `json:"acknowledged"`
		} `json:"restore"`
		SnapshotRestore *struct {
			Acknowledged bool `json:"acknowledged"`
		} `json:"snapshot_restore"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	switch action {
	case "restore_placement":
		return req.Restore != nil && req.Restore.Acknowledged
	case "restore_snapshot":
		return req.SnapshotRestore != nil && req.SnapshotRestore.Acknowledged
	}
	return false
}

// requireOperationGate is requireDestructiveGate for POST /operations, where
// the gate applies per action rather than per route.
//
// Order, and why: the exempt actions go straight through (authentication and
// CSRF have already run), then the gate, then the transport rule, then for a
// restore the acknowledgement and the step-up. The password is checked last
// because checking it spends a rate-limited hash comparison and can say
// "wrong", and neither should happen for a request that was going to be
// refused for another reason anyway.
func requireOperationGate(e gateEnforcement) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := drainBody(r)
			action, exempt := exemptActionOf(body)
			if exempt {
				next.ServeHTTP(w, r)
				return
			}
			if !destructiveGatePassed(e.gate) {
				noteGateRefusal(r.Context(), e.gate, "")
				writeDestructiveGateDenied(w)
				return
			}
			if e.insecureTransportAckNeeded(r) && !insecureTransportAcknowledged(r) {
				refuseInsecureTransport(w)
				return
			}
			if _, stepUp := conditionsOf(e.gate); stepUp && (action == "restore_placement" || action == "restore_snapshot") {
				if !e.checkRestore(w, r, action, body) {
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// checkRestore enforces the acknowledgement and the step-up on a restore. It
// writes the refusal and reports false when either is missing.
func (e gateEnforcement) checkRestore(w http.ResponseWriter, r *http.Request, action string, body []byte) bool {
	if !restoreAcknowledged(action, body) {
		writeError(w, http.StatusBadRequest, "ACKNOWLEDGEMENT_REQUIRED",
			"a restore writes into a directory you named, so the request has to say you understand that: set acknowledged to true in its parameters")
		return false
	}
	if e.stepUp == nil {
		writeError(w, http.StatusForbidden, "STEP_UP_UNAVAILABLE",
			"this deployment cannot re-check your password, so it will not run a restore over HTTP; run `retnd snapshot restore` on the host")
		return false
	}
	password := r.Header.Get(StepUpHeader)
	if password == "" {
		writeError(w, http.StatusForbidden, "STEP_UP_REQUIRED",
			"a restore asks for your password again: send it in the "+StepUpHeader+" header")
		return false
	}
	if err := e.stepUp.VerifyStepUp(r.Context(), actorFromContext(r.Context()), password); err != nil {
		writeError(w, http.StatusForbidden, "STEP_UP_FAILED", "that password was not accepted, or too many attempts were made in the last minute")
		return false
	}
	return true
}

// requireRouteGate is requireDestructiveGate with the transport rule added,
// for the routes whose whole purpose is gated (retention apply). The
// operations route has its own, per action: requireOperationGate.
func requireRouteGate(e gateEnforcement) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !destructiveGatePassed(e.gate) {
				noteGateRefusal(r.Context(), e.gate, "")
				writeDestructiveGateDenied(w)
				return
			}
			if e.insecureTransportAckNeeded(r) && !insecureTransportAcknowledged(r) {
				refuseInsecureTransport(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
