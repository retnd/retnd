package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// The agent token routes (#1345), served by the fake engine off the real
// BackupService behind it, for the reason fakeengine_encryption_996_test.go
// gives: a fake that answered from a fixture would let a verb that never
// reached the engine look correct.
//
// The projection to the wire is mirrored here, as the other hooks do; the
// client's own contract check is what catches a mistake in the mirror.

// serveAgentTokens reports whether it answered the request.
func (e *fakeEngine) serveAgentTokens(w http.ResponseWriter, r *http.Request, path string) bool {
	switch {
	case path == "/agent/tokens" && r.Method == http.MethodGet:
		tokens, err := e.svc.ListAgentTokens(r.Context())
		if err != nil {
			refuseServiceError(w, "listAgentTokens", err)
			return true
		}
		out := apicontract.ListAgentTokensResponse{Tokens: make([]apicontract.AgentToken, 0, len(tokens))}
		for _, t := range tokens {
			out.Tokens = append(out.Tokens, mirrorAgentToken(t))
		}
		writeJSON(w, http.StatusOK, out)
		return true
	case path == "/agent/tokens" && r.Method == http.MethodPost:
		var body apicontract.MintAgentTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
			return true
		}
		minted, err := e.svc.MintAgentToken(r.Context(), service.MintAgentTokenRequest{
			Name: body.Name, Scope: body.Scope, ExpiresIn: body.ExpiresIn, Owner: e.username,
		})
		if err != nil {
			refuseServiceError(w, "mintAgentToken", err)
			return true
		}
		writeJSON(w, http.StatusCreated, apicontract.MintedAgentToken{Token: minted.Token, AgentToken: mirrorAgentToken(minted.AgentToken)})
		return true
	case strings.HasPrefix(path, "/agent/tokens/") && strings.HasSuffix(path, "/revoke") && r.Method == http.MethodPost:
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/agent/tokens/"), "/revoke")
		revoked, err := e.svc.RevokeAgentToken(r.Context(), id)
		if errors.Is(err, service.ErrAgentTokenNotFound) {
			refuse(w, http.StatusNotFound, apicontract.ErrorCodeAgentTokenNotFound, err.Error())
			return true
		}
		if err != nil {
			refuseServiceError(w, "revokeAgentToken", err)
			return true
		}
		writeJSON(w, http.StatusOK, mirrorAgentToken(revoked))
		return true
	}
	return false
}

func mirrorAgentToken(t service.AgentToken) apicontract.AgentToken {
	out := apicontract.AgentToken{
		ID: t.ID, Name: t.Name, Prefix: t.Prefix, Scope: t.Scope, Owner: t.Owner, Status: t.Status,
		CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339), ExpiresAt: t.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if t.LastUsedAt != nil {
		s := t.LastUsedAt.UTC().Format(time.RFC3339)
		out.LastUsedAt = &s
	}
	if t.RevokedAt != nil {
		s := t.RevokedAt.UTC().Format(time.RFC3339)
		out.RevokedAt = &s
	}
	return out
}
