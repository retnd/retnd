package webhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/core/agenttools"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/service"
)

// Confirmations over HTTP (EPIC U, U2.1, #1349; FR-95 and FR-94; ADR 0034
// decision 5).
//
// A tool call under operate or configure becomes a pending confirmation, and
// these three routes are how a person lists, approves and denies them. They
// are session-only: core/agenttools' deny list holds them, so a bearer is
// AGENT_ROUTE_DENIED before its scope is looked at, and an agent can never
// approve its own ask. The one route under the prefix a token reads is GET
// /agent/confirmations/{id}, the confirmation_status tool, which answers a
// token only for its own.
//
// # Approval replays the record
//
// Approve asks for the administrator's password (the step-up, #1163), claims
// the confirmation, and then runs the recorded request through the same
// router the browser would have hit, as the token that asked. So the shut
// operation gate, the deny list, CSRF for a bearer, validation and
// every other refusal apply to it exactly as they would to the original, and
// the action log gets an api_action from agent:<name> beside the approval's
// own from the person. Nothing the approve request carries is replayed: a body
// on it is refused, and the replay is built from the record alone.

// AgentConfirmations is what the three routes need from core/service.
// *service.BackupService satisfies it; NewRouter finds it by asserting it on
// the backend, the way it finds AgentAccess.
type AgentConfirmations interface {
	CreateAgentConfirmation(ctx context.Context, req service.CreateAgentConfirmationRequest) (service.AgentConfirmation, error)
	ListAgentConfirmations(ctx context.Context) ([]service.AgentConfirmation, error)
	AgentConfirmationStatus(ctx context.Context, tokenID, id string) (service.AgentConfirmation, error)
	ApproveAgentConfirmation(ctx context.Context, id, approver string) (service.AgentConfirmation, service.AgentPrincipal, error)
	FinishAgentApproval(ctx context.Context, c service.AgentConfirmation, exec service.AgentExecution) error
	DenyAgentConfirmation(ctx context.Context, id, by string) (service.AgentConfirmation, error)
}

// AgentToolResolver is how the router learns what an agent's tool call stands
// for. The catalogue (core/agenttools) is the table that answers it; the
// router takes it as an interface so a test can name a table of its own.
type AgentToolResolver interface {
	// ResolveAgentTool reports the scope a tool needs, when the request it
	// would make (method, the path below /api/v1, and the body's action word
	// for the route several acts share) is the one that tool is a name for.
	ResolveAgentTool(tool, method, path, action string) (scope string, ok bool)
}

// catalogueTools answers ResolveAgentTool from core/agenttools' own table, the
// one the wire's deny list and the MCP server read too.
type catalogueTools struct{}

func (catalogueTools) ResolveAgentTool(tool, method, path, action string) (string, bool) {
	scope, ok := agenttools.Proposes(tool, method, path, action)
	return string(scope), ok
}

// Headers a refused-pending answer carries, so a tool result can name the
// confirmation without parsing the message.
const (
	ConfirmationIDHeader      = "X-Retnd-Confirmation-Id"
	ConfirmationExpiresHeader = "X-Retnd-Confirmation-Expires"
)

// agentProposer turns the mutating request of an agent's tool call into a
// pending confirmation. A raw bearer request, one that names no tool, never
// reaches it: that request is refused AGENT_CONFIRMATION_REQUIRED and nothing
// is recorded (ADR 0034 decision 4).
//
// The request is what is recorded: its method, its path as the client sent
// it, its query and its body, byte for byte. The route pattern the router
// would match it to is looked up only to build the echoed command and to name
// the backup set, so what is replayed is never rebuilt from either.
type agentProposer struct {
	confirmations AgentConfirmations
	resolver      AgentToolResolver
	root          *chi.Mux
}

