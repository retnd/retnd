package main

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// The agent confirmation routes (#1349), served by the fake engine off the
// real BackupService behind it, for the reason fakeengine_encryption_996_test.go
// gives.
//
// Two things are stood in for. The step-up is checked here the way the engine
// checks it (the header must carry the administrator's password, and the
// request must carry no body), so a verb that forgot to send it is refused
// rather than waved through. And the replay itself, which belongs to
// apps/common/webhost and is proved there, is answered the way that router
// answers: POST /operations meets the shut destructive gate, anything else is
// served.

// serveAgentConfirmations reports whether it answered the request.
func (e *fakeEngine) serveAgentConfirmations(w http.ResponseWriter, r *http.Request, path string) bool {
	const prefix = "/agent/confirmations"
	if path != prefix && !strings.HasPrefix(path, prefix+"/") {
		return false
	}
	if path == prefix && r.Method == http.MethodGet {
		got, err := e.svc.ListAgentConfirmations(r.Context())
		if err != nil {
			refuseServiceError(w, "listAgentConfirmations", err)
			return true
		}
		out := apicontract.ListAgentConfirmationsResponse{Confirmations: make([]apicontract.AgentConfirmation, 0, len(got))}
		for _, c := range got {
			out.Confirmations = append(out.Confirmations, c.Contract())
		}
		writeJSON(w, http.StatusOK, out)
		return true
	}

	rest := strings.TrimPrefix(path, prefix+"/")
	id, verb, ok := strings.Cut(rest, "/")
	if !ok || r.Method != http.MethodPost {
		return false
	}
	switch verb {
	case "deny":
		c, err := e.svc.DenyAgentConfirmation(r.Context(), id, e.username)
		if err != nil {
			refuseConfirmationError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, c.Contract())
		return true
	case "approve":
		if first, _ := io.ReadAll(io.LimitReader(r.Body, 1)); len(first) > 0 {
			refuse(w, http.StatusBadRequest, apicontract.ErrorCodeAgentConfirmationBodySubstituted, "a body was sent with the approval")
			return true
		}
		if r.Header.Get("X-Retnd-Step-Up-Password") != e.password {
			refuse(w, http.StatusForbidden, apicontract.ErrorCodeStepUpRequired, "approving asks for your password again")
			return true
		}
		c, _, err := e.svc.ApproveAgentConfirmation(r.Context(), id, e.username)
		if err != nil {
			refuseConfirmationError(w, err)
			return true
		}
		exec := service.AgentExecution{Status: http.StatusOK, Message: "OK", CorrelationID: "cid_fake"}
		if c.Route == "/operations" {
			exec = service.AgentExecution{
				Status: http.StatusForbidden, Code: string(apicontract.ErrorCodeDestructiveOperationsDisabled),
				Message: "destructive operations are refused over HTTP in this release", CorrelationID: "cid_fake",
			}
		}
		exec.ExitCode = service.AgentExecutionExitCode(exec.Status, exec.Code)
		if err := e.svc.FinishAgentApproval(r.Context(), c, e.username, exec); err != nil {
			refuseServiceError(w, "approveAgentConfirmation", err)
			return true
		}
		c.Execution = &exec
		writeJSON(w, http.StatusOK, c.Contract())
		return true
	}
	return false
}

func refuseConfirmationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrAgentConfirmationNotFound):
		refuse(w, http.StatusNotFound, apicontract.ErrorCodeAgentConfirmationNotFound, err.Error())
	case errors.Is(err, service.ErrAgentConfirmationExpired):
		refuse(w, http.StatusConflict, apicontract.ErrorCodeAgentConfirmationExpired, err.Error())
	case errors.Is(err, service.ErrAgentConfirmationNotPending):
		refuse(w, http.StatusConflict, apicontract.ErrorCodeAgentConfirmationDecided, err.Error())
	default:
		refuseServiceError(w, "agentConfirmation", err)
	}
}
