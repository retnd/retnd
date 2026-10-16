package webhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/core/service"
)

// The gate that opens for a profile with no identity header to spoof, and
// the conditions that go with it (issues #1163 and #1164).
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
// one localAccountProfile names AND the table's row for that ID has no
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
// asks more of a gate that opens this way, and every gated place asks it
// through one decision (gateEnforcement.decide), so the three of them cannot
// drift apart:
//
//   - a restore re-authenticates the administrator and carries an explicit
//     acknowledgement, because a stolen session should not be enough to write
//     a tree into a directory or start a billed thaw;
//   - a request whose browser reached retnd over plain HTTP from another
//     machine carries one more acknowledgement, because on that connection
//     the session is travelling in the clear. That one is a consent prompt,
//     not an access control: anybody holding the session can send the
//     header, and what it buys is that an operator is told before acting;
//   - nothing about a request can change the answer, because the gate is
//     static and the conditions read only the connection and the decoded
//     body the handler itself runs.

// localAccountProfile is the human half of the decision: the profiles whose
// identity is a session cookie this process issued. It is written out rather
// than derived so that opening the gate for a profile is a line in a diff, and
// it is a switch rather than a map so nothing at run time can add to it.
func localAccountProfile(id profile.ID) bool {
	switch id {
	case profile.Generic, profile.TrueNAS, profile.Unraid, profile.OpenMediaVault, profile.Proxmox, profile.Synology:
		return true
	}
	return false
}

// profileGate is the DestructiveGate for one compiled profile. It is decided
// once, when it is built, from a table that does not change while the
// process runs.
type profileGate struct {
	passed bool
	reason string
}

// NewProfileGate returns the gate for the profile with this ID. An ID that is
// not a profile gets a gate that never opens. Lookup is exact, so "GENERIC"
// and " generic" are not profiles.
func NewProfileGate(id profile.ID) DestructiveGate {
	p, err := profile.Lookup(string(id))
	known := err == nil && p.ID == id
	switch {
	case known && localAccountProfile(id) && p.Gateway == nil && !p.Capabilities.NativeAuth:
		return profileGate{passed: true}
	case known && (p.Gateway != nil || p.Capabilities.NativeAuth):
		return profileGate{reason: "gateway profile: its identity is a header this build cannot verify, so the gate stays shut until #92"}
	default:
		return profileGate{reason: "gate shut until #92: this profile is not known to be a local-account profile"}
	}
}

// Passed reports whether the profile is a local-account profile.
func (g profileGate) Passed() bool { return g.passed }

// Reason says why the gate is shut for this profile, and nothing when it is
// open.
func (g profileGate) Reason() string { return g.reason }

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
// The actions of POST /operations
// ---------------------------------------------------------------------

// OperationAction is the action a POST /operations body names. It is the one
// field on the route whose value decides whether the gate applies, so every
// place in this package that decides anything from it (the exempt list, the
// restore conditions, the audit entry, the handler's dispatch) reads the same
// typed value, decoded once.
type OperationAction string

// The actions, spelled once in core/apicontract and read through core/service.
const (
	ActionRunCycle            OperationAction = service.ActionRunCycle
	ActionRunBackupSet        OperationAction = service.ActionRunBackupSet
	ActionRestorePlacement    OperationAction = service.ActionRestorePlacement
	ActionRestoreSnapshot     OperationAction = service.ActionRestoreSnapshot
	ActionVerifySnapshot      OperationAction = service.ActionVerifySnapshot
	ActionHoldSnapshot        OperationAction = service.ActionHoldSnapshot
	ActionReleaseSnapshotHold OperationAction = service.ActionReleaseSnapshotHold
)

// destructiveGateExemptActions names the actions on POST /operations that do
// not pass through the destructive gate (#1164). The route is gated as a
// whole; this is the list of what it lets by.
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
var destructiveGateExemptActions = map[OperationAction]bool{
	ActionVerifySnapshot:      true,
	ActionHoldSnapshot:        true,
	ActionReleaseSnapshotHold: true,
}

func (a OperationAction) gateExempt() bool { return destructiveGateExemptActions[a] }

