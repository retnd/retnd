package webhost

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// The agent audit trail on /api/v1 (EPIC U, U1.5, #1348; FR-97).
//
// Two halves. A bearer request that got past the authenticator is a call, and
// auditAgentCalls writes its row once the answer is known, reads included,
// because the action log skips every GET. And GET /api/v1/agent/audit pages
// the table back to a signed-in person. A token never reads it: the trail
// names every other token and what each did, which is an operator's record,
// so the route is on core/agenttools' deny list beside the token routes.

// AgentCallRecorder is what auditAgentCalls writes through.
// *service.BackupService satisfies it, found by assertion on the backend like
// AgentAccess is, so a backend without an audit table audits nothing.
type AgentCallRecorder interface {
	RecordAgentCall(ctx context.Context, call service.AgentCall) error
}

// AgentAuditReader is what GET /agent/audit reads through.
type AgentAuditReader interface {
	ListAgentAudit(ctx context.Context, q service.AgentAuditQuery) ([]service.AgentAuditEntry, string, error)
}

// agentSelfAuditedPrefix is the streamable HTTP transport (U1.4). It writes a
// row per tool call itself, naming the tool, so this middleware writing one
// per HTTP request as well would be two rows for one call.
const agentSelfAuditedPrefix = "/agent/mcp"

// auditAgentCalls records every request an authenticated agent made that the
// authenticator let through, after the handler has answered. It sits behind
// authMiddleware, so a request with no agent principal is none of its business
// and a refusal the authenticator made has already been recorded by it.
func auditAgentCalls(backend any) func(http.Handler) http.Handler {
	recorder, _ := backend.(AgentCallRecorder)
	return func(next http.Handler) http.Handler {
		if recorder == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := agentPrincipalFrom(r.Context())
			if !ok || strings.HasPrefix(strings.TrimPrefix(r.URL.Path, "/api/v1"), agentSelfAuditedPrefix) {
				next.ServeHTTP(w, r)
				return
			}
			capture := &capturingWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(capture, r)

			code, _ := capture.refusal()
			tool, transport := service.AgentCallHeaders(r.Header)
			call := service.AgentCall{
				TokenID: principal.TokenID, TokenName: principal.Name, Tool: tool, Transport: transport,
				Arguments: map[string]any{
					"method": r.Method,
					"path":   routePatternOf(r),
					"params": routeParamsOf(r),
					"query":  r.URL.Query(),
				},
				BackupSetID: backupSetOfRoute(routeParamsOf(r)),
				Mutating:    !safeMethod(r.Method),
				Refused:     capture.status == http.StatusUnauthorized || capture.status == http.StatusForbidden || capture.status == http.StatusTooManyRequests,
				Status:      capture.status, Code: code,
				CorrelationID: responseCorrelationID(capture),
			}
			if err := recorder.RecordAgentCall(r.Context(), call); err != nil {
				slog.WarnContext(r.Context(), "agent call was not recorded", "token", principal.TokenID, "error", err)
			}
		})
	}
}

// listAgentAudit is GET /api/v1/agent/audit.
func (h *handlers) listAgentAudit(w http.ResponseWriter, r *http.Request) {
	reader, ok := h.backend.(AgentAuditReader)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", "this deployment cannot keep an agent audit trail")
		return
	}
	q := r.URL.Query()
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	entries, next, err := reader.ListAgentAudit(r.Context(), service.AgentAuditQuery{
		TokenID: q.Get("token"), Since: q.Get("since"), Before: q.Get("before"), Limit: limit,
	})
	if errors.Is(err, service.ErrInvalidRequest) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if err != nil {
		h.internalError(w, r, "INTERNAL", "failed to read the agent audit trail", err)
		return
	}
	out := apicontract.ListAgentAuditResponse{Entries: make([]apicontract.AgentAuditEntry, 0, len(entries)), NextCursor: next}
	for _, e := range entries {
		out.Entries = append(out.Entries, e.Contract())
	}
	writeJSON(w, http.StatusOK, out)
}
