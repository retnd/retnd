package webhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// Confirmations over HTTP (EPIC U, U2.1, #1349; FR-95 and FR-94; ADR 0034
// decision 5).
//
// A tool call under operate or configure becomes a pending confirmation, and
// these three routes are how a person lists, approves and denies them. They
// are session-only: the whole /agent/confirmations prefix is in
// agentDeniedPrefixes, so a bearer is AGENT_ROUTE_DENIED before its scope is
// looked at, and an agent can never approve its own ask.
//
// # Approval replays the record
//
// Approve asks for the administrator's password (the step-up, #1163), claims
// the confirmation, and then runs the recorded request through the same
// router the browser would have hit, as the token that asked. So the shut
// operation gate, the denied prefixes, CSRF for a bearer, validation and
// every other refusal apply to it exactly as they would to the original, and
// the action log gets an api_action from agent:<name> beside the approval's
// own from the person. Nothing the approve request carries is replayed: a body
// on it is refused, and the replay is built from the record alone.

// AgentConfirmations is what the three routes need from core/service.
// *service.BackupService satisfies it; NewRouter finds it by asserting it on
// the backend, the way it finds AgentAccess.
type AgentConfirmations interface {
	ListAgentConfirmations(ctx context.Context) ([]service.AgentConfirmation, error)
	ApproveAgentConfirmation(ctx context.Context, id, approver string) (service.AgentConfirmation, service.AgentPrincipal, error)
	FinishAgentApproval(ctx context.Context, c service.AgentConfirmation, approver string, exec service.AgentExecution) error
	DenyAgentConfirmation(ctx context.Context, id, by string) (service.AgentConfirmation, error)
}

// replayContextKey carries the principal a confirmation is being replayed as.
// It is unexported so nothing outside this package can plant one: a request
// that arrives over the wire has no way to put a value on its context.
type replayContextKey struct{}

func agentReplayFrom(ctx context.Context) (service.AgentPrincipal, bool) {
	p, ok := ctx.Value(replayContextKey{}).(service.AgentPrincipal)
	return p, ok
}

// replayAgent is authMiddleware's branch for a replayed request. It does what
// authenticateBearer does after a token has been accepted, minus the token
// (the confirmation was claimed for it a moment ago, and ApproveAgentConfirmation
// checked it was still live), and it keeps the denied prefixes: a recorded
// request for a route a bearer may never reach is refused here, whatever a
// tool call managed to record.
func replayAgent(w http.ResponseWriter, r *http.Request, p service.AgentPrincipal) (*http.Request, bool) {
	if agentRouteDenied(r) {
		writeError(w, http.StatusForbidden, "AGENT_ROUTE_DENIED", "an agent token cannot reach this route")
		return nil, false
	}
	actor := "agent:" + p.Name
	ctx := context.WithValue(r.Context(), actorContextKey{}, actor)
	ctx = context.WithValue(ctx, authContextKey{}, authContextForAgent(actor))
	return r.WithContext(withAgentPrincipal(ctx, p)), true
}

// replayWriter is the response a replay writes into. It keeps what an
// execution result needs (status, headers, the start of the body) and nothing
// else, because the approver is not the one who asked for the response.
type replayWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *replayWriter) Header() http.Header { return w.header }