// propose records the request as a confirmation and writes the answer. It
// reports false, having written nothing, when the request is not a tool call
// it can propose (no tool, a tool the catalogue does not stand behind for this
// request, or a route the router does not have), so the caller refuses it as it
// would any raw request.
func (p *agentProposer) propose(w http.ResponseWriter, r *http.Request, principal service.AgentPrincipal) (status int, code, correlationID string, handled bool) {
	tool, _ := service.AgentCallHeaders(r.Header)
	if tool == "" || p == nil || p.resolver == nil {
		return 0, "", "", false
	}
	// One form of the path, or none: a request whose decoded and raw paths
	// disagree is not one a person is asked to approve.
	rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/api/v1")
	if r.URL.RawPath != "" && strings.TrimPrefix(path.Clean("/"+r.URL.RawPath), "/api/v1") != rel {
		return 0, "", "", false
	}

	var body []byte
	if r.Body != nil && r.Body != http.NoBody {
		got, err := io.ReadAll(io.LimitReader(r.Body, maxAgentProposalBody+1))
		if err != nil || len(got) > maxAgentProposalBody {
			return refuse(w, http.StatusBadRequest, "INVALID_REQUEST", "the request is too large to put in front of a person")
		}
		body = got
	}
	var act struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(body, &act)
	scope, ok := p.resolver.ResolveAgentTool(tool, r.Method, rel, act.Action)
	if !ok {
		return 0, "", "", false
	}

	rctx := chi.NewRouteContext()
	if !p.root.Match(rctx, r.Method, "/api/v1"+rel) {
		return 0, "", "", false
	}
	params := make(map[string]string, len(rctx.URLParams.Keys))
	for i, k := range rctx.URLParams.Keys {
		params[k] = rctx.URLParams.Values[i]
	}
	line := cliecho.Echo(cliecho.Action{
		Method: r.Method, Route: strings.TrimPrefix(rctx.RoutePattern(), "/api/v1"), Params: params, Query: r.URL.Query(), Body: body,
	})
	command := line.Shell()
	if command == "" {
		command = "(no retnd command does this: " + line.Gap + ")"
	}
	var set string
	if params["source"] != "" && params["set"] != "" {
		set = params["source"] + "/" + params["set"]
	}
	route := r.URL.EscapedPath()
	route = strings.TrimPrefix(route, "/api/v1")
	if r.URL.RawQuery != "" {
		route += "?" + r.URL.RawQuery
	}

	c, err := p.confirmations.CreateAgentConfirmation(r.Context(), service.CreateAgentConfirmationRequest{
		Principal: principal, Scope: scope, Tool: tool, Method: r.Method, Route: route, Body: body, Command: command, BackupSet: set,
	})
	switch {
	case errors.Is(err, service.ErrAgentConfirmationLimit):
		return refuse(w, http.StatusTooManyRequests, "AGENT_CONFIRMATION_LIMIT", err.Error())
	case errors.Is(err, service.ErrAgentScopeInsufficient):
		return refuse(w, http.StatusForbidden, "AGENT_SCOPE_INSUFFICIENT", err.Error())
	case errors.Is(err, service.ErrAgentTokenInvalid):
		return refuse(w, http.StatusUnauthorized, "AGENT_TOKEN_INVALID", "the agent token is not valid")
	case errors.Is(err, service.ErrInvalidRequest):
		return refuse(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case err != nil:
		slog.ErrorContext(r.Context(), "an agent's change could not be recorded", "tool", tool, "error", err)
		return refuse(w, http.StatusInternalServerError, "INTERNAL", "the change could not be recorded, so nothing was asked of a person")
	}
	w.Header().Set(ConfirmationIDHeader, c.ID)
	w.Header().Set(ConfirmationExpiresHeader, c.ExpiresAt.UTC().Format(time.RFC3339))
	return refuse(w, http.StatusConflict, "AGENT_CONFIRMATION_REQUIRED",
		"a person has to confirm this before it runs: confirmation "+c.ID+" waits until "+c.ExpiresAt.UTC().Format(time.RFC3339)+" for `"+command+"` to be approved")
}

// refuse writes one error envelope and reports it as handled.
func refuse(w http.ResponseWriter, status int, code, message string) (int, string, string, bool) {
	return status, code, writeError(w, status, code, message), true
}

// maxAgentProposalBody is the most of a request a confirmation records.
const maxAgentProposalBody = 64 << 10

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
// are not the replay's. The one thing it takes from approver is the
// connection the transport rule reads: the peer address, the TLS state, the
// forwarded headers and the insecure-transport acknowledgement. The rule is
// about the person who approved, so it judges them the way it would judge
// their own call, and it believes the forwarded headers exactly when it
// would on the approve request. The correlation id is the approval's, so one
// id finds both the approval and what it ran.
func replayAgentRequest(approver *http.Request, handler http.Handler, c service.AgentConfirmation, p service.AgentPrincipal) service.AgentExecution {
	ctx := approver.Context()
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
	// POST /operations refuses a request without a key, and the recorded one
	// has none to give. The confirmation's id is unique across the deployment
	// and is claimed once, so it names this replay and no other.
	req.Header.Set("Idempotency-Key", "agent-confirmation:"+c.ID)
	req.RemoteAddr, req.TLS = approver.RemoteAddr, approver.TLS
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Proto", BrowserTransportHeader, InsecureTransportHeader} {
		if v := approver.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
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

// getAgentConfirmation is GET /api/v1/agent/confirmations/{id}: the state of
// one confirmation and, once an approved one has run, how it ended. It is the
// one confirmation route a bearer reaches, because it is how an agent reads
// what became of its own ask (the confirmation_status tool), and it answers a
// token only for the confirmations that token made. A person reads any.
func (h *handlers) getAgentConfirmation(w http.ResponseWriter, r *http.Request) {
	cs, ok := h.agentConfirmationsService(w)
	if !ok {
		return
	}
	tokenID := ""
	if p, isAgent := agentPrincipalFrom(r.Context()); isAgent {
		tokenID = p.TokenID
	}
	c, err := cs.AgentConfirmationStatus(r.Context(), tokenID, chi.URLParam(r, "id"))
	if err != nil {
		h.writeConfirmationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c.Contract())
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
	exec := replayAgentRequest(r, h.router, c, principal)
	// The replay has happened whatever this write does, so a failure to record
	// it is logged and the person still gets the answer.
	if err := cs.FinishAgentApproval(r.Context(), c, exec); err != nil {
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