func (a OperationAction) isRestore() bool {
	return a == ActionRestorePlacement || a == ActionRestoreSnapshot
}

// parsedOperation is a POST /operations body, decoded once for the whole
// request. The gate decides on it and the handler dispatches on it, so there
// is no second reading of the body for an attacker to make disagree with the
// first (the review of #1167: two decoders were an argument that they agreed,
// one is a fact).
type parsedOperation struct {
	req submitOperationRequest
	// err is the decode's own error, reported by the handler in its words.
	err error
	// plain says the body is one JSON object with exactly one "action" key
	// (matched as the decoder matches it, ignoring case) whose value is a
	// string. Only a plain body can be exempt: no client writes two action
	// keys on purpose, and refusing them costs nothing.
	plain bool
}

type parsedOperationKey struct{}

// operationRequest is the decoded body of this POST /operations, decoding it
// the first time it is asked for and handing back a request that carries it.
func operationRequest(w http.ResponseWriter, r *http.Request) (*parsedOperation, *http.Request) {
	if p, ok := r.Context().Value(parsedOperationKey{}).(*parsedOperation); ok {
		return p, r
	}
	p := &parsedOperation{}
	src := r.Body
	if src == nil {
		src = http.NoBody
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, src, maxSubmitOperationBodyBytes))
	if err != nil {
		p.err = err
	} else {
		p.req, p.err = decodeSubmitOperation(body)
		p.plain = p.err == nil && plainActionKey(body)
	}
	return p, r.WithContext(context.WithValue(r.Context(), parsedOperationKey{}, p))
}

// decodeSubmitOperation is the one decoder of a POST /operations body. The
// audit entry reads a body with it too (actionlog.go), so the act an entry
// names is the act that ran.
func decodeSubmitOperation(body []byte) (submitOperationRequest, error) {
	var req submitOperationRequest
	err := json.NewDecoder(bytes.NewReader(body)).Decode(&req)
	return req, err
}

// action is what the body asked for, or nothing when it could not be read,
// which is gated and never a restore.
func (p *parsedOperation) action() OperationAction {
	if p.err != nil {
		return ""
	}
	return p.req.Action
}

func (p *parsedOperation) exempt() bool {
	return p.err == nil && p.plain && p.req.Action.gateExempt()
}

// plainActionKey reports whether body is a JSON object with exactly one key
// that the decoder would read as "action", and a string value for it.
func plainActionKey(body []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return false
	}
	seen := 0
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return false
		}
		key, ok := keyTok.(string)
		if !ok {
			return false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return false
		}
		if !strings.EqualFold(key, "action") {
			continue
		}
		seen++
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return false
		}
	}
	if _, err := dec.Token(); err != nil {
		return false
	}
	return seen == 1
}

// restoreAcknowledged reads the restore acknowledgement off the decoded
// request. For restore_placement it is the cost consent the field always
// carried (a thaw is billed and takes hours, and writes nowhere); for
// restore_snapshot it is the consent to write into target_path.
func restoreAcknowledged(req *submitOperationRequest) bool {
	switch req.Action {
	case ActionRestorePlacement:
		return req.Restore != nil && req.Restore.Acknowledged
	case ActionRestoreSnapshot:
		return req.SnapshotRestore != nil && req.SnapshotRestore.Acknowledged
	}
	return false
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
//
// An error that also has a RetryAfter() time.Duration method is a tripped
// limiter rather than a wrong password, and is answered 429 RATE_LIMITED with
// Retry-After.
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

	// BrowserTransportHeader is the verdict the hop that faces the browser
	// gives about the browser's own connection. serve-ui sets it on every
	// request it proxies, after deleting whatever the browser sent, because
	// the engine behind it only ever sees serve-ui: over loopback in the
	// Synology package and over a container network everywhere else, and
	// neither says anything about the browser.
	//
	// The engine believes it from any peer. That is safe for exactly one
	// reason, which is why nothing but the insecure-transport prompt reads
	// it: a caller who could forge it could equally send
	// InsecureTransportHeader, so a forged verdict buys nothing the prompt was
	// withholding. It never reaches identity, rate limiting or the gate.
	BrowserTransportHeader = "X-Retnd-Browser-Transport"
	// The three verdicts. Anything else is read as BrowserTransportHTTP.
	BrowserTransportHTTPS    = "https"
	BrowserTransportLoopback = "loopback"
	BrowserTransportHTTP     = "http"
)

