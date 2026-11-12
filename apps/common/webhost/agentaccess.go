package webhost

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// The agent principal: a bearer token that authenticates /api/v1 as
// agent:<name> (EPIC U, FR-91 and FR-92; ADR 0034 decisions 2 and 4).
//
// Only a bearer that carries the rat_ prefix is an agent token; any other
// Authorization header is ignored, as it always was. A token is a second way to be authenticated here, never a way around the
// first. It is checked against its row on every request, a request that
// presents one is never also given the benefit of a cookie, and it reaches
// strictly less than a session does:
//
//   - the routes in agentDeniedPrefixes refuse a bearer whatever its scope,
//     before the scope is looked at;
//   - a bearer reaches no route that changes anything. A read token is told
//     so (AGENT_SCOPE_INSUFFICIENT); an operate or configure token is told a
//     person has to confirm the change (AGENT_CONFIRMATION_REQUIRED), which is
//     how U2.1 will take it. Until then nothing a token can do mutates;
//   - requireCSRF passes a bearer principal and nobody else. CSRF defends a
//     credential a browser sends by itself, and a header is not one.

// AgentAccess is what the bearer authenticator and the three token routes
// need from core/service. *service.BackupService satisfies it; NewRouter finds
// it by asserting it on the backend, the way it finds an ActionRecorder, so a
// backend that cannot keep tokens simply has no agent access.
type AgentAccess interface {
	AuthenticateAgentToken(ctx context.Context, presented string) (service.AgentPrincipal, error)
	RecordAgentRefusal(ctx context.Context, refusal service.AgentRefusal) error
	MintAgentToken(ctx context.Context, req service.MintAgentTokenRequest) (service.MintedAgentToken, error)
	ListAgentTokens(ctx context.Context) ([]service.AgentToken, error)
	RevokeAgentToken(ctx context.Context, id string) (service.AgentToken, error)
}

// agentDeniedPrefixes are the routes no token reaches under any scope. They
// are paths below /api/v1. /auth/* is mounted beside the router rather than
// inside it, so serve's mount refuses it too (AgentDeniedByMount).
var agentDeniedPrefixes = []string{"/auth", "/system/first-run", "/recoveries", "/agent/tokens"}

type principalContextKey struct{}