func (w *replayWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *replayWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	if room := 4096 - w.body.Len(); room > 0 {
		w.body.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// replayAgentRequest runs a confirmation's recorded request through handler
// as the principal that asked, and reports how it ended. The request is built
// from the record alone, on a context of its own: the approver's cookies,
// headers (the step-up password among them), route match and cancellation
// are not the replay's. The correlation id is the approval's, so one id finds
// both the approval and what it ran.
func replayAgentRequest(ctx context.Context, handler http.Handler, c service.AgentConfirmation, p service.AgentPrincipal) service.AgentExecution {
	inner, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	if scope := ctx.Value(requestScopeKey{}); scope != nil {
		inner = context.WithValue(inner, requestScopeKey{}, scope)
	}
	inner = context.WithValue(inner, replayContextKey{}, p)

	target := "/api/v1" + c.Route
	req, err := http.NewRequestWithContext(inner, c.Method, target, bytes.NewReader(c.Body))
	if err != nil {
		return service.AgentExecution{Status: http.StatusBadRequest, Code: "INVALID_REQUEST", Message: "the recorded request cannot be built"}
	}
	if len(c.Body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	rw := &replayWriter{header: http.Header{}}
	correlation := CorrelationIDFrom(ctx)
	if correlation != "" {
		rw.header.Set(CorrelationHeader, correlation)
	}
	handler.ServeHTTP(rw, req)

	exec := service.AgentExecution{Status: rw.status, CorrelationID: rw.header.Get(CorrelationHeader)}
	if exec.Status == 0 {
		exec.Status = http.StatusOK
	}
	if exec.Status >= http.StatusBadRequest {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(rw.body.Bytes(), &envelope) == nil && envelope.Error.Code != "" {
			exec.Code, exec.Message = envelope.Error.Code, envelope.Error.Message
		} else {
			exec.Code, exec.Message = "HTTP_"+strconv.Itoa(exec.Status), http.StatusText(exec.Status)
		}
	} else {
		exec.Message = http.StatusText(exec.Status)
	}
	exec.ExitCode = service.AgentExecutionExitCode(exec.Status, exec.Code)
	return exec
}

func (h *handlers) agentConfirmationsService(w http.ResponseWriter) (AgentConfirmations, bool) {
	if h.confirmations == nil {
		writeError(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", "this deployment cannot keep agent confirmations")
		return nil, false
	}
	return h.confirmations, true
}

// writeConfirmationError maps the service's refusals to the codes the
// contract declares, and everything else to a 500 that says nothing more.
func (h *handlers) writeConfirmationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrAgentConfirmationNotFound):
		writeError(w, http.StatusNotFound, "AGENT_CONFIRMATION_NOT_FOUND", err.Error())
	case errors.Is(err, service.ErrAgentConfirmationExpired):
		writeError(w, http.StatusConflict, "AGENT_CONFIRMATION_EXPIRED", err.Error())
	case errors.Is(err, service.ErrAgentConfirmationNotPending):
		writeError(w, http.StatusConflict, "AGENT_CONFIRMATION_DECIDED", err.Error())
	case errors.Is(err, service.ErrAgentConfirmationBodySubstituted):
		writeError(w, http.StatusConflict, "AGENT_CONFIRMATION_BODY_SUBSTITUTED", err.Error())
	case errors.Is(err, service.ErrAgentConfirmationTokenEnded):
		writeError(w, http.StatusConflict, "AGENT_CONFIRMATION_TOKEN_ENDED", err.Error())
	default:
		h.internalError(w, r, "INTERNAL", "failed to decide an agent confirmation", err)
	}
}

// listAgentConfirmations is GET /api/v1/agent/confirmations.
func (h *handlers) listAgentConfirmations(w http.ResponseWriter, r *http.Request) {
	cs, ok := h.agentConfirmationsService(w)
	if !ok {
		return
	}
	got, err := cs.ListAgentConfirmations(r.Context())
	if err != nil {
		h.internalError(w, r, "INTERNAL", "failed to list agent confirmations", err)
		return
	}
	out := apicontract.ListAgentConfirmationsResponse{Confirmations: make([]apicontract.AgentConfirmation, 0, len(got))}
	for _, c := range got {
		out.Confirmations = append(out.Confirmations, c.Contract())
	}
	writeJSON(w, http.StatusOK, out)
}

// approveAgentConfirmation is POST /api/v1/agent/confirmations/{id}/approve.
func (h *handlers) approveAgentConfirmation(w http.ResponseWriter, r *http.Request) {
	cs, ok := h.agentConfirmationsService(w)
	if !ok {
		return
	}
	// First, and before the password is looked at: what runs is what was
	// recorded, so there is nothing for a body to be.
	if r.Body != nil {
		if first, _ := io.ReadAll(io.LimitReader(r.Body, 1)); len(first) > 0 {
			writeError(w, http.StatusBadRequest, "AGENT_CONFIRMATION_BODY_SUBSTITUTED",
				"approving replays the request that was recorded; a body sent with the approval is never used, so nothing was run")
			return
		}
	}
	if ref, refused := h.enforce.checkStepUp(r, stepUpAct{
		name: "approving an agent's change", plural: "approvals", onHost: "retnd agent confirmations approve",
	}); refused {
		ref.write(w, r, "")
		return
	}

	approver := actorFromContext(r.Context())
	c, principal, err := cs.ApproveAgentConfirmation(r.Context(), chi.URLParam(r, "id"), approver)
	if err != nil {
		h.writeConfirmationError(w, r, err)
		return
	}
	exec := replayAgentRequest(r.Context(), h.router, c, principal)
	// The replay has happened whatever this write does, so a failure to record
	// it is logged and the person still gets the answer.
	if err := cs.FinishAgentApproval(r.Context(), c, approver, exec); err != nil {
		slog.ErrorContext(r.Context(), "an approved confirmation's execution was not recorded", "confirmation", c.ID, "error", err)
	}
	c.Execution = &exec
	writeJSON(w, http.StatusOK, c.Contract())
}

// denyAgentConfirmation is POST /api/v1/agent/confirmations/{id}/deny.
func (h *handlers) denyAgentConfirmation(w http.ResponseWriter, r *http.Request) {
	cs, ok := h.agentConfirmationsService(w)
	if !ok {
		return
	}
	c, err := cs.DenyAgentConfirmation(r.Context(), chi.URLParam(r, "id"), actorFromContext(r.Context()))
	if err != nil {
		h.writeConfirmationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c.Contract())
}