// BrowserTransportOf is the verdict about r's connection as the hop that
// received it sees it: TLS, a browser on this machine, or neither. serve-ui
// stamps it into BrowserTransportHeader.
func BrowserTransportOf(r *http.Request) string {
	if r.TLS != nil {
		return BrowserTransportHTTPS
	}
	if addrIsLoopback(r.RemoteAddr) {
		return BrowserTransportLoopback
	}
	return BrowserTransportHTTP
}

func addrIsLoopback(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// gateEnforcement is what the gated routes and the capabilities read share.
type gateEnforcement struct {
	gate     DestructiveGate
	stepUp   StepUpVerifier
	trustFwd bool
}

// browserTransport is the verdict about the browser's connection. The
// browser-facing hop's verdict wins when there is one. Without it the engine
// is the browser-facing hop, and decides from its own connection: TLS, or the
// project's own edge proxy's X-Forwarded-Proto and first X-Forwarded-For entry
// when trustFwd lets it believe them, or its own peer.
func (e gateEnforcement) browserTransport(r *http.Request) string {
	if v := r.Header.Get(BrowserTransportHeader); v != "" {
		return v
	}
	if r.TLS != nil {
		return BrowserTransportHTTPS
	}
	addr := r.RemoteAddr
	if e.trustFwd {
		if strings.EqualFold(firstForwardedEntry(r.Header.Get("X-Forwarded-Proto")), "https") {
			return BrowserTransportHTTPS
		}
		// The FIRST entry is the one the first hop wrote; a loopback entry
		// further along proves nothing.
		if first := firstForwardedEntry(r.Header.Get("X-Forwarded-For")); first != "" {
			addr = first
		}
	}
	if addrIsLoopback(addr) {
		return BrowserTransportLoopback
	}
	return BrowserTransportHTTP
}

// insecureTransportAckNeeded reports whether a gated act on this request
// needs the insecure-transport acknowledgement (whether or not it carries
// it). It is what GET /system/capabilities reports, so a screen asks exactly
// when the router would refuse.
func (e gateEnforcement) insecureTransportAckNeeded(r *http.Request) bool {
	secureTransport, _ := conditionsOf(e.gate)
	if !secureTransport {
		return false
	}
	v := e.browserTransport(r)
	return v != BrowserTransportHTTPS && v != BrowserTransportLoopback
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

// insecureTransportAuditReason is what the audit entry records as the reason
// for a transport refusal. The gate was open; this is why the act was not.
const insecureTransportAuditReason = "the gate is open, and the browser's connection is not encrypted and not from this machine, and the request did not acknowledge that"

// ---------------------------------------------------------------------
// One decision for every gated act
// ---------------------------------------------------------------------

// gatedAct is what a request asks the gate for.
type gatedAct struct {
	// op is the /operations request, nil for the other gated places.
	op *submitOperationRequest
	// sourceRecovery is a recovery onto the backup set's own source host
	// (issue #1191), nil for everything else. Its target is read off the
	// recovery body the handler then runs (recoveryRequest).
	sourceRecovery *sourceRecoveryAct
	// validatorUpload is an upload or edit of a custom recovery-test
	// validator (#1203): new code the engine will run on the host.
	validatorUpload bool
}

// sourceRecoveryAct is what decide needs from a source-host recovery: the
// destination the request acknowledged, and (when the gate could work it
// out) the one the configuration resolves to now (#1193).
type sourceRecoveryAct struct {
	acknowledged string
	current      string
	url          string
}

// gateRefusal is how a gated act is refused.
type gateRefusal struct {
	status     int
	code       string
	message    string
	retryAfter time.Duration
	// The gate's state when it refused, and why, for the audit entry.
	gateState  string
	gateReason string
}

// decide is the one place that says whether a gated act may go ahead: the
// gate, then the transport rule, then for a restore its acknowledgement and
// the step-up. The two middlewares, createBackupSet's run_immediately and (for
// the transport half) GET /system/capabilities all read it, so they cannot
// disagree about what is enforced.
//
// The password is checked last because checking it spends a rate-limited
// hash comparison and can say "wrong", and neither should happen for a
// request that was going to be refused for another reason anyway.
func (e gateEnforcement) decide(r *http.Request, act gatedAct) (gateRefusal, bool) {
	if !destructiveGatePassed(e.gate) {
		state, reason := gateStatus(e.gate)
		return newGateRefusal(http.StatusForbidden, "DESTRUCTIVE_OPERATIONS_DISABLED", destructiveGateRefusal, state, reason), true
	}
	if e.insecureTransportAckNeeded(r) && !insecureTransportAcknowledged(r) {
		return newGateRefusal(http.StatusForbidden, "INSECURE_TRANSPORT_ACK_REQUIRED", insecureTransportRefusal,
			"open", insecureTransportAuditReason), true
	}
	_, stepUp := conditionsOf(e.gate)
	if stepUp && act.op != nil && act.op.Action.isRestore() {
		return e.checkRestore(r, act.op)
	}
	if stepUp && act.sourceRecovery != nil {
		return e.checkSourceRecovery(r, act.sourceRecovery)
	}
	if stepUp && act.validatorUpload {
		return e.checkStepUp(r, stepUpAct{
			name: "a validator upload", plural: "validator uploads", onHost: "retnd assurance validator add",
		})
	}
	return gateRefusal{}, false
}

// newGateRefusal is every refusal decide makes. One constructor, with the
// code in the position writeError takes it, so the contract test that reads
// the source for emitted codes (contract_test.go) sees these too.
func newGateRefusal(status int, code, message, gateState, gateReason string) gateRefusal {
	return gateRefusal{status: status, code: code, message: message, gateState: gateState, gateReason: gateReason}
}

// checkRestore is the acknowledgement and the step-up a restore needs where
// the gate opens for local accounts. Every refusal here happens with the gate
// open.
func (e gateEnforcement) checkRestore(r *http.Request, req *submitOperationRequest) (gateRefusal, bool) {
	if !restoreAcknowledged(req) {
		// Named for what was not acknowledged and in the conflict class, like
		// every other consent refusal in the contract. The two restores are
		// different acts with different consequences, and each says its own.
		if req.Action == ActionRestorePlacement {
			return newGateRefusal(http.StatusConflict, "RECOVERY_COST_NOT_ACKNOWLEDGED",
				"asking the provider to make this copy readable again has a retrieval cost and takes hours, so the request has to say you accept that: set acknowledged to true in restore",
				"open", "the restore did not acknowledge its retrieval cost"), true
		}
		target := ""
		if req.SnapshotRestore != nil {
			target = req.SnapshotRestore.TargetPath
		}
		return newGateRefusal(http.StatusConflict, "RESTORE_TARGET_NOT_ACKNOWLEDGED",
			"this restore writes files into "+strconv.Quote(target)+", so the request has to say you understand that: set acknowledged to true in snapshot_restore",
			"open", "the restore did not acknowledge its target directory"), true
	}
	return e.checkStepUp(r, stepUpAct{
		name: "a restore", plural: "restores", onHost: "retnd snapshot restore",
	})
}

// checkSourceRecovery is the acknowledgement and the step-up a recovery onto
// the set's own source host needs where the gate opens for local accounts
// (issue #1191). It writes onto the production machine the backup came from,
// so it asks what a restore asks, in the same order.
func (e gateEnforcement) checkSourceRecovery(r *http.Request, act *sourceRecoveryAct) (gateRefusal, bool) {
	if act.acknowledged == "" {
		return newGateRefusal(http.StatusConflict, "RECOVERY_SOURCE_HOST_NOT_ACKNOWLEDGED",
			"recovering to the source host writes onto the machine this backup came from, so the request has to acknowledge that destination: send the destination_id GET .../source-host returned as acknowledged_destination in its target",
			"open", "the recovery did not acknowledge writing onto its source host"), true
	}
	if act.current != "" && act.acknowledged != act.current {
		return newGateRefusal(http.StatusConflict, "RECOVERY_SOURCE_HOST_NOT_ACKNOWLEDGED",
			"the destination changed since it was acknowledged: it is now "+act.url+", so nothing was written and no password was checked; look at it again and acknowledge what it is now",
			"open", "the recovery acknowledged a source-host destination the configuration no longer resolves to"), true
	}
	return e.checkStepUp(r, stepUpAct{
		name: "a recovery onto the source host", plural: "recoveries onto the source host",
		onHost: "retnd recover --acknowledge-source-write",
	})
}

// stepUpAct names the act a step-up refusal is about, in the words the
// refusal and its audit entry use.
type stepUpAct struct {
	name   string // "a restore"
	plural string // "restores"
	onHost string // the command that does the same on the host
}

// checkStepUp is the password re-check every step-up act shares: wired,
// carried, not rate limited, and right.
func (e gateEnforcement) checkStepUp(r *http.Request, act stepUpAct) (gateRefusal, bool) {
	if e.stepUp == nil {
		// A capability this deployment lacks, like RECOVERY_UNAVAILABLE, and
		// not a refusal of this caller.
		return newGateRefusal(http.StatusServiceUnavailable, "STEP_UP_UNAVAILABLE",
			"this deployment cannot re-check your password, so it will not run "+act.name+" over HTTP; run `"+act.onHost+"` on the host",
			"open", "no password check is wired, so "+act.plural+" fail closed"), true
	}
	password := r.Header.Get(StepUpHeader)
	if password == "" {
		return newGateRefusal(http.StatusForbidden, "STEP_UP_REQUIRED",
			act.name+" asks for your password again: send it in the "+StepUpHeader+" header",
			"open", "the "+strings.TrimPrefix(act.name, "a ")+" did not carry the administrator's password"), true
	}
	if err := e.stepUp.VerifyStepUp(r.Context(), actorFromContext(r.Context()), password); err != nil {
		var limited interface{ RetryAfter() time.Duration }
		if errors.As(err, &limited) {
			ref := newGateRefusal(http.StatusTooManyRequests, "RATE_LIMITED",
				"too many password checks in the last minute; wait before trying again, because another attempt now is refused without being checked",
				"open", "the password check is rate limited")
			ref.retryAfter = limited.RetryAfter()
			return ref, true
		}
		return newGateRefusal(http.StatusForbidden, "STEP_UP_FAILED", "that password was not accepted",
			"open", "the administrator's password was not accepted"), true
	}
	return gateRefusal{}, false
}

// write sends the refusal and leaves the note the audit entry is made from.
// operation names the act when only the caller knows it (run_immediately);
// "" lets the route and body say.
func (ref gateRefusal) write(w http.ResponseWriter, r *http.Request, operation string) {
	ref.note(r.Context(), operation)
	if ref.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((ref.retryAfter+time.Second-1)/time.Second)))
	}
	writeError(w, ref.status, ref.code, ref.message)
}

// note records the refusal for recordActions (issue #1165) without writing
// anything, for the create whose run was refused and still answers 201.
func (ref gateRefusal) note(ctx context.Context, operation string) {
	noteRefusal(ctx, gateNote{
		refused: true, state: ref.gateState, reason: ref.gateReason, operation: operation,
		code: ref.code, message: ref.message,
	})
}

// requireOperationGate is requireDestructiveGate for POST /operations, where
// the gate applies per action rather than per route. The exempt actions go
// straight through (authentication and CSRF have already run); everything
// else goes through decide, against the same decoded body the handler then
// runs.
func requireOperationGate(e gateEnforcement) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, r := operationRequest(w, r)
			if p.exempt() {
				next.ServeHTTP(w, r)
				return
			}
			act := gatedAct{}
			if p.err == nil {
				act.op = &p.req
			}
			if ref, refused := e.decide(r, act); refused {
				ref.write(w, r, "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireRouteGate is the gate for the routes whose whole purpose is gated
// (retention apply): decide, with no per-action half.
func requireRouteGate(e gateEnforcement) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ref, refused := e.decide(r, gatedAct{}); refused {
				ref.write(w, r, "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