// withAgentPrincipal marks ctx as carrying an authenticated agent. It is
// unexported so nothing outside this package can plant one.
func withAgentPrincipal(ctx context.Context, p service.AgentPrincipal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

func agentPrincipalFrom(ctx context.Context) (service.AgentPrincipal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(service.AgentPrincipal)
	return p, ok
}

// bearerToken reports whether the request presents an agent token and, if so,
// what it is. A bearer is an agent token only if it carries the rat_ prefix;
// any other Authorization header, Bearer or not, is not ours and is ignored,
// so a front proxy's own Bearer header is left alone.
func bearerToken(r *http.Request) (string, bool) {
	scheme, rest, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(rest)
	if !strings.HasPrefix(token, service.AgentTokenPrefix) {
		return "", false
	}
	return token, true
}

// agentRouteDenied reports whether a path below /api/v1 is one a bearer may
// never reach. Both the decoded and the raw form are checked, because the
// router matches on whichever the request carries.
func agentRouteDenied(r *http.Request) bool {
	candidates := []string{r.URL.Path}
	if r.URL.RawPath != "" {
		candidates = append(candidates, r.URL.RawPath)
	}
	for _, c := range candidates {
		rel := strings.TrimPrefix(path.Clean("/"+c), "/api/v1")
		for _, p := range agentDeniedPrefixes {
			if rel == p || strings.HasPrefix(rel, p+"/") {
				return true
			}
		}
	}
	return false
}

// AgentDeniedByMount refuses a bearer request before it reaches a handler
// mounted outside the API router (the /api/v1/auth routes), whatever the token
// is. A request with no bearer header passes untouched.
func AgentDeniedByMount(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := bearerToken(r); ok {
			writeError(w, http.StatusForbidden, "AGENT_ROUTE_DENIED", "an agent token cannot reach this route")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authenticateBearer is authMiddleware's branch for a request that presents a
// bearer token. It returns the request to serve, or false after writing a
// refusal.
func authenticateBearer(w http.ResponseWriter, r *http.Request, agents AgentAccess, presented string) (*http.Request, bool) {
	// audit records a refusal that was just written. The refusal stands
	// whether or not the row lands, so a failure to write it is logged and
	// nothing else.
	audit := func(tokenID string, status int, code, correlationID string) {
		err := agents.RecordAgentRefusal(r.Context(), service.AgentRefusal{
			TokenID: tokenID, Method: r.Method, Path: r.URL.Path, Status: status, Code: code, CorrelationID: correlationID,
		})
		if err != nil {
			slog.WarnContext(r.Context(), "agent refusal was not recorded", "code", code, "error", err)
		}
	}

	if agentRouteDenied(r) {
		audit("", http.StatusForbidden, "AGENT_ROUTE_DENIED",
			writeError(w, http.StatusForbidden, "AGENT_ROUTE_DENIED", "an agent token cannot reach this route"))
		return nil, false
	}
	principal, err := agents.AuthenticateAgentToken(r.Context(), presented)
	if err != nil {
		// Fail closed, the way a session lookup that errors does: a token
		// that could not be checked is not a token that was accepted.
		if !errors.Is(err, service.ErrAgentTokenInvalid) {
			slog.ErrorContext(r.Context(), "the agent token could not be checked", "error", err)
		}
		audit("", http.StatusUnauthorized, "AGENT_TOKEN_INVALID",
			writeError(w, http.StatusUnauthorized, "AGENT_TOKEN_INVALID", "the agent token is not valid"))
		return nil, false
	}

	if !safeMethod(r.Method) {
		if principal.Scope == service.AgentScopeRead {
			audit(principal.TokenID, http.StatusForbidden, "AGENT_SCOPE_INSUFFICIENT",
				writeError(w, http.StatusForbidden, "AGENT_SCOPE_INSUFFICIENT", "a read token cannot start or change anything"))
			return nil, false
		}
		audit(principal.TokenID, http.StatusConflict, "AGENT_CONFIRMATION_REQUIRED",
			writeError(w, http.StatusConflict, "AGENT_CONFIRMATION_REQUIRED", "a change made with an agent token has to be confirmed by a person"))
		return nil, false
	}

	actor := "agent:" + principal.Name
	ctx := context.WithValue(r.Context(), actorContextKey{}, actor)
	ctx = context.WithValue(ctx, authContextKey{}, authContextForAgent(actor))
	return r.WithContext(withAgentPrincipal(ctx, principal)), true
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func (h *handlers) agentTokens(w http.ResponseWriter) (AgentAccess, bool) {
	if h.agents == nil {
		writeError(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", "this deployment cannot keep agent tokens")
		return nil, false
	}
	return h.agents, true
}

// listAgentTokens is GET /api/v1/agent/tokens.
func (h *handlers) listAgentTokens(w http.ResponseWriter, r *http.Request) {
	agents, ok := h.agentTokens(w)
	if !ok {
		return
	}
	tokens, err := agents.ListAgentTokens(r.Context())
	if err != nil {
		h.internalError(w, r, "INTERNAL", "failed to list agent tokens", err)
		return
	}
	out := apicontract.ListAgentTokensResponse{Tokens: make([]apicontract.AgentToken, 0, len(tokens))}
	for _, t := range tokens {
		out.Tokens = append(out.Tokens, t.Contract())
	}
	writeJSON(w, http.StatusOK, out)
}

// mintAgentToken is POST /api/v1/agent/tokens. The owner is the signed-in
// user, never a field of the body.
func (h *handlers) mintAgentToken(w http.ResponseWriter, r *http.Request) {
	agents, ok := h.agentTokens(w)
	if !ok {
		return
	}
	var body apicontract.MintAgentTokenRequest
	if !decodeStrict(w, r, &body) {
		return
	}
	minted, err := agents.MintAgentToken(r.Context(), service.MintAgentTokenRequest{
		Name: body.Name, Scope: body.Scope, ExpiresIn: body.ExpiresIn, Owner: actorFromContext(r.Context()),
	})
	if errors.Is(err, service.ErrInvalidRequest) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if err != nil {
		h.internalError(w, r, "INTERNAL", "failed to mint an agent token", err)
		return
	}
	writeJSON(w, http.StatusCreated, apicontract.MintedAgentToken{Token: minted.Token, AgentToken: minted.AgentToken.Contract()})
}

// revokeAgentToken is POST /api/v1/agent/tokens/{id}/revoke.
func (h *handlers) revokeAgentToken(w http.ResponseWriter, r *http.Request) {
	agents, ok := h.agentTokens(w)
	if !ok {
		return
	}
	revoked, err := agents.RevokeAgentToken(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, service.ErrAgentTokenNotFound) {
		writeError(w, http.StatusNotFound, "AGENT_TOKEN_NOT_FOUND", "no such agent token")
		return
	}
	if err != nil {
		h.internalError(w, r, "INTERNAL", "failed to revoke an agent token", err)
		return
	}
	writeJSON(w, http.StatusOK, revoked.Contract())
}
